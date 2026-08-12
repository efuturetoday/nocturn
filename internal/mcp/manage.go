package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/internal/discovery"
)

// Write declares a server: <dir>/<name>/mcp.json, where dir is a workspace's mcp/ folder.
//
// The declaration is validated before anything touches the disk, and by Server.Validate — the same
// function Discover runs. A config that would be skipped at load time is refused at write time
// instead, because a server that silently never appears is the one failure a person cannot act on.
//
// It refuses to overwrite. Replacing a server's URL under its own name would keep the folder — and
// therefore its secret shard, which is keyed by that folder's path — while pointing a credential
// that was authorized for one host at another one.
func Write(dir string, s Server) error {
	if !discovery.ValidName(s.Name) {
		return fmt.Errorf("mcp: invalid server name %q", s.Name)
	}
	if err := s.Validate(); err != nil {
		return err
	}

	// The name lives in the folder, so it is not repeated in the file — Discover overwrites a
	// manifest name with the folder's anyway (discovery.ResolveName), and a second copy could only
	// ever disagree with the first.
	decl := s
	decl.Name = ""
	data, err := json.MarshalIndent(decl, "", "  ")
	if err != nil {
		return err
	}

	// Creating the folder IS the claim on the name — os.Mkdir fails when the path exists, so two
	// callers adding the same server cannot both get past here. A Stat first and MkdirAll after would
	// let both through the check and both succeed at the create, and the second WriteFile would then
	// quietly overwrite the first server's declaration.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(dir, s.Name)
	if err := os.Mkdir(target, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("mcp server %q already exists", s.Name)
		}
		return err
	}
	if err := os.WriteFile(filepath.Join(target, ConfigFile), data, 0o600); err != nil {
		// The folder we just made is the reservation; without a declaration in it, Discover would
		// report a server that does not exist and the name would stay taken.
		_ = os.RemoveAll(target)
		return err
	}
	return nil
}

// Read returns one server's declaration from disk, with its name filled in from the folder.
//
// It exists for the caller that must know something about a server it is about to delete — its URL,
// so a remembered grant for that host can go with it. The live inventory cannot answer that: a server
// declared a moment ago is on disk but not yet in any snapshot, and reading a zero URL there would
// silently skip the revocation.
func Read(dir, name string) (Server, error) {
	if !discovery.ValidName(name) {
		return Server{}, fmt.Errorf("mcp: invalid server name %q", name)
	}
	s, err := loadServer(filepath.Join(dir, name, ConfigFile))
	if err != nil {
		return Server{}, fmt.Errorf("mcp %s: %w", name, err)
	}
	s.Name = name
	return s, nil
}

// Remove drops a server's folder — its declaration and its secret shard together.
//
// That the shard goes with it is the point, not a side effect: the shard is encrypted under a key
// derived from this folder's path, so it is readable nowhere else, and leaving it behind would be a
// token for a server nobody declared any more. Dropping <dir>/<name>/ removes exactly that server,
// which is what the layout was chosen for.
func Remove(dir, name string) error {
	if !discovery.ValidName(name) {
		return fmt.Errorf("mcp: invalid server name %q", name)
	}
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("no mcp server %q", name)
	}
	return os.RemoveAll(target)
}

// Host is the network host a server's URL points at — the target its gate grants are keyed by, and
// what a caller needs to revoke them. ok is false for a URL with no host.
func Host(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	return u.Host, true
}
