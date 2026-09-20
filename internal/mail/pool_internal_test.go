package mail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// These tests are about the connection the Mailbox keeps: that there is one rather than one per call,
// that a server hanging up is survivable, and that a wrong request is NOT retried into a second login
// that hides it.

// countingDialer stands in for the network: it counts logins and hands back a REAL Client, spoken to
// by a scripted server over a pipe. Real, because a Client that cannot be closed would make the
// lifecycle half of these tests meaningless.
type countingDialer struct {
	mu    sync.Mutex
	dials int
	fail  error // when set, dialling fails
}

func (d *countingDialer) dial(ctx context.Context, _ Account, _ string) (*Client, error) {
	d.mu.Lock()
	if d.fail != nil {
		err := d.fail
		d.mu.Unlock()
		return nil, err
	}
	d.dials++
	d.mu.Unlock()

	client, server := net.Pipe()
	go serveOK(server)
	return NewClient(ctx, client, "ich@firma.de", "geheim")
}

func (d *countingDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// serveOK is the smallest server a login and a logout need. The protocol is exercised properly in
// client_test.go; here it only has to be real enough that a Client can be opened and closed.
func serveOK(conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	send := func(line string) {
		w.WriteString(line + "\r\n")
		w.Flush()
	}
	send("* OK [CAPABILITY IMAP4rev2] ready")

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		tag, rest, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		if cmd, _, _ := strings.Cut(rest, " "); strings.EqualFold(cmd, "LOGOUT") {
			send("* BYE")
			send(tag + " OK LOGOUT completed")
			return
		}
		send(tag + " OK completed")
	}
}

func pooled(t *testing.T) (*Mailbox, *countingDialer) {
	t.Helper()
	d := &countingDialer{}
	m := New(Config{
		Account:  Account{IMAPAddr: "imap.firma.de:993", User: "ich@firma.de", From: "ich@firma.de"},
		Password: func(string) (string, bool) { return "geheim", true },
	})
	m.dial = d.dial
	t.Cleanup(m.Close)
	return m, d
}

// TestWithClientReusesOneConnection is the whole point: a conversation is list, then read, then read,
// and a login for each of them is three handshakes the household pays for one question.
func TestWithClientReusesOneConnection(t *testing.T) {
	m, d := pooled(t)
	for range 3 {
		if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
			t.Fatalf("withClient: %v", err)
		}
	}
	if got := d.count(); got != 1 {
		t.Errorf("%d logins for 3 operations, want 1", got)
	}
}

// TestWithClientRetriesOnceAfterTheServerHangsUp: an IMAP server drops an idle session whenever it
// likes. Without the retry that surfaces to the person as an error instead of their mail.
func TestWithClientRetriesOnceAfterTheServerHangsUp(t *testing.T) {
	m, d := pooled(t)
	if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
		t.Fatalf("first call: %v", err)
	}

	calls := 0
	err := m.withClient(t.Context(), func(*Client) error {
		calls++
		if calls == 1 {
			return io.EOF // the kept connection is gone
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 2 {
		t.Errorf("the operation ran %d times, want 2 (once on the dead connection, once after)", calls)
	}
	if got := d.count(); got != 2 {
		t.Errorf("%d logins, want 2 (the original and the reconnect)", got)
	}
}

// TestWithClientDoesNotRetryAFreshConnection pins the guard against a login loop: a transport error on
// a connection this very call opened is a real failure, not a stale socket.
func TestWithClientDoesNotRetryAFreshConnection(t *testing.T) {
	m, d := pooled(t)
	calls := 0
	err := m.withClient(t.Context(), func(*Client) error {
		calls++
		return io.EOF
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want the transport error surfaced", err)
	}
	if calls != 1 {
		t.Errorf("the operation ran %d times on a fresh connection, want 1", calls)
	}
	if got := d.count(); got != 1 {
		t.Errorf("%d logins, want 1 — a fresh connection must not be re-dialled", got)
	}
}

// TestWithClientDropsAConnectionThatFailedAtTheTransport is what makes the retry above worth having
// past the first failure: what is KEPT has to be usable. A dead socket left in the pool costs the
// next operation a round trip before it can discover the same thing again — and it only finds its way
// back to the retry at all if that second failure is classified as transport too, which a protocol
// desync is not.
func TestWithClientDropsAConnectionThatFailedAtTheTransport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		warm  bool // open a connection first, so the failing one is not the one this call dialled
		want  int  // logins by the end
		calls int  // times the operation ran
	}{
		{name: "a fresh connection", warm: false, want: 1, calls: 1},
		{name: "a kept one, and the retry fails too", warm: true, want: 2, calls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, d := pooled(t)
			if tc.warm {
				if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
					t.Fatalf("warming the connection: %v", err)
				}
			}
			calls := 0
			err := m.withClient(t.Context(), func(*Client) error {
				calls++
				return io.EOF
			})
			if !errors.Is(err, io.EOF) {
				t.Fatalf("err = %v, want the transport error surfaced", err)
			}
			if calls != tc.calls {
				t.Errorf("the operation ran %d times, want %d", calls, tc.calls)
			}
			if got := d.count(); got != tc.want {
				t.Fatalf("%d logins during the failing call, want %d", got, tc.want)
			}

			// The point of the test: nothing broken was kept, so the next call dials rather than
			// handing the same dead connection out again.
			if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
				t.Fatalf("the call after the failure: %v", err)
			}
			if got := d.count(); got != tc.want+1 {
				t.Errorf("%d logins, want %d — the failed connection was kept and handed out again",
					got, tc.want+1)
			}
		})
	}
}

