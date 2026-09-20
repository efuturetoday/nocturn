package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// cmdConfig shows or sets one installed extension's settings — the values its manifest declares and a
// human supplies. With no assignments it PRINTS what the extension asks for and what is set, because
// the first thing anybody needs is the list of names, and guessing them from a skill body is how a
// setting ends up misspelled in a file nothing reads back.
func cmdConfig(args []string) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	ws := workspaceFlag(fs)
	fs.Usage = func() { configUsage(os.Stderr) }
	pos, code, done := parseArgs(fs, args)
	if done {
		return code
	}
	if len(pos) == 0 {
		fs.Usage()
		return 2
	}
	if err := runConfig(*ws, pos[0], pos[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	return 0
}

func configUsage(w io.Writer) {
	io.WriteString(w, `usage: nocturn config <extension> [setting=value …] [-w workspace]

  With no assignments it prints the settings the extension declares and their current values.

  nocturn config home-assistant
  nocturn config home-assistant base_url=https://hass.example.com
`)
}

// runConfig reads or writes one extension's config.json. A write REPLACES the whole set of values
// (after merging with what is stored), so validation always runs against the complete configuration
// rather than against one field in isolation — a required value that is still missing is refused
// here, not discovered on the next turn.
func runConfig(wsName, target string, assignments []string) error {
	if !extension.ValidName(target) {
		return fmt.Errorf("name an extension by its folder, got %q", target)
	}
	wsDir := filepath.Join(wsRoot, wsName)
	decl, values, err := workspace.DeclOf(wsDir, target)
	if err != nil {
		return err
	}
	if len(assignments) == 0 {
		printConfig(os.Stdout, target, decl, values)
		return nil
	}
	if values == nil {
		values = extension.Values{}
	}
	for _, a := range assignments {
		name, value, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("%q is not a setting=value assignment", a)
		}
		values[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	if err := workspace.SetConfig(wsDir, target, values); err != nil {
		return err
	}
	fmt.Printf("configured %s in workspace %q\n", target, wsName)
	printConfig(os.Stdout, target, decl, values)
	return nil
}

// printConfig renders what an extension asks for and what it has — settings with their values, then
// the credentials, by name only. A credential's VALUE is never printed and never read here: this
// command talks to the same file a person edits, and the values live in the encrypted shard.
func printConfig(w io.Writer, name string, d extension.Decl, v extension.Values) {
	if len(d.Config) == 0 && len(d.Credentials) == 0 {
		fmt.Fprintf(w, "%s needs no settings.\n", name)
		return
	}
	for _, c := range d.Config {
		value := v[c.Name]
		if value == "" {
			value = "(not set)"
			if c.Optional {
				value = "(not set, optional)"
			}
		}
		fmt.Fprintf(w, "  %-16s %s\n", c.Name, value)
		if c.Label != "" {
			fmt.Fprintf(w, "  %-16s %s\n", "", c.Label)
		}
	}
	for _, c := range d.Credentials {
		fmt.Fprintf(w, "  %-16s seed with: nocturn secret set %s/%s\n", c.Name, name, c.Name)
	}
}
