package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// `nocturn reload` — tell a running daemon to look at the folders again.
//
// A workspace's agents, skills, plugins and MCP servers are discovered when it opens, and there is no
// watcher on those directories. That is a decision rather than an omission: a ticker would re-run
// every MCP handshake against other people's servers on a schedule, and a filesystem watcher would
// mean a dependency, recursive watch management for every subdirectory that appears, and events that
// get dropped under load — after which a periodic reconcile is needed anyway (internal/knowledge
// made the same call, and says so).
//
// So it is asked for. The app asks over its socket; this is how the person who just copied a folder
// in asks, without restarting a daemon that may be holding live conversations.
//
// Authority is the same as `nocturn pair`: the 0600 credential the daemon wrote beside the vault.
// Being allowed to reload a workspace is being allowed to read its directory — which is the same
// authority that could edit the folders this command exists to notice.

// reloadResult is what the daemon reports back: the workspace as it now stands.
type reloadResult struct {
	Ws      string   `json:"ws"`
	Agents  int      `json:"agents"`
	Skills  []string `json:"skills"`
	Plugins []string `json:"plugins"`
	Tools   int      `json:"tools"`
	MCP     []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Tools int    `json:"tools"`
		Note  string `json:"note"`
	} `json:"mcp"`
}

// cmdReload re-runs a workspace's discovery on the running daemon and prints what it found.
func cmdReload(addr, ws string) int {
	bearer, err := cliBearer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reload:", err)
		return 1
	}

	body, err := json.Marshal(map[string]string{"ws": ws})
	if err != nil {
		fmt.Fprintln(os.Stderr, "reload:", err)
		return 1
	}
	req, err := http.NewRequest(http.MethodPost, httpBase(addr)+"/reload", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "reload:", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")

	// Generous, because the daemon answers only once every MCP server has been tried and each of those
	// is allowed thirty seconds. Waiting is the right behaviour here: somebody typed this and wants to
	// know what the workspace holds now, not that the request was accepted.
	res, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reload: cannot reach the daemon at %s: %v\n", addr, err)
		fmt.Fprintln(os.Stderr, "reload: is it running? `nocturn serve`")
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "reload: the daemon refused (%s) %s\n", res.Status, errorBody(res))
		return 1
	}

	var out reloadResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		fmt.Fprintln(os.Stderr, "reload: unreadable answer:", err)
		return 1
	}
	printReload(out)
	return 0
}

// printReload says what the workspace holds now — the point of having asked.
func printReload(r reloadResult) {
	fmt.Printf("%s reloaded: %d agents, %d skills, %d plugins, %d tools\n",
		r.Ws, r.Agents, len(r.Skills), len(r.Plugins), r.Tools)
	if len(r.Skills) > 0 {
		fmt.Println("  skills:  " + strings.Join(r.Skills, ", "))
	}
	if len(r.Plugins) > 0 {
		fmt.Println("  plugins: " + strings.Join(r.Plugins, ", "))
	}
	// Every declared server, connected or not. A server that was configured and did not come up is the
	// single most useful thing this output can say, and a count cannot say it.
	for _, s := range r.MCP {
		line := fmt.Sprintf("  mcp %s: %s", s.Name, s.State)
		if s.Tools > 0 {
			line += fmt.Sprintf(" (%d tools)", s.Tools)
		}
		if s.Note != "" {
			line += " — " + s.Note
		}
		fmt.Println(line)
	}
}