// TestWithClientDoesNotRetryARequestError is the other half, and the one that keeps debugging
// possible: "no such UID" is an answer. Retrying it would hide it behind a second login and double
// every future investigation.
func TestWithClientDoesNotRetryARequestError(t *testing.T) {
	m, d := pooled(t)
	if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
		t.Fatalf("first call: %v", err)
	}
	calls := 0
	err := m.withClient(t.Context(), func(*Client) error {
		calls++
		return ErrNotFound
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if calls != 1 {
		t.Errorf("a request error was retried %d times", calls-1)
	}
	if got := d.count(); got != 1 {
		t.Errorf("%d logins, want 1 — a request error must not reconnect", got)
	}
}

// TestCloseStopsTheMailbox pins that a workspace on its way out cannot have a fresh session opened
// underneath it, and that closing twice is fine.
func TestCloseStopsTheMailbox(t *testing.T) {
	m, d := pooled(t)
	if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
		t.Fatalf("withClient: %v", err)
	}
	m.Close()
	m.Close() // idempotent

	if err := m.withClient(t.Context(), func(*Client) error { return nil }); err == nil {
		t.Error("an operation after Close succeeded")
	}
	if got := d.count(); got != 1 {
		t.Errorf("%d logins, want 1 — Close must not be followed by a new one", got)
	}
}

// TestWithClientIsSerialised pins that two callers never share the connection at the same time. A
// Client carries a selected folder, so an overlap is a folder race, and -race would not catch it on
// its own because the collision is logical rather than a data race on our side.
func TestWithClientIsSerialised(t *testing.T) {
	m, _ := pooled(t)
	var mu sync.Mutex
	inside, maxInside := 0, 0

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.withClient(t.Context(), func(*Client) error {
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Errorf("%d operations ran at once, want 1", maxInside)
	}
}

// TestIsTransport pins the rule the retry hangs on.
func TestIsTransport(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"closed", net.ErrClosed, true},
		{"wrapped net error", errors.New("x"), false},
		{"not found is an answer", ErrNotFound, false},
		{"a wrapped not found is still an answer", errors.Join(ErrNotFound, errors.New("uid 9")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransport(tc.err); got != tc.want {
				t.Errorf("isTransport(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestBoundRestampsTheDeadline is the regression test for what keeping a connection would otherwise
// break. A deadline is an absolute time, so one stamped at dial expires under every operation after
// the first — invisible while callers dialled per operation, and instant once one is kept: a second
// call failing on a perfectly healthy socket.
func TestBoundRestampsTheDeadline(t *testing.T) {
	client, server := net.Pipe()
	go serveOK(server)
	rec := &deadlineRecorder{Conn: client}
	c, err := NewClient(t.Context(), rec, "ich@firma.de", "geheim")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Put the connection where a kept one ends up: past its deadline.
	if err := c.conn.SetDeadline(time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := c.bound(t.Context()); err != nil {
		t.Fatalf("bound: %v", err)
	}
	if got := rec.deadline(); !got.After(time.Now()) {
		t.Errorf("bound left the deadline at %v, which is not in the future", got)
	}
}

// deadlineRecorder is a connection that remembers the last deadline stamped on it.
//
// The test asks what bound DID, rather than performing I/O afterwards and inferring it. An expired
// deadline on a live connection is observed by go-imap's own reader too, which closes the connection
// when it trips — so a write after bound answers "did the reader get there first", and the answer
// depends on the machine. That is a coin toss dressed up as an assertion, and it flaked.
type deadlineRecorder struct {
	net.Conn
	mu   sync.Mutex
	last time.Time
}

func (d *deadlineRecorder) SetDeadline(t time.Time) error {
	d.mu.Lock()
	d.last = t
	d.mu.Unlock()
	return d.Conn.SetDeadline(t)
}

func (d *deadlineRecorder) deadline() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// TestIdleReaperClosesAnUnusedConnection: an authenticated IMAP session is a slot the household's own
// server counts, so a mailbox nobody is reading must not hold one forever.
func TestIdleReaperClosesAnUnusedConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, d := pooled(t)
		m.idleAfter = time.Minute

		if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
			t.Fatalf("withClient: %v", err)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()

		m.mu.Lock()
		open := m.conn != nil
		m.mu.Unlock()
		if open {
			t.Error("an idle connection was still open past the reaper")
		}
		if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
			t.Fatalf("after the reaper: %v", err)
		}
		if got := d.count(); got != 2 {
			t.Errorf("%d logins, want 2 — reaped, then opened again on demand", got)
		}
	})
}

// TestIdleReaperKeepsAConnectionInUse pins the generation guard: a connection used again before the
// reaper's deadline stays, and the earlier reaper does not take it down behind the caller's back.
func TestIdleReaperKeepsAConnectionInUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, d := pooled(t)
		m.idleAfter = time.Minute

		for range 3 {
			if err := m.withClient(t.Context(), func(*Client) error { return nil }); err != nil {
				t.Fatalf("withClient: %v", err)
			}
			time.Sleep(50 * time.Second) // each use re-arms, so the reaper never reaches its minute
			synctest.Wait()
		}
		m.mu.Lock()
		open := m.conn != nil
		m.mu.Unlock()
		if !open {
			t.Error("a connection in steady use was reaped")
		}
		if got := d.count(); got != 1 {
			t.Errorf("%d logins across three uses, want 1", got)
		}
	})
}
