package mail_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/mail"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), mail.ConfigFile)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLoadAccount(t *testing.T) {
	path := write(t, `{"imap":"imap.firma.de:993","smtp":"smtp.firma.de:465","user":"ich@firma.de","from":"post@firma.de"}`)
	acct, ok, err := mail.LoadAccount(path)
	if err != nil || !ok {
		t.Fatalf("LoadAccount: %v, ok=%v", err, ok)
	}
	want := mail.Account{IMAPAddr: "imap.firma.de:993", SMTPAddr: "smtp.firma.de:465", User: "ich@firma.de", From: "post@firma.de"}
	if acct != want {
		t.Errorf("got %+v, want %+v", acct, want)
	}
}

// TestLoadAccountDefaultsFromToUser: the sender address is the login in every household setup that
// does not run its own domain, so making it mandatory would be a field everyone types twice.
func TestLoadAccountDefaultsFromToUser(t *testing.T) {
	path := write(t, `{"imap":"a:993","smtp":"b:465","user":"ich@firma.de"}`)
	acct, _, err := mail.LoadAccount(path)
	if err != nil {
		t.Fatalf("LoadAccount: %v", err)
	}
	if acct.From != "ich@firma.de" {
		t.Errorf("From = %q, want the user", acct.From)
	}
}

// TestLoadAccountMissingFileIsNotAnError pins the shape the whole feature hangs on: no mail.json
// means this workspace has no mailbox, so the tools are not offered at all — rather than offered and
// failing on every call, which is what a model would keep retrying.
func TestLoadAccountMissingFileIsNotAnError(t *testing.T) {
	_, ok, err := mail.LoadAccount(filepath.Join(t.TempDir(), "nothing.json"))
	if err != nil {
		t.Fatalf("a missing account file was an error: %v", err)
	}
	if ok {
		t.Error("a missing account file reported an account")
	}
}

// TestLoadAccountRejectsAnIncompleteFile: a half-configured mailbox must fail loudly at open, not at
// the first call from a cron agent at six in the morning.
func TestLoadAccountRejectsAnIncompleteFile(t *testing.T) {
	for name, content := range map[string]string{
		"no imap": `{"smtp":"b:465","user":"ich@firma.de"}`,
		"no smtp": `{"imap":"a:993","user":"ich@firma.de"}`,
		"no user": `{"imap":"a:993","smtp":"b:465"}`,
		"garbage": `not json at all`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok, err := mail.LoadAccount(write(t, content)); err == nil || ok {
				t.Errorf("accepted %s", name)
			}
		})
	}
}

// TestLoadAccountCarriesNoPassword pins the split the design rests on: whoever can read the account
// file learns where the mail lives and nothing that lets them fetch it. A password field must not
// quietly become supported later — the credential's name is a constant in the code precisely so a
// file cannot point it elsewhere.
func TestLoadAccountCarriesNoPassword(t *testing.T) {
	path := write(t, `{"imap":"a:993","smtp":"b:465","user":"ich@firma.de","password":"geheim"}`)
	acct, ok, err := mail.LoadAccount(path)
	if err != nil || !ok {
		t.Fatalf("LoadAccount: %v, ok=%v", err, ok)
	}
	if acct != (mail.Account{IMAPAddr: "a:993", SMTPAddr: "b:465", User: "ich@firma.de", From: "ich@firma.de"}) {
		t.Errorf("a password in the file changed the account: %+v", acct)
	}
}

// TestSaveAccountRoundTrips pins that what setup writes is what Open reads back — two functions, one
// file format, and a drift between them would surface as a workspace that lost its mailbox.
func TestSaveAccountRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), mail.ConfigFile)
	want := mail.Account{IMAPAddr: "imap.firma.de:993", SMTPAddr: "smtp.firma.de:465", User: "ich@firma.de", From: "post@firma.de"}
	if err := mail.SaveAccount(path, want); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	got, ok, err := mail.LoadAccount(path)
	if err != nil || !ok {
		t.Fatalf("LoadAccount: %v, ok=%v", err, ok)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestSaveAccountIsNotWorldReadable: the file names the mailbox and its login. That is not a
// credential, but it is not everyone's business on a shared machine either.
func TestSaveAccountIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), mail.ConfigFile)
	if err := mail.SaveAccount(path, mail.Account{IMAPAddr: "a:993", SMTPAddr: "b:465", User: "ich@firma.de", From: "ich@firma.de"}); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// TestSaveAccountCarriesNoPassword pins that the written file has no password field at all — the
// property the whole split rests on, checked against the bytes rather than against the struct.
func TestSaveAccountCarriesNoPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), mail.ConfigFile)
	if err := mail.SaveAccount(path, mail.Account{IMAPAddr: "a:993", SMTPAddr: "b:465", User: "ich@firma.de", From: "ich@firma.de"}); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, word := range []string{"password", "secret", "geheim"} {
		if strings.Contains(strings.ToLower(string(data)), word) {
			t.Errorf("the account file mentions %q: %s", word, data)
		}
	}
}

// TestDiscoverWithoutADomain pins the one branch that needs no DNS: an address that is not one yields
// no proposal at all, rather than "imap.:993".
func TestDiscoverWithoutADomain(t *testing.T) {
	imapAddr, smtpAddr := mail.Discover(t.Context(), "chef")
	if imapAddr != "" || smtpAddr != "" {
		t.Errorf("Discover proposed %q / %q for an address with no domain", imapAddr, smtpAddr)
	}
}
