package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// version is overridden at build time: go build -ldflags "-X main.version=$(git describe --tags)".
var version = "dev"

// dispatch routes the CLI and returns a Unix exit code (0 ok, 1 runtime error, 2 usage error). main()
// is the only caller of os.Exit, so every command returns instead of exiting — deferred cleanup runs
// and the router stays testable. A bare invocation (no subcommand) opens the interactive TUI.
func dispatch(args []string) int {
	if len(args) == 0 {
		return runApp("")
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		addr := fs.String("addr", envOr("NOCTURN_ADDR", ":8080"), "listen address for the WebSocket daemon")
		fs.Usage = usage(fs, "serve [--addr :8080]", "Run the out-of-band WebSocket daemon instead of the terminal UI.")
		if code, done := parseFlags(fs, args[1:]); done {
			return code
		}
		return runApp(*addr)
	case "voice":
		fs := flag.NewFlagSet("voice", flag.ContinueOnError)
		port := fs.Int("port", 8788, "loopback port for the voice PoC harness")
		wsName := fs.String("w", workspace.DefaultWorkspace, "workspace")
		fs.StringVar(wsName, "workspace", workspace.DefaultWorkspace, "workspace")
		fs.Usage = usage(fs, "voice [--port 8788] [-w workspace]",
			"Run the browser voice PoC harness (loopback only, no pairing).")
		if code, done := parseFlags(fs, args[1:]); done {
			return code
		}
		return cmdVoice(*port, *wsName)
	case "auth":
		return cmdAuth(args[1:])
	case "secret":
		return cmdSecret(args[1:])
	case "ls":
		return cmdLs(args[1:])
	case "version", "--version", "-v":
		fmt.Println("nocturn", version)
		return 0
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "nocturn: unknown command %q\n\n", args[0])
		printUsage(os.Stderr)
		return 2
	}
}

// printUsage is the top-level help. Data goes to the passed writer; the caller picks stdout (help) or
// stderr (an error).
func printUsage(w io.Writer) {
	fmt.Fprint(w, `nocturn — a secure personal AI assistant

Usage:
  nocturn                      Open the interactive terminal assistant (default workspace)
  nocturn serve [--addr :8080] Run the out-of-band WebSocket daemon
  nocturn voice [--port 8788]  Run the browser voice PoC harness (loopback only, no pairing)
  nocturn auth <provider>      Connect an OAuth account (opens a browser)
  nocturn secret set <target>  Seed a static credential (value read from stdin)
  nocturn secret ls            List the credential names a workspace holds (never values)
  nocturn ls                   List workspaces, or one workspace's plugins/mcp/agents/skills
  nocturn version              Print the version
  nocturn help                 Show this help

Most commands take -w/--workspace (default: `+workspace.DefaultWorkspace+`).
A credential target is owner-namespaced: plugin:<name>/<credential> or mcp:<name>.
`)
}

// usage builds a per-command Usage that prints a one-line synopsis + the flag defaults to stderr.
func usage(fs *flag.FlagSet, synopsis, desc string) func() {
	return func() {
		fmt.Fprintf(os.Stderr, "usage: nocturn %s\n\n%s\n\n", synopsis, desc)
		fs.PrintDefaults()
	}
}

// parseFlags parses args; done is true when the command should stop now (help printed → code 0, or a
// bad flag → code 2). The FlagSet must be ContinueOnError so a parse error returns instead of exiting.
func parseFlags(fs *flag.FlagSet, args []string) (code int, done bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	return 0, false
}

// parseArgs parses flags that may appear BEFORE or AFTER the positional arguments. Stdlib flag stops
// at the first non-flag token, so a natural `secret set plugin:x/y -w main` would drop the flag; here
// we resume parsing after each positional and collect them. Returns the positionals.
func parseArgs(fs *flag.FlagSet, args []string) (pos []string, code int, done bool) {
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, true
			}
			return nil, 2, true
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, 0, false
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// workspaceFlag registers -w and --workspace (same target) on fs, defaulting to the default workspace.
func workspaceFlag(fs *flag.FlagSet) *string {
	ws := new(string)
	fs.StringVar(ws, "workspace", workspace.DefaultWorkspace, "the workspace to act on")
	fs.StringVar(ws, "w", workspace.DefaultWorkspace, "the workspace to act on (shorthand)")
	return ws
}

