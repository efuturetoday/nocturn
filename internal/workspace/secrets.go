package workspace

import (
	"log/slog"
	"path/filepath"

	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/mail"
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
	log.Info("secret: workspace vault unlocked", "ws", name)
	return workspaceSecrets{master: master, vault: vault, resolution: res, injector: injector, scanner: scanner}, nil
}

// reconcile brings the credential stack in line with what is on disk RIGHT NOW: every
// plugin/MCP shard's secrets into the resolution store, and a refreshing OAuth source for every
// provider that has a stored token. A discovery pass calls it before connecting anything.
//
// Both halves are idempotent by construction, which is what lets it run on every reload rather than
// only at startup: LoadShardsInto copies by name into the store, and SetResolver replaces by name on
// the injector. Bindings are not reconciled here at all — bindExtensions swaps the whole owned set in
// one call, next to the discovery that decides which owners still exist.
func (s workspaceSecrets) reconcile(dir, name string, log *slog.Logger) {
	if s.master == nil || s.resolution == nil {
		return // locked vault: nothing to resolve over, and nothing to leak
	}
	log = log.With("component", "secret")
	// Rebuilt, not accumulated. The store is what the injector resolves through, so a name that is no
	// longer on disk has to disappear from it — otherwise `nocturn secret rm` deletes a value from its
	// shard, reports success, and the daemon goes on injecting its copy until the process ends. Built
	// aside and swapped in one step, so no request ever sees a half-loaded set.
	next := secret.NewStore()
	s.vault.Store().CopyInto(next)
	secret.LoadShardsInto(next, s.master, dir, name, ExtensionDirs(), extension.ValidName, log)
	// The mailbox is not an extension and its shard is not in that tree, but its passwords still have
	// to be in the resolution store — that is what the leak SCANNER reads. Without this the mail
	// password would be the one credential the host holds that nothing would block on its way out.
	if sv, err := secret.OpenShard(s.master, dir, name, mail.Dir); err == nil {
		sv.Store().CopyInto(next)
	}
	s.resolution.Reset(next)
	// OAuth tokens live in each plugin/mcp folder's shard (path-encrypted), not the workspace vault —
	// registerOAuth reads and refreshes them through the shard router, keyed by the credential's name.
	registerOAuth(s.injector, NewShardTokens(s.master, dir, name, log), dir, log)
}
