package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// The account is a plain readable file in the workspace directory, next to reminders.json, and it
// holds no password. That split is what lets a household look at where their mail comes from without
// unlocking anything, and it is why the vault entry names are constants in send.go rather than fields
// here — a configuration that could NAME its secret would let whoever edits the file point the
// credential somewhere else. internal/extension states the general form of that rule: a manifest
// names the credential, a config only fills in values. Mail is not an extension yet, so the names are
// constants instead.

const (
	// Dir is the mailbox's folder inside the workspace. A folder rather than a bare file, and NOT a
	// folder under extensions/: mail is built into the host — nothing is installed, there is no
	// artifact, and a workspace has exactly one mailbox — so it does not belong in the tree of
	// installed things. What the folder is for is the credential: a secret shard is keyed by its
	// PATH, so the passwords can only belong to the mailbox if the mailbox has a place of its own.
	// Delete the folder and the passwords go with it, which is the whole difference from the two
	// fixed vault names this replaced.
	Dir = "mail"

	// ConfigFile is the account file's name inside that folder.
	ConfigFile = "mail.json"
)

// accountFile is the on-disk form. Separate from Account so the wire names stay short and the Go
// field names stay explicit.
type accountFile struct {
	IMAP string `json:"imap"` // host:port
	SMTP string `json:"smtp"` // host:port
	User string `json:"user"` // the login
	From string `json:"from"` // the address messages are sent as; defaults to user
}

// LoadAccount reads the account file. A missing file is not an error — it means this workspace has no
// mailbox, and the mail tools are then not offered at all rather than offered and failing on every
// call. That is the same shape knowledge_search has without an embedder.
func LoadAccount(path string) (Account, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, fmt.Errorf("mail: read %s: %w", path, err)
	}
	var f accountFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Account{}, false, fmt.Errorf("mail: %s: %w", path, err)
	}
	if f.IMAP == "" || f.SMTP == "" || f.User == "" {
		return Account{}, false, fmt.Errorf("mail: %s: imap, smtp and user are all required", path)
	}
	from := f.From
	if from == "" {
		from = f.User
	}
	return Account{IMAPAddr: f.IMAP, SMTPAddr: f.SMTP, User: f.User, From: from}, true, nil
}
