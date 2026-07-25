package serve

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// This file is the app-driven half of MCP OAuth. The daemon runs the whole spec flow (discovery,
// dynamic registration, token exchange, storage) in workspace.MCPAuth; the companion app only opens a
// consent URL in the external system browser and relays back the single-use, PKCE-bound authorization
// code. The token is minted, held, and refreshed here and NEVER travels to the app, the model, or the
// guest — the app catches the deep-link redirect to appRedirect and lifts code+state from its query.

// appRedirect is the fixed redirect the companion app registers and returns on. It is a custom-scheme
// deep link (RFC 8252 native-app redirect): the consent page opens in the SYSTEM browser — so the
// user's password manager works, unlike an embedded web view — and the authorization server redirects
// here, which the OS routes back into the app as a deep link. The app lifts code+state and relays them
// as auth.callback. The scheme must match the app's registered URL scheme (iOS CFBundleURLTypes /
// Android intent-filter). Security rests on PKCE: the verifier never leaves the daemon, so a
// scheme-hijacking app that captured the code still cannot exchange it.
const appRedirect = "nocturn://oauth/callback"

// authOpTimeout bounds a single discovery-or-exchange leg so a hung authorization server can't wedge
// the handler goroutine (the discovery HTTP client has no timeout of its own).
const authOpTimeout = 45 * time.Second

// errVaultLocked is reported when the workspace has no master passphrase, so no token can be stored.
var errVaultLocked = errors.New("vault locked — unlock it on the daemon to connect an account")

// ── server → client ──────────────────────────────────────────────────────────

// AuthOpen hands the client a consent URL to open in the external browser and the redirect prefix to
// watch for. The client lifts code+state from the deep-link redirect and returns them as auth.callback
// with the same id.
type AuthOpen struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	Server         string `json:"server"`
	URL            string `json:"url"`
	RedirectPrefix string `json:"redirectPrefix"`
}

// AuthDone reports the outcome of a connect attempt: connected, or an error to show. Correlated to
// its AuthOpen by id once a session exists; a failure during auth.begin (before an id is minted)
// carries only the server, so the client clears that server's in-flight state either way.
type AuthDone struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	Server string `json:"server,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// AuthAccounts lists the workspace's connectable MCP accounts and whether each is already connected.
type AuthAccounts struct {
	Type     string              `json:"type"`
	Ws       string              `json:"ws"`
	Accounts []workspace.Account `json:"accounts"`
}

// ── client → server ──────────────────────────────────────────────────────────

// AuthList requests the connectable-accounts listing for a workspace.
type AuthList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// AuthBegin starts connecting an account: the discover-mode MCP server by name, with optional scopes.
type AuthBegin struct {
	Cmd    string   `json:"cmd"`
	Ws     string   `json:"ws"`
	Server string   `json:"server"`
	Scopes []string `json:"scopes,omitempty"`
}

// AuthCallback relays the intercepted authorization code back to finish a session begun by AuthBegin.
type AuthCallback struct {
	Cmd   string `json:"cmd"`
	Ws    string `json:"ws"`
	ID    string `json:"id"`
	Code  string `json:"code"`
	State string `json:"state"`
}

// auth dispatches an auth.* action. Begin and callback do network I/O (discovery, token exchange), so
// they run in their own goroutine with a bounded context — the socket read loop stays responsive and
// a hung authorization server cannot block other commands. Every reply still goes through c.send,
// which is safe from any goroutine.
func (c *conn) auth(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "auth.list":
		var m AuthList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad auth.list")
			return
		}
		acc, ok := c.accounts(ctx, m.Ws)
		if !ok {
			return
		}
		c.send(ctx, AuthAccounts{Type: "auth.accounts", Ws: m.Ws, Accounts: acc.List()})

	case "auth.begin":
		var m AuthBegin
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad auth.begin")
			return
		}
		acc, ok := c.accounts(ctx, m.Ws)
		if !ok {
			return
		}
		go func() {
			opCtx, cancel := context.WithTimeout(ctx, authOpTimeout)
			defer cancel()
			p, err := acc.Begin(opCtx, m.Server, m.Scopes, appRedirect)
			if err != nil {
				// A correlated failure (carrying the server), not a bare error event — so the client can
				// clear that server's spinner. No session id exists yet.
				c.send(ctx, AuthDone{Type: "auth.done", Server: m.Server, OK: false, Error: err.Error()})
				return
			}
			c.send(ctx, AuthOpen{Type: "auth.open", ID: p.ID, Server: m.Server, URL: p.AuthorizeURL, RedirectPrefix: p.RedirectPrefix})
		}()

	case "auth.callback":
		var m AuthCallback
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad auth.callback")
			return
		}
		acc, ok := c.accounts(ctx, m.Ws)
		if !ok {
			return
		}
		go func() {
			opCtx, cancel := context.WithTimeout(ctx, authOpTimeout)
			defer cancel()
			if err := acc.Complete(opCtx, m.ID, m.Code, m.State); err != nil {
				c.send(ctx, AuthDone{Type: "auth.done", ID: m.ID, OK: false, Error: err.Error()})
				return
			}
			c.send(ctx, AuthDone{Type: "auth.done", ID: m.ID, OK: true})
		}()

	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// accounts resolves a workspace and its MCP OAuth orchestrator, writing an error and returning false
// if the workspace is unknown or its vault is locked (no orchestrator).
func (c *conn) accounts(ctx context.Context, ws string) (*workspace.MCPAuth, bool) {
	w, ok := c.workspace(ctx, ws)
	if !ok {
		return nil, false
	}
	acc := w.Accounts()
	if acc == nil {
		c.failed(ctx, "auth", errVaultLocked)
		return nil, false
	}
	return acc, true
}
