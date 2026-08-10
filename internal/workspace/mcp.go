package workspace

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/mcp"
)

// mcpSetupTimeout bounds the startup handshake + tools/list for one MCP server.
const mcpSetupTimeout = 30 * time.Second

// MCPStatus is one declared MCP server and what became of it. A server that failed is kept, with
// the reason: the absence of a server you configured is exactly what you opened this view to find,
// and a list that silently omits it cannot tell you.
type MCPStatus struct {
	Name  string
	URL   string
	State MCPState
	Tools int    // tools it contributed; 0 unless connected
	Note  string // why it is not connected, in the words the log used
}

// MCPState is how far a server got.
type MCPState string

const (
	MCPConnected MCPState = "connected"
	// MCPNeedsAuth is not a failure but an errand: the server speaks OAuth and nobody has run
	// `nocturn auth <name>` yet.
	MCPNeedsAuth MCPState = "needs auth"
	MCPFailed    MCPState = "failed"
)

// installMCP connects the remote MCP servers declared in <dir>/mcp/*.json and folds each server's
// tools into the workspace toolset (as <server>_<tool>, refusing a name collision). Discovery
// (Connect + tools/list) runs on a bounded context with NO gate machinery installed — so the
// startup handshake never prompts; the runtime chat turn installs the gate, so a later
// model-invoked MCP call asks the human on the net axis exactly like http_read/http_write. A server
// that fails to load/connect/list is logged and skipped, never bricking the workspace (like a flaky
// plugin). Credentials are token (a bearer the operator seeded in the vault under mcp.SecretName)
// or public.
// It reports one MCPStatus per DECLARED server, connected or not: a server that was configured and
// did not come up is the single most useful thing this list can say, and a count cannot say it.
//
// The handshakes run CONCURRENTLY, and that is not an optimisation — it is what keeps one unreachable
// server from holding up everything else. Each is bounded by mcpSetupTimeout on its own, so the wall
// clock is max(timeouts) rather than their sum; sequentially, three declared servers behind a
// black-holing host cost a minute and a half before a workspace finished opening.
//
// Only the handshakes are concurrent. Folding the results into the toolset stays sequential, in the
// sorted declaration order Set.All gives, for two reasons that are really one: the toolset is a plain
// map, so concurrent writes would be a data race, and the collision rule is "first wins" — deciding
// it from goroutines would make WHICH server keeps a contested name depend on scheduling. Each
// goroutine writes exactly one element of results, which are distinct memory locations and need no
// lock. The two things they share are safe by construction: secret.Injector is mutex-guarded, and
// Scanner is documented as shareable across concurrent scans.
func (p pass) installMCP(toolset agentkit.ToolSet) []MCPStatus {
	log := p.log.With("component", "mcp")
	servers := mcp.Discover(filepath.Join(p.dir, "mcp"), p.diag).All()
	// An empty list, never nil: this goes out on the wire, where nil is JSON null and an empty
	// slice is [] — and a client rendering "no servers" should not have to handle both.
	if len(servers) == 0 {
		return []MCPStatus{}
	}

	results := make([]mcpResult, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Go(func() { results[i] = p.connectServer(srv, log) })
	}
	wg.Wait()

	out := make([]MCPStatus, 0, len(results))
	for _, r := range results {
		st := r.status
		if len(r.tools) > 0 {
			if clash := firstClash(toolset, r.tools); clash != "" {
				log.Warn("mcp tool name collides, skipping server", "server", st.Name, "tool", clash)
				st.State, st.Note = MCPFailed, "tool name "+clash+" already taken"
			} else {
				for _, t := range r.tools {
					toolset[t.Spec().Name] = t
				}
				log.Debug("mcp server connected", "server", st.Name, "tools", len(r.tools))
			}
		}
		out = append(out, st)
	}
	return out
}

// mcpResult is what one server's handshake produced: its status, and the tools to fold in if it got
// that far. The fold is the caller's, so this stage touches nothing shared.
type mcpResult struct {
	status MCPStatus
	tools  []agentkit.Tool
}

// connectServer runs one server's handshake to a verdict. It is pure with respect to the workspace:
// everything it learns comes back in the result.
func (p pass) connectServer(srv mcp.Server, log *slog.Logger) mcpResult {
	st := MCPStatus{Name: srv.Name, URL: srv.URL, State: MCPFailed}
	conn, err := mcp.NewConn(srv, p.injector, p.scanner)
	if err != nil {
		log.Warn("mcp server skipped (bad config)", "server", srv.Name, "err", err)
		st.Note = "bad config: " + err.Error()
		return mcpResult{status: st}
	}
	conn.SetLogger(log)
	ctx, cancel := context.WithTimeout(context.Background(), mcpSetupTimeout)
	defer cancel()
	tools, err := connectMCP(ctx, conn)
	if err != nil {
		var needAuth *mcp.AuthRequiredError
		if errors.As(err, &needAuth) {
			// The server wants OAuth and isn't authorized yet — not a failure, an action for the
			// operator. The daemon cannot open a browser; the flow runs from the CLI or the app.
			log.Info("mcp server needs authorization", "server", srv.Name, "action", "run: nocturn auth "+srv.Name)
			st.State, st.Note = MCPNeedsAuth, "run: nocturn auth "+srv.Name
		} else {
			log.Warn("mcp server skipped", "server", srv.Name, "err", err)
			st.Note = err.Error()
		}
		return mcpResult{status: st}
	}
	st.State, st.Tools = MCPConnected, len(tools)
	return mcpResult{status: st, tools: tools}
}

// firstClash names the first of tools whose name the toolset already holds, or "" if none does.
func firstClash(toolset agentkit.ToolSet, tools []agentkit.Tool) string {
	for _, t := range tools {
		if _, dup := toolset[t.Spec().Name]; dup {
			return t.Spec().Name
		}
	}
	return ""
}

// connectMCP performs one server's discovery (handshake + tools/list) on the setup ctx.
func connectMCP(ctx context.Context, conn *mcp.Conn) ([]agentkit.Tool, error) {
	if err := conn.Connect(ctx); err != nil {
		return nil, err
	}
	return conn.Tools(ctx)
}
