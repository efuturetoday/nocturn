package mail_test

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/mail"
)

// These tests drive the client against a scripted IMAP server over a pipe. What is under test is
// ours, not go-imap's: the order a listing comes back in, the flags and envelope fields that survive
// into a Header, and — the one that matters for the household — that reading peeks instead of marking
// mail as seen. The wire format is only the vehicle.

// messages is the mailbox the scripted server serves, oldest first — the order a real one answers a
// sequence range in.
var messages = []struct {
	uid           uint32
	flags         string
	date          string
	subject, from string
}{
	{101, `\Seen`, "Mon, 10 Aug 2026 09:00:00 +0200", "aeltestes", "chef@firma.de"},
	{102, ``, "Tue, 11 Aug 2026 09:00:00 +0200", "mittleres", "buero@firma.de"},
	{103, ``, "Wed, 12 Aug 2026 09:00:00 +0200", "neuestes", "chef@firma.de"},
}

// fakeServer speaks just enough IMAP4rev2 for the commands this package sends. It records every line
// it received, so a test can assert on what the client asked for and not only on what it did with the
// answer.
type fakeServer struct {
	mu    sync.Mutex
	lines []string
}

// envelope is one message's ENVELOPE in wire form: date, subject, from, sender, reply-to, to, then
// the four fields this package never reads.
func envelope(date, subject, from, to string) string {
	addr := func(a string) string {
		user, host, _ := strings.Cut(a, "@")
		return `((NIL NIL "` + user + `" "` + host + `"))`
	}
	return `("` + date + `" "` + subject + `" ` + addr(from) + ` ` + addr(from) + ` ` + addr(from) +
		` ` + addr(to) + ` NIL NIL NIL NIL)`
}

func (s *fakeServer) record(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

// commands returns every line the client sent, for a failure message.
func (s *fakeServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// sent reports whether any command the client sent contained the given fragment.
func (s *fakeServer) sent(fragment string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if strings.Contains(l, fragment) {
			return true
		}
	}
	return false
}

func (s *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	send := func(format string) {
		w.WriteString(format + "\r\n")
		w.Flush()
	}
	send("* OK [CAPABILITY IMAP4rev2] nocturn test server ready")

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.record(line)
		tag, rest, _ := strings.Cut(line, " ")
		cmd, args, _ := strings.Cut(rest, " ")
		switch strings.ToUpper(cmd) {
		case "LOGIN":
			send(tag + " OK LOGIN completed")
		case "SELECT", "EXAMINE": // a read-only select goes out as EXAMINE
			send("* 3 EXISTS")
			send("* OK [UIDVALIDITY 1] valid")
			send(tag + " OK [READ-ONLY] SELECT completed")
		case "FETCH":
			// The header listing: three messages, ascending, as a real server answers.
			for seq, m := range messages {
				send(`* ` + strconv.Itoa(seq+1) + ` FETCH (UID ` + strconv.Itoa(int(m.uid)) +
					` FLAGS (` + m.flags + `) ENVELOPE ` +
					envelope(m.date, m.subject, m.from, "ich@firma.de") + `)`)
			}
			send(tag + " OK FETCH completed")
		case "UID":
			body := "Content-Type: text/plain; charset=utf-8\r\n\r\nBis Freitag.\r\n"
			if sub, _, _ := strings.Cut(args, " "); strings.EqualFold(sub, "SEARCH") {
				send(`* ESEARCH (TAG "` + tag + `") UID ALL 101:103`)
				send(tag + " OK SEARCH completed")
				continue
			}
			if strings.Contains(args, "999") { // a UID the server does not have
				send(tag + " OK FETCH completed")
				continue
			}
			if !strings.Contains(strings.ToUpper(args), "BODY") { // a header fetch of the matches
				for seq, m := range messages {
					if strings.Contains(args, strconv.Itoa(int(m.uid))) {
						send(`* ` + strconv.Itoa(seq+1) + ` FETCH (UID ` + strconv.Itoa(int(m.uid)) +
							` FLAGS (` + m.flags + `) ENVELOPE ` +
							envelope(m.date, m.subject, m.from, "ich@firma.de") + `)`)
					}
				}
				send(tag + " OK FETCH completed")
				continue
			}
			send(`* 3 FETCH (UID 103 FLAGS () ENVELOPE ` +
				envelope("Wed, 12 Aug 2026 09:00:00 +0200", "neuestes", "chef@firma.de", "ich@firma.de") +
				` BODY[] {` + strconv.Itoa(len(body)) + `}`)
			w.WriteString(body + ")\r\n")
			w.Flush()
			send(tag + " OK FETCH completed")
		case "LOGOUT":
			send("* BYE logging out")
			send(tag + " OK LOGOUT completed")
			return
		default:
			send(tag + " OK completed")
		}
	}
}

// dial wires a client to a fresh scripted server over an in-memory pipe.
func dial(t *testing.T) (*mail.Client, *fakeServer) {
	t.Helper()
	client, server := net.Pipe()
	s := &fakeServer{}
	go s.serve(server)
	c, err := mail.NewClient(client, "ich@firma.de", "geheim")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, s
}