func cmdAuth(args []string) int {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	ws := workspaceFlag(fs)
	scope := fs.String("scope", "", "space- or comma-separated OAuth scopes to request (discover mode)")
	fs.Usage = usage(fs, "auth <provider> [-w workspace] [-scope \"a b\"]", "Connect an OAuth account (opens a browser). <provider> is a plugin or MCP server name.")
	pos, code, done := parseArgs(fs, args)
	if done {
		return code
	}
	if len(pos) != 1 {
		fs.Usage()
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runAuth(ctx, pos[0], *ws, splitScopes(*scope)); err != nil {
		fmt.Fprintln(os.Stderr, "auth:", err)
		return 1
	}
	return 0
}

// splitScopes parses a scope flag that may be space- or comma-separated into a clean list.
func splitScopes(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func cmdSecret(args []string) int {
	if len(args) == 0 {
		secretUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "set":
		return cmdSecretSet(args[1:])
	case "ls":
		return cmdSecretLs(args[1:])
	case "help", "-h", "--help":
		secretUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "nocturn secret: unknown subcommand %q\n\n", args[0])
		secretUsage(os.Stderr)
		return 2
	}
}

func secretUsage(w io.Writer) {
	io.WriteString(w, `usage: nocturn secret set <target> [-w workspace]   (value on stdin)
       nocturn secret ls          [-w workspace]

  target is owner-namespaced:
    plugin:<name>/<credential>   a plugin credential (from its manifest)
    mcp:<name>                   an MCP server's bearer (host-bound; host from its mcp.json)

The value is read from stdin, so it never enters your shell history or the process list:
  printf %s "$TOKEN" | nocturn secret set plugin:gmail/acct -w main
`)
}

func cmdSecretSet(args []string) int {
	fs := flag.NewFlagSet("secret set", flag.ContinueOnError)
	ws := workspaceFlag(fs)
	fs.Usage = func() { secretUsage(os.Stderr) }
	pos, code, done := parseArgs(fs, args)
	if done {
		return code
	}
	if len(pos) != 1 {
		fs.Usage()
		return 2
	}
	if err := runSecretSet(*ws, pos[0]); err != nil {
		fmt.Fprintln(os.Stderr, "secret set:", err)
		return 1
	}
	return 0
}

func cmdSecretLs(args []string) int {
	fs := flag.NewFlagSet("secret ls", flag.ContinueOnError)
	ws := workspaceFlag(fs)
	fs.Usage = usage(fs, "secret ls [-w workspace]", "List the credential names this workspace holds (names only, never values).")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	names, err := listSecretNames(*ws)
	if err != nil {
		fmt.Fprintln(os.Stderr, "secret ls:", err)
		return 1
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return 0
}

// listSecretNames returns every credential name resolvable in a workspace — the workspace vault plus
// each plugin/mcp shard — names ONLY (Store.Names never returns a value).
func listSecretNames(wsName string) ([]string, error) {
	master, err := openMaster()
	if err != nil {
		return nil, err
	}
	if master == nil {
		return nil, errors.New("set NOCTURN_MASTER_PASSPHRASE to read the vault")
	}
	wsDir := filepath.Join(wsRoot, wsName)
	vault, err := secret.OpenVault(filepath.Join(wsDir, "vault.enc"), master.WorkspaceKey(wsName))
	if err != nil {
		return nil, err
	}
	res := secret.NewStore()
	vault.Store().CopyInto(res)
	secret.LoadShardsInto(res, master, wsDir, wsName, discovery.ValidName, discardLog())
	// Hide the ".provider" sidecar records — they are resolved-OAuth wiring (endpoints, client id,
	// resource, scopes), not credentials the operator seeds or reasons about.
	var creds []string
	for _, n := range res.Names() {
		if !strings.HasSuffix(n, ".provider") {
			creds = append(creds, n)
		}
	}
	return creds, nil
}

func cmdLs(args []string) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	var ws string
	fs.StringVar(&ws, "workspace", "", "list one workspace's plugins/mcp/agents/skills")
	fs.StringVar(&ws, "w", "", "shorthand for --workspace")
	fs.Usage = usage(fs, "ls [-w workspace]", "With no -w, list workspaces. With -w, list that workspace's plugins, MCP servers, agents, and skills.")
	if code, done := parseFlags(fs, args); done {
		return code
	}

	if ws == "" {
		entries, err := os.ReadDir(wsRoot)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ls:", err)
			return 1
		}
		for _, e := range entries {
			if e.IsDir() {
				fmt.Println(e.Name())
			}
		}
		return 0
	}

	wsDir := filepath.Join(wsRoot, ws)
	var diag agentkit.Diagnostics
	base, _ := agentkit.NewToolSet()

	var plugins []string
	for _, p := range plugin.Discover(filepath.Join(wsDir, "plugins"), base, &diag).All() {
		plugins = append(plugins, p.Name())
	}
	var servers []string
	for _, s := range mcp.Discover(filepath.Join(wsDir, "mcp"), &diag).All() {
		servers = append(servers, s.Name)
	}
	var agents []string
	for _, a := range agent.Discover(filepath.Join(wsDir, "agents"), &diag).All() {
		agents = append(agents, a.Name)
	}
	skills, _ := skill.Discover(filepath.Join(wsDir, "skills"), &diag)
	printGroup("plugins", plugins)
	printGroup("mcp", servers)
	printGroup("agents", agents)
	printGroup("skills", skillNames(skills))
	// Anything skipped during discovery goes to stderr, so `ls` never silently hides a broken folder
	// while its clean listing stays pipeable on stdout.
	for _, d := range diag.All() {
		fmt.Fprintf(os.Stderr, "skipped %s: %s\n", d.Subject, d.Message)
	}
	return 0
}

// printGroup prints a labeled list, or "(none)" when empty.
func printGroup(label string, items []string) {
	if len(items) == 0 {
		fmt.Printf("%s: (none)\n", label)
		return
	}
	fmt.Printf("%s: %s\n", label, join(items))
}

// skillNames returns a skill set's names, sorted.
func skillNames(s agentkit.SkillSet) []string {
	out := make([]string, 0, len(s))
	for name := range s {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func join(items []string) string { return strings.Join(items, ", ") }

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
