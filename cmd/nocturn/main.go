// Command nocturn launches the interactive TUI chat. Every effect runs through
// Nocturn's security path — capability-brokered, with human approval for
// sensitive actions. The TUI is the whole interface; the only argument is an
// optional workspace name (`nocturn [workspace]`, default "default") — the
// isolated context (own vault, grants, agents, plugins) to run.
package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	run := tuiCmd
	if len(args) > 0 && args[0] == "serve" {
		run, args = serveCmd, args[1:] // `nocturn serve` = the companion-app daemon (one binary, a mode)
	}
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
