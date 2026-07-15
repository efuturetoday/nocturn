package mcpcap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Connect performs the MCP handshake (initialize + notifications/initialized)
// through the gated transport.
func (c *Conn) Connect(ctx context.Context) error {
	ctx = gateway.WithIntent(ctx, "MCP "+c.server.Name+": connect ("+c.server.URL+")")
	if err := c.client.Initialize(ctx); err != nil {
		return fmt.Errorf("mcpcap: connect %s: %w", c.server.Name, err)
	}
	return nil
}

// toolNameRe follows the MCP tool-name guidance (letters, digits, _, -, .;
// 1–128 chars) — anything else from a remote server is rejected fail-closed
// before it can masquerade in the registry or the HITL prompt.
var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Tools lists the server's tools (paginated tools/list) and maps each onto a
// nocturn tool, namespaced "<server>.<tool>". Description and input schema come
// from the server (already ingress-redacted at the transport); the schema must
// be a JSON object schema (fail closed — it is handed to the model verbatim).
// Each Invoke stamps the semantic intent "MCP <server>: <tool>" so an Ask
// prompts at the tool level rather than the raw transport, then runs
// tools/call through the same gated transport — a remote tool is unreachable
// without passing the broker.
func (c *Conn) Tools(ctx context.Context) ([]tool.Tool, error) {
	ctx = gateway.WithIntent(ctx, "MCP "+c.server.Name+": list tools")
	infos, err := c.client.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcpcap: list %s: %w", c.server.Name, err)
	}
	tools := make([]tool.Tool, 0, len(infos))
	seen := map[string]bool{}
	for _, info := range infos {
		if !toolNameRe.MatchString(info.Name) {
			return nil, fmt.Errorf("mcpcap: %s: bad tool name %q", c.server.Name, info.Name)
		}
		if seen[info.Name] {
			return nil, fmt.Errorf("mcpcap: %s: duplicate tool %q", c.server.Name, info.Name)
		}
		seen[info.Name] = true
		var obj map[string]any
		if len(info.InputSchema) == 0 || json.Unmarshal(info.InputSchema, &obj) != nil || obj["type"] != "object" {
			return nil, fmt.Errorf("mcpcap: %s: tool %q inputSchema must be a JSON object schema", c.server.Name, info.Name)
		}
		name := info.Name
		tools = append(tools, tool.Tool{
			Spec: tool.Spec{
				Name:        c.server.Name + "." + name,
				Description: info.Description,
				Parameters:  info.InputSchema,
			},
			Invoke: func(ctx context.Context, args string) (string, error) {
				if args != "" && !json.Valid([]byte(args)) {
					return "", errors.New("invalid arguments: not valid JSON")
				}
				ctx = gateway.WithIntent(ctx, "MCP "+c.server.Name+": "+name)
				return c.client.CallTool(ctx, name, json.RawMessage(args))
			},
		})
	}
	return tools, nil
}
