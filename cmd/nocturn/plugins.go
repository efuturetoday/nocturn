package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// loadPlugins installs every plugin under ./plugins/<name>/ into the shared
// registry + injector, reviewing each one's ceiling before it runs. It is a no-op
// if the plugins dir is absent. Run BEFORE bubbletea grabs the terminal — the
// review prompt reads stdin.
//
// A plugin declaring a scary ceiling is shown verbatim and installed only on an
// explicit "y". (Follow-up: a manifest-hash "already approved" record so an
// unchanged plugin needs no re-prompt on every boot.)
func loadPlugins(reg *brain.Registry, inj *secret.Injector) error {
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
	}
	return nil
}

// reviewPlugin shows the plugin's ceiling (what it may attempt) + the credentials
// it uses, and asks the operator to confirm the install. This is the ONE trust
// decision; per-effect asks still happen at runtime.
func reviewPlugin(m plugin.Manifest) (bool, error) {
	fmt.Printf("\nInstall plugin %q v%s — it may attempt:\n", m.Name, m.Version)
	for _, r := range m.Requires {
		fmt.Printf("    %-12s %s\n", r.Capability, r.Host)
	}
	for _, c := range m.Credentials {
		fmt.Printf("    credential   %s → %s\n", c.Name, c.Host)
	}
	fmt.Print("Install? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}
