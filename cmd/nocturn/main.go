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
	if err := tuiCmd(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
