package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// This file is what a setup command needs and nothing else: guess where a mailbox lives, prove the
// guess and the password are right, and write the account file. It is here rather than in cmd/ so the
// verification runs the SAME code path a later send takes — a check that proves a different path
// proves nothing.

// Discover finds an address's mail servers, best effort.
//
// SRV first (RFC 6186): a domain that publishes _imaps._tcp and _submissions._tcp is telling us
// exactly where to go, and a household on a provider that does gets it right without typing. Failing
// that, the conventional names, because "imap.<domain>:993" is right far more often than it is wrong
// and a wrong guess costs nothing here — Verify runs before anything is written.
//
// Whatever comes back is a proposal. Nothing in this package treats it as authoritative.
func Discover(ctx context.Context, address string) (imapAddr, smtpAddr string) {
	domain := domainOf(address)
	if domain == "" {
		return "", ""
	}
	var r net.Resolver
	lookup := func(service string) string {
		_, recs, err := r.LookupSRV(ctx, service, "tcp", domain)
		if err != nil || len(recs) == 0 || recs[0].Target == "." {
			return "" // "." is the RFC 6186 way of saying "this service is not offered"
		}
		return net.JoinHostPort(strings.TrimSuffix(recs[0].Target, "."), strconv.Itoa(int(recs[0].Port)))
	}
	imapAddr = lookup("imaps")
	if imapAddr == "" {
		imapAddr = "imap." + domain + ":993"
	}
	smtpAddr = lookup("submissions")
	if smtpAddr == "" {
		smtpAddr = "smtp." + domain + ":" + submissionTLSPort
	}
	return imapAddr, smtpAddr
}

// Verify proves the account works before it is written down: it logs into the mailbox and
// authenticates to the submission server, then hangs up. Nothing is sent.
//
// It exists because the alternative is finding out at six in the morning, in a cron agent's
// transcript nobody is reading. Both halves are always attempted and their failures JOINED, because a
// mailbox that reads but cannot send is a real and common state — a provider wanting an app password
// for one of them — and stopping at the first failure would hide the second.
func Verify(ctx context.Context, acct Account, imapPassword, smtpPassword string) error {
	var imapErr error
	if c, err := Dial(ctx, acct, imapPassword); err != nil {
		imapErr = err
	} else {
		if _, err := c.List("INBOX", 1); err != nil {
			imapErr = err
		}
		c.Close()
	}
	return errors.Join(imapErr, verifySubmission(ctx, acct, smtpPassword))
}

// verifySubmission authenticates to the submission server and hangs up without sending.
func verifySubmission(ctx context.Context, acct Account, password string) error {
	host, port, err := net.SplitHostPort(acct.SMTPAddr)
	if err != nil {
		return fmt.Errorf("mail: smtp address %q: %w", acct.SMTPAddr, err)
	}
	c, err := connectSMTP(ctx, acct, password, host, port)
	if err != nil {
		return err
	}
	return c.Close()
}

// SaveAccount writes the account file, 0600 and via a temp file in the same directory, so a crashed
// write leaves the previous account intact rather than a half-written one. The password is not here
// and cannot be: the file has no field for it.
func SaveAccount(path string, acct Account) error {
	f := accountFile{IMAP: acct.IMAPAddr, SMTP: acct.SMTPAddr, User: acct.User, From: acct.From}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".mail-*.json")
	if err != nil {
		return fmt.Errorf("mail: write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Flush before the rename: the rename is atomic in the directory, which says nothing about the
	// bytes having reached the disk first. Without this a power loss can persist the new name over
	// the old file with nothing behind it. The containing directory is deliberately NOT synced —
	// that is the difference between "the previous account survives" and "the new one survives", and
	// the recovery for the second is running setup again.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
