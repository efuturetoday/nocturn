package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/efuturetoday/nocturn/internal/mail"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// `nocturn mail` is the one path that writes a mailbox into a workspace, and it earns its existence
// over a text editor in exactly three ways: it guesses the servers from the address, it PROVES the
// account works before writing anything, and it puts the password in the vault in the same breath —
// off the command line and out of the shell history.

const mailSetupTimeout = 30 * time.Second

func cmdMail(args []string) int {
	if len(args) == 0 {
		mailUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "setup":
		return cmdMailSetup(args[1:])
	case "check":
		return cmdMailCheck(args[1:])
	case "help", "-h", "--help":
		mailUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "nocturn mail: unknown subcommand %q\n\n", args[0])
		mailUsage(os.Stderr)
		return 2
	}
}

func mailUsage(w io.Writer) {
	io.WriteString(w, `usage: nocturn mail setup --user <address> [--imap host:port] [--smtp host:port]
                              [--from <address>] [-w workspace]   (password on stdin)
       nocturn mail check [-w workspace]

setup finds the servers from the address when they are not given, logs in to check both of
them, and only then writes mail.json and stores the password in the workspace vault.

The password is read from stdin, so it never enters your shell history or the process list:
  printf %s "$PASSWORD" | nocturn mail setup --user ich@firma.de -w main

check connects with what is already configured and reports what it sees.
`)
}

func cmdMailSetup(args []string) int {
	fs := flag.NewFlagSet("mail setup", flag.ContinueOnError)
	ws := workspaceFlag(fs)
	user := fs.String("user", "", "the mailbox login, usually the address itself")
	from := fs.String("from", "", "the address messages are sent as (default: --user)")
	imapAddr := fs.String("imap", "", "IMAP server as host:port (default: discovered)")
	smtpAddr := fs.String("smtp", "", "submission server as host:port (default: discovered)")
	fs.Usage = func() { mailUsage(os.Stderr) }
	if _, code, done := parseArgs(fs, args); done {
		return code
	}
	if *user == "" {
		fs.Usage()
		return 2
	}
	if err := runMailSetup(*ws, *user, *from, *imapAddr, *smtpAddr); err != nil {
		fmt.Fprintln(os.Stderr, "mail setup:", err)
		return 1
	}
	return 0
}

func runMailSetup(wsName, user, from, imapAddr, smtpAddr string) error {
	wsDir := filepath.Join(wsRoot, wsName)
	if _, err := os.Stat(wsDir); err != nil {
		return fmt.Errorf("no workspace %q at %s", wsName, wsDir)
	}
	vault, err := openWorkspaceVault(wsName, wsDir)
	if err != nil {
		return err
	}
	password, err := readPasswordFromStdin()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), mailSetupTimeout)
	defer cancel()

	// Discovery fills only what was not given: a household that knows its servers must never have
	// them overridden by a guess.
	if imapAddr == "" || smtpAddr == "" {
		discoveredIMAP, discoveredSMTP := mail.Discover(ctx, user)
		if imapAddr == "" {
			imapAddr = discoveredIMAP
		}
		if smtpAddr == "" {
			smtpAddr = discoveredSMTP
		}
		fmt.Printf("servers: imap %s · smtp %s\n", imapAddr, smtpAddr)
	}
	if imapAddr == "" || smtpAddr == "" {
		return errors.New("could not work out the servers — pass --imap and --smtp")
	}
	if from == "" {
		from = user
	}
	acct := mail.Account{IMAPAddr: imapAddr, SMTPAddr: smtpAddr, User: user, From: from}

	// The same password for both halves, which is what a mailbox normally is. Where a provider wants
	// separate ones, the second is set afterwards with `nocturn secret set`.
	if err := mail.Verify(ctx, acct, password, password); err != nil {
		// Nothing is written on a failed check. A half-configured mailbox fails later, somewhere
		// else, in a transcript nobody reads.
		return err
	}

	if err := vault.Set(mail.SecretIMAPPassword, []byte(password)); err != nil {
		return fmt.Errorf("store the mailbox password: %w", err)
	}
	if err := vault.Set(mail.SecretSMTPPassword, []byte(password)); err != nil {
		return fmt.Errorf("store the submission password: %w", err)
	}
	if err := mail.SaveAccount(filepath.Join(wsDir, mail.ConfigFile), acct); err != nil {
		return err
	}
	fmt.Printf("mailbox %s configured in workspace %q — restart the daemon or run `nocturn reload` to pick it up\n", user, wsName)
	return nil
}

func cmdMailCheck(args []string) int {
	fs := flag.NewFlagSet("mail check", flag.ContinueOnError)
	ws := workspaceFlag(fs)
	fs.Usage = func() { mailUsage(os.Stderr) }
	if _, code, done := parseArgs(fs, args); done {
		return code
	}
	if err := runMailCheck(*ws); err != nil {
		fmt.Fprintln(os.Stderr, "mail check:", err)
		return 1
	}
	return 0
}

func runMailCheck(wsName string) error {
	wsDir := filepath.Join(wsRoot, wsName)
	acct, ok, err := mail.LoadAccount(filepath.Join(wsDir, mail.ConfigFile))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("workspace %q has no mailbox — run `nocturn mail setup --user <address> -w %s`", wsName, wsName)
	}
	vault, err := openWorkspaceVault(wsName, wsDir)
	if err != nil {
		return err
	}
	password, ok := vault.Get(mail.SecretIMAPPassword)
	if !ok {
		return fmt.Errorf("no password stored for %s — run `nocturn mail setup`", acct.User)
	}

	ctx, cancel := context.WithTimeout(context.Background(), mailSetupTimeout)
	defer cancel()

	c, err := mail.Dial(ctx, acct, string(password))
	if err != nil {
		return err
	}
	defer c.Close()
	headers, err := c.List(ctx, "INBOX", 1)
	if err != nil {
		return err
	}
	fmt.Printf("%s at %s: ok\n", acct.User, acct.IMAPAddr)
	if len(headers) == 0 {
		fmt.Println("INBOX is empty")
		return nil
	}
	h := headers[0]
	fmt.Printf("newest: %s — %q (%s)\n", h.From, h.Subject, h.Date.Format(time.RFC1123))
	return nil
}

// openWorkspaceVault unlocks one workspace's vault under the master key, the same file and key the
// daemon opens.
func openWorkspaceVault(wsName, wsDir string) (*secret.Vault, error) {
	master, err := openMaster()
	if err != nil {
		return nil, fmt.Errorf("unlock vault: %w", err)
	}
	if master == nil {
		return nil, errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault")
	}
	return secret.OpenVault(filepath.Join(wsDir, "vault.enc"), master.WorkspaceKey(wsName))
}

// readPasswordFromStdin takes the value the way `secret set` does — off stdin, never argv, so it
// stays out of the shell history and out of the process list any other user on the box can read.
func readPasswordFromStdin() (string, error) {
	value, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<16))
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	value = bytes.TrimRight(value, "\r\n")
	if len(value) == 0 {
		return "", errors.New(`empty password on stdin (pipe it in, e.g. ` + "`printf %s \"$PASSWORD\" | nocturn mail setup --user ...`)")
	}
	return string(value), nil
}
