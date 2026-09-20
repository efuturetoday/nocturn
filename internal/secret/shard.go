package secret

import (
	"log/slog"
	"os"
	"path/filepath"
)

// This file loads per-item SECRET SHARDS: each extension folder may carry its own
// encrypted secrets.enc beside its manifest, encrypted under a key derived from the
// folder's workspace-relative PATH (Master.ShardKey) with AAD bound to that path.
// The point is compartmentalization by PLACEMENT, not by a self-declared name: an
// artifact in extensions/x/ can only ever decrypt extensions/x/'s shard, because the
// key is a function of where it sits — one claiming another's name gains nothing.

// ShardFile is the encrypted secret file inside an item's folder. Exported because the folder is a
// control plane other packages walk: what lists or serves an item's files has to know which name to
// refuse, and a second spelling of it would be a hole the day one of them changed.
const ShardFile = "secrets.enc"

// ShardPath is the encrypted secret file for one item's folder: <wsDir>/<relPath>/secrets.enc
// (relPath is workspace-relative, e.g. "extensions/gmail").
func ShardPath(wsDir, relPath string) string {
	return filepath.Join(wsDir, relPath, ShardFile)
}

// OpenShard opens (creating if absent) the secret shard for one extension folder: a Vault at
// ShardPath, keyed by the folder-path-derived key (Master.ShardKey) and AES-GCM AAD-bound to the
// relative path, so the shard is decryptable only from its own placement.
func OpenShard(m *Master, wsDir, wsName, relPath string) (*Vault, error) {
	return OpenVault(ShardPath(wsDir, relPath), m.ShardKey(wsName, relPath), WithAAD([]byte(relPath)))
}

// LoadShardsInto opens every <wsDir>/<dir>/<folder>/secrets.enc for each of dirs, decrypts it
// with the folder-path-derived key + path-bound AAD, and copies its secrets into dst
// (the workspace resolution store the injector reads). It is FAIL-CLOSED with NO
// fallback: a shard that will not open — wrong key, tamper, corruption — is skipped
// with a warning, so that item's credentials are simply absent; the workspace vault
// is NEVER read as a substitute, and a bad shard NEVER aborts the workspace. A folder
// whose name is not a valid identifier (valid==false) is not an addressable owner and
// is ignored; a folder with no secrets.enc simply has no credentials. dirs are the control-plane
// trees to walk — the caller names them, because which folders hold installable things is the
// composition root's knowledge and this package stays mechanism-only.
func LoadShardsInto(dst *Store, m *Master, wsDir, wsName string, dirs []string, valid func(string) bool, log *slog.Logger) {
	for _, kind := range dirs {
		root := filepath.Join(wsDir, kind)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // no such tree in this workspace
		}
		for _, e := range entries {
			if !e.IsDir() || !valid(e.Name()) {
				continue
			}
			relPath := kind + "/" + e.Name()
			if _, err := os.Stat(ShardPath(wsDir, relPath)); err != nil {
				continue // no shard → no credentials for this item
			}
			sv, err := OpenShard(m, wsDir, wsName, relPath)
			if err != nil {
				log.Warn("secret: shard skipped (fail-closed, no fallback)", "shard", relPath, "err", err)
				continue
			}
			sv.store.CopyInto(dst)
		}
	}
}
