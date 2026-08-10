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

	target := filepath.Join(dir, s.Name)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("mcp server %q already exists", s.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
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
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, ConfigFile), data, 0o600)
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
