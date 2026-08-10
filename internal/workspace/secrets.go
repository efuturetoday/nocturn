package workspace

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// This file assembles a workspace's secret stack. Each workspace opens its OWN encrypted vault —
// <dir>/vault.enc, keyed by the master's workspace-domain-separated sub-key — so a credential
// authorized in one workspace is encrypted under a different key, in a different file, on a
// different injector than any other's. The single master (one passphrase) is the root of every
// workspace key; nothing here holds the passphrase.

const vaultFile = "vault.enc"

// workspaceSecrets is the durable half of a workspace's credential stack: one vault on one file, and
// the resolution store the injector and scanner both read. All four are nil when the vault is locked.
type workspaceSecrets struct {
	master     *secret.Master
	vault      *secret.Vault
	resolution *secret.Store
	injector   *secret.Injector
	scanner    *secret.Scanner
}

// buildWorkspaceSecrets opens this workspace's vault and assembles its injector + scanner, seeding
// env secrets and per-workspace bindings. A nil master (vault locked, no passphrase) yields a zero
// workspaceSecrets: the workspace runs without host-owned credentials or leak scanning. The vault is
// held for its lifetime — an OAuth refresh persists the new token back through it.
//
// What it deliberately does NOT do is anything that depends on DISCOVERY. Reading the plugin/MCP
// shards and registering their OAuth token sources used to happen here, once, and that was invisible
// until a server could be added while the daemon ran: its shard was never read and its token never
// got a resolver, so authorizing it from the phone left it failing 401 until a restart. Both moved
// into reconcileSecrets, which every discovery pass runs.
func buildWorkspaceSecrets(master *secret.Master, dir, name string, log *slog.Logger) (workspaceSecrets, error) {
	log = log.With("component", "secret")
	if master == nil {
		log.Info("vault locked (no master passphrase) — running without host-owned credentials")
		return workspaceSecrets{}, nil
	}
	vault, err := secret.OpenVault(filepath.Join(dir, vaultFile), master.WorkspaceKey(name))
	if err != nil {
		return workspaceSecrets{}, err
	}
	seedEnvSecrets(vault, log)
	// The injector + scanner resolve over a UNION resolution store: the workspace vault's own
	// secrets PLUS every plugin/mcp shard's (each secrets.enc decrypted with its folder-path key).
	// This store lives only in memory and is NEVER persisted, so a write to the workspace vault (an
	// OAuth refresh) can never leak a shard secret into vault.enc — compartmentalization holds on
	// disk. Shards fail closed: a bad one is absent, not a fallback to the workspace vault.
	res := secret.NewStore()
	vault.Store().CopyInto(res)
	injector := secret.NewInjector(res)
	scanner := secret.NewScanner(res)
	// Trace injection + leak-scan security events (names/rule-ids only, never a secret value) under
	// this workspace's component=secret logger.
	injector.SetLogger(log)
	scanner.SetLogger(log)
	loadBindings(injector, filepath.Join(dir, "bindings.json"), log)
	log.Info("secret: workspace vault unlocked", "ws", name)
	return workspaceSecrets{master: master, vault: vault, resolution: res, injector: injector, scanner: scanner}, nil
}

// reconcile brings the credential stack in line with what is on disk RIGHT NOW: every
// plugin/MCP shard's secrets into the resolution store, and a refreshing OAuth source for every
// provider that has a stored token. A discovery pass calls it before connecting anything.
//
// Both halves are idempotent by construction, which is what lets it run on every reload rather than
// only at startup: LoadShardsInto copies by name into the store, and SetResolver replaces by name on
// the injector. Plugin bindings are the exception — AddBinding appends — so installPlugins clears
// each owner's bindings before adding them, and that is where it belongs, next to the discovery that
// decides which owners still exist.
func (s workspaceSecrets) reconcile(dir, name string, log *slog.Logger) {
	if s.master == nil || s.resolution == nil {
		return // locked vault: nothing to resolve over, and nothing to leak
	}
	log = log.With("component", "secret")
	secret.LoadShardsInto(s.resolution, s.master, dir, name, discovery.ValidName, log)
	// OAuth tokens live in each plugin/mcp folder's shard (path-encrypted), not the workspace vault —
	// registerOAuth reads and refreshes them through the shard router, keyed by the credential's name.
	registerOAuth(s.injector, NewShardTokens(s.master, dir, name, log), dir, log)
}

// seedEnvSecrets stores each NOCTURN_SECRET_<NAME>=value into the vault under <name> (lowercased) —
// the input channel for credential values until an interactive add-secret UX exists. The same env is
// seeded into every workspace's vault (a shared input), but each copy lives in an isolated vault.
func seedEnvSecrets(vault *secret.Vault, log *slog.Logger) {
	const prefix = "NOCTURN_SECRET_"
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, prefix) || v == "" {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(k, prefix))
		if err := vault.Set(name, []byte(v)); err != nil {
			log.Warn("secret: seed", "name", name, "err", err)
		}
	}
}

// loadBindings reads a workspace's bindings.json (a list of host-owned credential bindings) and
// registers each at the workspace level (owner ""), so the model's own network calls inject them.
// Absent file = none.
func loadBindings(inj *secret.Injector, path string, log *slog.Logger) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // no bindings configured
	}
	var raw []struct {
		Secret string `json:"secret"`
		Host   string `json:"host"`
		Header string `json:"header"`
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Warn("secret: bindings.json", "err", err)
		return
	}
	for _, b := range raw {
		inj.AddBinding("", secret.Binding{Secret: b.Secret, Host: b.Host, Header: b.Header, Prefix: b.Prefix})
	}
	log.Info("secret: bindings loaded", "count", len(raw))
}