// TestListReturnsNewestFirst pins the order. A mailbox has no upper bound, so a listing that handed
// back the oldest messages first would fill the model's context with mail from years ago and never
// reach today's.
func TestListReturnsNewestFirst(t *testing.T) {
	c, _ := dial(t)
	got, err := c.List("INBOX", 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	want := []string{"neuestes", "mittleres", "aeltestes"}
	for i, w := range want {
		if got[i].Subject != w {
			t.Errorf("message %d: subject %q, want %q", i, got[i].Subject, w)
		}
	}
	if got[0].UID != 103 {
		t.Errorf("newest UID = %d, want 103", got[0].UID)
	}
	if got[0].From != "chef@firma.de" {
		t.Errorf("From = %q, want chef@firma.de", got[0].From)
	}
	if got[0].Seen {
		t.Error("an unflagged message came back as seen")
	}
	if !got[2].Seen {
		t.Error(`a message flagged \Seen came back as unseen`)
	}
}

// TestListAsksOnlyForTheTail pins that a limit bounds the SERVER request, not just the returned
// slice: fetching a whole mailbox and then cutting it to ten would move every message over the wire.
func TestListAsksOnlyForTheTail(t *testing.T) {
	c, s := dial(t)
	if _, err := c.List("INBOX", 2); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !s.sent("FETCH 2:3") {
		t.Errorf("expected a fetch of the last two messages, got commands: %v", s.commands())
	}
}

func TestListEmptyLimit(t *testing.T) {
	c, s := dial(t)
	got, err := c.List("INBOX", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d messages, want none", len(got))
	}
	if s.sent("SELECT") || s.sent("EXAMINE") {
		t.Error("a listing of nothing still opened the folder")
	}
}

// TestReadPeeks is the household-facing invariant: the assistant reading the inbox must not mark the
// person's mail as seen. A background agent skimming at six in the morning would otherwise empty
// their unread list before they wake up.
func TestReadPeeks(t *testing.T) {
	c, s := dial(t)
	if _, err := c.Read("INBOX", 103); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !s.sent("BODY.PEEK[") {
		t.Errorf("read did not peek — commands: %v", s.commands())
	}
	if s.sent("BODY[]") {
		t.Errorf("read fetched the body without peeking — commands: %v", s.commands())
	}
}

func TestReadReturnsTheMessage(t *testing.T) {
	c, _ := dial(t)
	got, err := c.Read("INBOX", 103)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Subject != "neuestes" {
		t.Errorf("Subject = %q, want neuestes", got.Subject)
	}
	if got.UID != 103 {
		t.Errorf("UID = %d, want 103", got.UID)
	}
	if !strings.Contains(got.Text, "Bis Freitag.") {
		t.Errorf("Text = %q, want the plain body", got.Text)
	}
	if len(got.To) != 1 || got.To[0] != "ich@firma.de" {
		t.Errorf("To = %v, want [ich@firma.de]", got.To)
	}
}

// TestReadMissingUID pins that a message gone between a listing and a read is an ordinary,
// branchable outcome rather than an opaque failure.
func TestReadMissingUID(t *testing.T) {
	c, _ := dial(t)
	_, err := c.Read("INBOX", 999)
	if err == nil {
		t.Fatal("reading a missing UID succeeded")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want a not-found error", err)
	}
}

// TestSearchRunsOnTheServer pins that a query goes out as IMAP SEARCH criteria rather than being
// applied to messages pulled down first. The whole reason mail is searched server-side is that a
// mailbox is unbounded: filtering locally would move a decade of it over the wire before matching.
func TestSearchRunsOnTheServer(t *testing.T) {
	c, s := dial(t)
	since := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.Search("INBOX", mail.Query{Text: "Dach", From: "chef", Since: since, Limit: 10}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, want := range []string{"UID SEARCH", `TEXT "Dach"`, `SINCE "1-Aug-2026"`, `FROM "chef"`} {
		if !s.sent(want) {
			t.Errorf("the query did not carry %s — commands: %v", want, s.commands())
		}
	}
}

// TestSearchReturnsNewestFirst pins the same order a listing has, and that a limit cuts to the NEWEST
// matches: a search for a common word would otherwise answer with the oldest mail in the mailbox.
func TestSearchReturnsNewestFirst(t *testing.T) {
	c, _ := dial(t)
	got, err := c.Search("INBOX", mail.Query{Text: "Freitag", Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2", len(got))
	}
	if got[0].UID != 103 || got[1].UID != 102 {
		t.Errorf("got UIDs %d,%d — want 103,102 (newest first, oldest match dropped)", got[0].UID, got[1].UID)
	}
}

func TestSearchWithoutLimitAsksNothing(t *testing.T) {
	c, s := dial(t)
	got, err := c.Search("INBOX", mail.Query{Text: "Freitag"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d matches, want none", len(got))
	}
	if s.sent("SEARCH") {
		t.Errorf("a search for no messages still went to the server: %v", s.commands())
	}
}
