package serve

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// openUnlockedWorkspace builds a workspace with a master key and one discover-mode MCP server, so its
// Accounts() orchestrator is live and List() has something to report.
func openUnlockedWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	srvDir := filepath.Join(dir, "mcp", "acme")
	if err := os.MkdirAll(srvDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srvDir, "mcp.json"), []byte(`{"url":"https://acme.example/mcp","auth":"oauth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	master, err := secret.DeriveMaster("pw", []byte("salt-16-bytes-!!"), secret.WithWorkFactor(10))
	if err != nil {
		t.Fatal(err)
	}
	h := workspace.Host{LLM: fakeLLM{}, Master: master, Log: slog.New(slog.DiscardHandler)}
	ws, err := workspace.Open(h, "main", dir)
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	return ws
}

// auth.list reports a workspace's connectable discover-mode servers and their status through the
// live Accounts() orchestrator.
func TestAuth_List_ReportsConnectableAccounts(t *testing.T) {
	c := connWith(openUnlockedWorkspace(t))
	c.auth(context.Background(), "auth.list", []byte(`{"cmd":"auth.list","ws":"main"}`))

	msg := recv(t, c)
	accounts, ok := msg.(AuthAccounts)
	if !ok {
		t.Fatalf("message = %T, want AuthAccounts", msg)
	}
	if len(accounts.Accounts) != 1 || accounts.Accounts[0].Server != "acme" || accounts.Accounts[0].Connected {
		t.Fatalf("accounts = %+v, want one unconnected \"acme\"", accounts.Accounts)
	}
}

// A locked vault has no orchestrator, so an auth command fails closed rather than silently doing
// nothing — the operator learns the daemon must be unlocked.
func TestAuth_VaultLocked_FailsClosed(t *testing.T) {
	ws := openWorkspace(t) // Host without a Master
	c := connWith(ws)
	c.auth(context.Background(), "auth.begin", []byte(`{"cmd":"auth.begin","ws":"main","server":"acme"}`))
	recvError(t, c, "vault locked")
}

func TestAuth_UnknownWorkspace_Errors(t *testing.T) {
	c := testConn()
	c.auth(context.Background(), "auth.list", []byte(`{"cmd":"auth.list","ws":"nope"}`))
	recvError(t, c, "unknown workspace")
}

func TestAuth_UnknownAction_Errors(t *testing.T) {
	c := testConn()
	c.auth(context.Background(), "auth.frobnicate", []byte(`{"cmd":"auth.frobnicate"}`))
	recvError(t, c, "unknown action")
}
