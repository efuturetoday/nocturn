package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/mail"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// runSecretSet seeds a static credential value into a plugin/mcp folder's encrypted secret shard.
// The target is the owner-namespaced credential the value belongs to — the SAME identifier that shows
// up in `secret ls`, diagnostics, and the vault:
//
//	<extension>/<credential>   the credential an extension's declaration names
//	<extension>                the same, when it declares exactly one
//
// The value is read from stdin (never argv), so it can be piped from a password manager. It is sealed
// into <wsRoot>/<workspace>/<relPath>/secrets.enc under the folder-path-derived key + path-bound AAD —
// exactly the shard the daemon reads at startup (LoadShardsInto).
func runSecretSet(wsName, target string) error {
	master, err := openMaster()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if master == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before seeding a secret")
	}

	wsDir := filepath.Join(wsRoot, wsName)
	relPath, secretKey, err := resolveSecretTarget(wsDir, target)
	if err != nil {
		return err
	}

	value, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return fmt.Errorf("read value from stdin: %w", err)
	}
	value = bytes.TrimRight(value, "\r\n")
	if len(value) == 0 {
		return errors.New("empty secret value on stdin (pipe the value in, e.g. `printf %s $TOKEN | nocturn secret set ...`)")
	}

	// OpenShard creates the shard file (and its folder) on first write — no MkdirAll needed here.
	sv, err := secret.OpenShard(master, wsDir, wsName, relPath)
	if err != nil {
		return fmt.Errorf("open shard %s: %w", relPath, err)
	}
	if err := sv.Set(secretKey, value); err != nil {
		return fmt.Errorf("store %q: %w", secretKey, err)
	}
	fmt.Printf("stored %s in workspace %q\n", secretKey, wsName)
	return nil
}

// runSecretRemove deletes a seeded credential from its extension's shard. It is the counterpart of
// runSecretSet and resolves the same target, so a value can always be revoked by the name it was
// stored under — a credential channel that only ever grows is one nobody can take back.
func runSecretRemove(wsName, target string) error {
	master, err := openMaster()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if master == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before removing a secret")
	}
	wsDir := filepath.Join(wsRoot, wsName)
	relPath, secretKey, err := resolveSecretTarget(wsDir, target)
	if err != nil {
		return err
	}
	sv, err := secret.OpenShard(master, wsDir, wsName, relPath)
	if err != nil {
		return fmt.Errorf("open shard %s: %w", relPath, err)
	}
	if err := sv.Delete(secretKey); err != nil {
		return fmt.Errorf("remove %q: %w", secretKey, err)
	}
	fmt.Printf("removed %s from workspace %q\n", secretKey, wsName)
	return nil
}

// resolveSecretTarget maps a credential target to its shard folder (relPath) and the vault key the
// injector looks the value up under, so a seeded value always lands under exactly the key that gets
// injected.
//
// The target is "<extension>[/<credential>]" — one grammar, and no kind in it: an extension is one
// installed thing whatever it carries, so naming its sort would only invite naming it wrongly. The
// credential may be left out when the extension declares exactly one.
func resolveSecretTarget(wsDir, target string) (relPath, key string, err error) {
	name, cred, _ := strings.Cut(target, "/")
	if !extension.ValidName(name) {
		return "", "", fmt.Errorf("target must be <extension>[/<credential>], got %q", target)
	}
	// The mailbox is not an extension — nothing installs it — but it holds credentials in a folder of
	// its own, so it answers to the same grammar. Without this the error message the mail tools print
	// ("seed it with: nocturn secret set mail/imap") would name a command that cannot work.
	if name == mail.Dir {
		switch cred {
		case "":
			return "", "", fmt.Errorf("the mailbox has two credentials — name one: %s, %s",
				mail.CredentialIMAP, mail.CredentialSMTP)
		case mail.CredentialIMAP, mail.CredentialSMTP:
			return mail.Dir, mail.Owner + "/" + cred, nil
		default:
			return "", "", fmt.Errorf("the mailbox has no credential %q (it has: %s, %s)",
				cred, mail.CredentialIMAP, mail.CredentialSMTP)
		}
	}
	decl, values, err := workspace.DeclOf(wsDir, name)
	if err != nil {
		return "", "", err
	}
	keys, err := decl.Keys(extension.Owner(name), values)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", name, err)
	}
	if len(keys) == 0 {
		return "", "", fmt.Errorf("%s declares no credentials", name)
	}
	if cred == "" {
		if len(keys) > 1 {
			return "", "", fmt.Errorf("%s declares %d credentials — name one: %s",
				name, len(keys), strings.Join(slices.Sorted(maps.Keys(keys)), ", "))
		}
		for only := range keys {
			cred = only
		}
	}
	k, ok := keys[cred]
	if !ok {
		return "", "", fmt.Errorf("%s declares no credential %q (it has: %s)",
			name, cred, strings.Join(slices.Sorted(maps.Keys(keys)), ", "))
	}
	return extension.Dir + "/" + name, k, nil
}
