package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/oauth"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// loadPlugins installs every plugin under ./plugins/<name>/ into the shared
// registry + injector, reviewing each one's ceiling before it runs, then running
// any OAuth flows it declares. It is a no-op if the plugins dir is absent. Run
// BEFORE bubbletea grabs the terminal — the review prompt AND the OAuth consent
// URL both use stdin/stdout.
//
// A plugin declaring a scary ceiling is shown verbatim and installed only on an
// explicit "y". (Follow-up: a manifest-hash "already approved" record so an
// unchanged plugin needs no re-prompt on every boot.)
func loadPlugins(ctx context.Context, reg *tool.Registry, inj *secret.Injector) error {
	entries, err := os.ReadDir("plugins")
	if err != nil {
		return nil // no plugins dir → nothing to install
	}
	host := plugin.NewHost(reg, inj)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("plugins", e.Name())
		l, err := plugin.Load(dir)
		if err != nil {
			return fmt.Errorf("plugin %s: %w", e.Name(), err)
		}
		if err := host.Install(l, reviewPlugin); err != nil {
			return fmt.Errorf("plugin %s: %w", l.Manifest.Name, err)
		}
		// The plugin declares its own OAuth providers; the host runs the flow and
		// injects the token — the plugin never sees it (ADR-5). Runs after a
		// successful install (its credential bindings are now in place).
		if err := wirePluginOAuth(ctx, inj, l.Manifest); err != nil {
			return err
		}
	}
	return nil
}

// wirePluginOAuth runs each OAuth provider a plugin declares: build a config from
// its manifest endpoints, load-or-authorize a token (persisted per plugin+name),
// and register a refreshing Bearer source under the credential's name — so the
// injector stamps it, host-side, for the plugin's declared destination. The guest
// never sees the token.
func wirePluginOAuth(ctx context.Context, inj *secret.Injector, m plugin.Manifest) error {
	if inj == nil {
		return nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	for _, o := range m.OAuth {
		cfg := oauth.Provider(o.AuthURL, o.TokenURL, o.ClientID, o.ClientSecret, o.Scopes...)
		path := filepath.Join(dir, "nocturn", "oauth", m.Name+"-"+o.Name+".json")
		tok, ok := loadTokenAt(path)
		if !ok {
			fmt.Printf("\nPlugin %q needs to authorize %q (scopes: %s)\n", m.Name, o.Name, strings.Join(o.Scopes, " "))
			if tok, err = oauth.Authorize(ctx, cfg, nil); err != nil { // nil prompt = print the URL
				return fmt.Errorf("plugin %s: authorize %s: %w", m.Name, o.Name, err)
			}
			if err := saveTokenAt(path, tok); err != nil {
				return fmt.Errorf("plugin %s: persist %s token: %w", m.Name, o.Name, err)
			}
		}
		p := path
		inj.SetSource(o.Name, oauth.NewSource(cfg, tok, func(t *oauth2.Token) { _ = saveTokenAt(p, t) }))
	}
	return nil
}

// reviewPlugin shows the plugin's ceiling (what it may attempt) + the credentials
// it uses, and asks the operator to confirm the install. This is the ONE trust
// decision; per-effect asks still happen at runtime.
func reviewPlugin(m plugin.Manifest) (bool, error) {
	fmt.Printf("\nInstall plugin %q v%s — it may attempt:\n", m.Name, m.Version)
	for _, r := range m.Requires {
		fmt.Printf("    %-12s %s\n", r.Capability, r.Target)
	}
	for _, td := range m.Tools {
		if td.Intent != "" {
			fmt.Printf("    tool %-8s asks: %q\n", td.Name, td.Intent)
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
