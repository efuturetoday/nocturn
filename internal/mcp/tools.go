package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
)

// Connect performs the MCP handshake (initialize + notifications/initialized)
// through the gated transport.
func (c *Conn) Connect(ctx context.Context) error {
	c.log.Debug("mcp connect", "url", c.server.URL)
	if err := c.client.Initialize(ctx); err != nil {
		return fmt.Errorf("mcp: connect %s: %w", c.server.Name, err)
	}
	return nil
}

// Tools lists the server's tools (paginated tools/list) and maps each onto an
// agentkit tool. The exposed name is "<server>_<sanitized>": MCP tool names are
// an arbitrary string (the MCP spec puts no charset on them), but OpenAI/agentkit
// require ^[a-zA-Z0-9_-]{1,64}$ — a dot is a hard HTTP-400 — so the remote name
// is SANITIZED for the model while the tool is still CALLED on the wire with its
// ORIGINAL name (the server expects "github.create_issue", not the alias). The
// input schema comes from the server (already ingress-redacted at the transport)
// and must parse to an object schema (fail closed — it is handed to the model
// verbatim). Every call runs tools/call through the gated transport, so a remote
// tool is unreachable without clearing the gate.
func (c *Conn) Tools(ctx context.Context) ([]agentkit.Tool, error) {
	infos, err := c.client.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: list %s: %w", c.server.Name, err)
	}
	c.log.Debug("mcp tools listed", "count", len(infos))
	out := make([]agentkit.Tool, 0, len(infos))
	seen := map[string]bool{}
	for _, info := range infos {
		clean := sanitizeName(info.Name)
		if clean == "" {
			// A name of only invalid chars leaves nothing to expose; skip the whole server (installMCP
			// logs + drops it) rather than surface a bare "<server>_" alias.
			return nil, fmt.Errorf("mcp: %s: tool %q has no usable name after sanitizing", c.server.Name, info.Name)
		}
		exposed := c.server.Name + "_" + clean
		if len(exposed) > 64 {
			exposed = exposed[:64] // OpenAI hard cap on the function name
		}
		if seen[exposed] {
			// Two remote names can collapse to the same alias (e.g. "a.b" and "a-b"); never
			// silently overwrite — skip the later one so the first mapping stands.
			return nil, fmt.Errorf("mcp: %s: tool alias %q collides (from %q)", c.server.Name, exposed, info.Name)
		}
		seen[exposed] = true

		schema, err := agentkit.ParseSchema(info.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("mcp: %s: tool %q schema: %w", c.server.Name, info.Name, err)
		}
		if schema == nil || schema.Type != agentkit.TypeObject {
			return nil, fmt.Errorf("mcp: %s: tool %q inputSchema must be a JSON object schema", c.server.Name, info.Name)
		}

		original := info.Name // capture per iteration; the wire name, not the alias
		t, err := agentkit.NewTool(exposed, info.Description, func(ctx context.Context, args string) (string, error) {
			if args != "" && !json.Valid([]byte(args)) {
				return "", errors.New("invalid arguments: not valid JSON")
			}
			return c.client.CallTool(ctx, original, json.RawMessage(args)) // owner + gating happen in transport
		}, agentkit.WithSchema(schema))
		if err != nil {
			// agentkit's own name regex is the backstop; a sanitized name that still fails is a bug.
			return nil, fmt.Errorf("mcp: %s: tool %q: %w", c.server.Name, info.Name, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// sanitizeName maps a remote MCP tool name onto the OpenAI/agentkit charset:
// every rune not in [A-Za-z0-9_-] becomes '_', repeated separators collapse, and
// leading/trailing separators are trimmed. It lowercases for a stable alias. An
// empty result (a name of only invalid chars) yields "" — the caller's length
// and NewTool checks then reject it.
func sanitizeName(name string) string {
	var b strings.Builder
	prevSep := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSep = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			prevSep = false
		default:
			if !prevSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			prevSep = true
		}
	}
	return strings.Trim(b.String(), "_")
}
