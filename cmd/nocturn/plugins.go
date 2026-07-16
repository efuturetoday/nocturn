package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/approval"
	"github.com/efuturetoday/nocturn/internal/oauth"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// loadPlugins installs every plugin under <ws>/plugins/<name>/ into the shared
// registry + injector, reviewing each one's cage before it runs, then running
// any OAuth flows it declares. It is a no-op if the plugins dir is absent. Run
// BEFORE bubbletea grabs the terminal — the review prompt AND the OAuth consent
// URL both use stdin/stdout.
//
// A plugin declaring a scary cage is shown verbatim and installed only on an
// explicit "y" — but an UNCHANGED plugin (same manifest + artifact as last
// approved) installs silently via the approved-record; a changed one re-prompts
// with a diff, so a manifest change is the signal instead of noise.
func loadPlugins(ctx context.Context, reg *tool.Registry, inj *secret.Injector, vault *secret.Vault, approvals *approval.Store, wsDir string) error {
	pluginsDir := filepath.Join(wsDir, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil // no plugins dir → nothing to install
	}
	host := plugin.NewHost(reg, inj)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(pluginsDir, e.Name())
		l, err := plugin.Load(dir)
		if err != nil {
			return fmt.Errorf("plugin %s: %w", e.Name(), err)
		}
		content, err := pluginApprovalContent(l)
		if err != nil {
			return fmt.Errorf("plugin %s: %w", l.Manifest.Name, err)
		}
		if err := host.Install(l, approvePlugin(approvals, content)); err != nil {
			return fmt.Errorf("plugin %s: %w", l.Manifest.Name, err)
		}
		// The plugin declares its own OAuth providers; the host runs the flow and
		// injects the token — the plugin never sees it (ADR-5). Runs after a
		// successful install (its credential bindings are now in place).
		if err := wirePluginOAuth(ctx, inj, vault, l.Manifest); err != nil {
			return err
		}
	}
	return nil
}

// pluginApprovalContent is the canonical declaration whose change triggers a
// re-review: the manifest (the authority the operator judges) plus the artifact's
// hash (integrity — the binary itself cannot be meaningfully diffed).
func pluginApprovalContent(l plugin.Loaded) ([]byte, error) {
	sum := sha256.Sum256(l.Artifact)
	return json.Marshal(struct {
		Manifest plugin.Manifest `json:"manifest"`
		Artifact string          `json:"artifact_sha256"`
	}{l.Manifest, hex.EncodeToString(sum[:])})
}

// approvePlugin is the install approve-callback gated by the approved-record: an
// unchanged plugin installs silently; a new or changed one is reviewed (with a
// diff of what changed) and, on "y", recorded for next time.
func approvePlugin(approvals *approval.Store, content []byte) func(plugin.Manifest) (bool, error) {
	return func(m plugin.Manifest) (bool, error) {
		ok, prior := approvals.Status("plugin", m.Name, content)
		if ok {
			return true, nil // unchanged since last approved — no prompt
		}
		if prior != nil {
			fmt.Printf("\n⚠  plugin %q changed since you last approved it:\n", m.Name)
			printApprovalDiff(prior, content)
		}
		yes, err := reviewPlugin(m)
		if err != nil || !yes {
			return false, err
		}
		return true, approvals.Approve("plugin", m.Name, content)
	}
}

// printApprovalDiff shows a crude line diff of two canonical JSON declarations —
// enough to surface WHAT changed (a repointed host, a widened cage, a bumped
// version) without a full diff library.
func printApprovalDiff(prior, current []byte) {
	oldL, newL := indentLines(prior), indentLines(current)
	inNew, inOld := map[string]bool{}, map[string]bool{}
	for _, l := range newL {
		inNew[l] = true
	}
	for _, l := range oldL {
		inOld[l] = true
	}
	for _, l := range oldL {
		if !inNew[l] {
			fmt.Printf("    - %s\n", strings.TrimSpace(l))
		}
	}
	for _, l := range newL {
		if !inOld[l] {
			fmt.Printf("    + %s\n", strings.TrimSpace(l))
		}
	}
}

func indentLines(raw []byte) []string {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		return strings.Split(string(raw), "\n")
	}
	return strings.Split(buf.String(), "\n")
}

// wirePluginOAuth runs each OAuth provider a plugin declares: build a config
// from its manifest endpoints, load-or-authorize a token (kept in the encrypted
// vault, keyed per plugin+name), and register a refreshing Bearer source under
// the credential's name — so the injector stamps it, host-side, for the
// plugin's declared destination. The guest never sees the token.
func wirePluginOAuth(ctx context.Context, inj *secret.Injector, vault *secret.Vault, m plugin.Manifest) error {
	if inj == nil {
		return nil
	}
	// The token is keyed by the credential's host (plugin.SecretName), so look up
	// the host of the CredentialDecl each OAuth block feeds (Validate guarantees a
	// match). A manifest that repoints the credential to another host thus yields a
	// different key → re-authorization, never a silent cross-host token reuse.
	credHost := map[string]string{}
	for _, c := range m.Credentials {
		credHost[c.Name] = c.Host
	}
	for _, o := range m.OAuth {
		cfg := oauth.Provider(o.AuthURL, o.TokenURL, o.ClientID, o.ClientSecret, o.Scopes...)
		// Same host-bound key the install binding resolves (plugin.SecretName), so
		// only THIS plugin's binding for THIS host can reach this source.
		name := plugin.SecretName(plugin.Owner(m.Name), o.Name, credHost[o.Name])
		tok, ok := vaultToken(vault, name)
		if !ok {
			fmt.Printf("\nPlugin %q needs to authorize %q (scopes: %s)\n", m.Name, o.Name, strings.Join(o.Scopes, " "))
			var err error
			if tok, err = oauth.Authorize(ctx, cfg, nil); err != nil { // nil prompt = print the URL
				return fmt.Errorf("plugin %s: authorize %s: %w", m.Name, o.Name, err)
			}
			if err := saveVaultToken(vault, name, tok); err != nil {
				return fmt.Errorf("plugin %s: persist %s token: %w", m.Name, o.Name, err)
			}
		}
		inj.SetResolver(name, oauth.NewCredential(cfg, tok, persistToken(vault, name)))
	}
	return nil
}

// reviewPlugin shows the plugin's cage (what it may attempt) + the credentials
// it uses, and asks the operator to confirm the install. This is the ONE trust
// decision; per-effect asks still happen at runtime.
func reviewPlugin(m plugin.Manifest) (bool, error) {
	fmt.Printf("\nInstall plugin %q v%s — it may attempt:\n", m.Name, m.Version)
	for _, r := range m.Requires {
		access := "read"
		if r.Mutates {
			access = "write"
		}
		fmt.Printf("    %-5s %-5s %s\n", r.Family, access, r.Target)
	}
	for _, td := range m.Tools {
		mark := ""
		if td.Consequential {
			mark = " [consequential — always asks, never remembered]"
		}
		if td.Intent != "" {
			fmt.Printf("    tool %-8s asks: %q%s\n", td.Name, td.Intent, mark)
		} else if mark != "" {
			fmt.Printf("    tool %-8s%s\n", td.Name, mark)
		}
	}
	for _, c := range m.Credentials {
		fmt.Printf("    credential   %s → %s\n", c.Name, c.Host)
	}
	for _, o := range m.OAuth {
		fmt.Printf("    oauth        %s @ %s (scopes: %s)\n", o.Name, o.AuthURL, strings.Join(o.Scopes, " "))
	}
	fmt.Print("Install? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}
