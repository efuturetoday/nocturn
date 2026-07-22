// Package mcp connects to remote (HTTP) MCP servers — the SAFE half of MCP
// (ADR-9): a remote server runs no code on your machine, so talking to it is
// just gated HTTP. Two layers live here, one per file:
//
//   - client.go — the protocol Client: JSON-RPC 2.0 over an injected Transport,
//     STDLIB-ONLY, performing no I/O of its own. It covers exactly the tools
//     subset a client needs over the Streamable HTTP transport (spec revision
//     2025-11-25): initialize + notifications/initialized, tools/list
//     (paginated), tools/call — with both response shapes the spec requires
//     (application/json and text/event-stream).
//   - conn.go/config.go/tools.go — the gated Conn: it supplies the Transport,
//     putting every JSON-RPC POST through the shared gate as an http.write on
//     the net host-allowlist, with host-owned credential injection and the
//     bidirectional leak scan, and maps the server's tools onto agentkit tools.
//
// Keeping the protocol Client I/O-free and stdlib-only is what lets every effect
// a tool call performs still cross the gate — the Conn is the ONLY path to the
// wire, so a remote tool is unreachable without clearing it.
//
// Deliberately NOT implemented (deny-by-default, see FRAGEN.md): sampling — a
// remote server must NEVER drive our LLM, so the client declares no
// capabilities and skips every server-initiated request — plus resources,
// prompts, elicitation, roots, completion, and SSE stream resumption.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// latestProtocolVersion is the newest MCP revision this client speaks. The spec
// says the initialize request SHOULD carry the latest version the client
// supports; the server either echoes it or answers with another one.
const latestProtocolVersion = "2025-11-25"

// supportedProtocolVersions are the revisions this client accepts a server to
// negotiate down to — the tools-over-Streamable-HTTP subset implemented here is
// identical across them. A server answering with anything else fails Initialize
// (the spec says the client SHOULD disconnect; we fail closed).
var supportedProtocolVersions = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
}

// maxToolPages caps tools/list pagination so a malicious server cannot spin the
// client forever on an endless cursor chain (fail closed).
const maxToolPages = 64

// maxSSELine caps a single SSE line — one data: frame carries a whole JSON-RPC
// message, so this bounds a message, mirroring the net tool's response cap.
const maxSSELine = 10 << 20 // 10 MiB

// Response is what a Transport returns: the HTTP Content-Type (raw header
// value; the client parses it), the response headers (for Mcp-Session-Id), and
// the body stream. For a 202 Accepted to a notification, Body may be nil.
type Response struct {
	ContentType string
	Header      http.Header
	Body        io.ReadCloser
}

// Transport POSTs one JSON-RPC message body to the server's MCP endpoint and
// returns the response stream. header carries the protocol-level request
// headers the spec REQUIRES on the wire (Accept, Content-Type, Mcp-Session-Id,
// MCP-Protocol-Version) — the transport must copy them onto the HTTP request
// verbatim, and must return an error for a non-2xx status. It is where the
// caller puts the endpoint, the gated HTTP call, and credential injection —
// the client itself performs no I/O.
type Transport func(ctx context.Context, body []byte, header http.Header) (*Response, error)

// Client is a session with one remote MCP server. It is concurrency-safe: tool
// calls may run in parallel once Initialize has completed.
type Client struct {
	transport Transport
	id        atomic.Int64

	mu       sync.Mutex
	session  string // Mcp-Session-Id, set by initialize if the server assigns one
	protocol string // negotiated protocol version, set by Initialize
}

// New builds a client over the given transport.
func New(t Transport) *Client { return &Client{transport: t} }

// ToolInfo is a tool the server advertises (tools/list): its name, description,
// and JSON Schema for arguments — mapped 1:1 to an agentkit tool by the caller.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcMessage is a decoded JSON-RPC message from the server: a response (ID set,
// no Method) or a server-initiated request/notification (Method set), which
// this tools-only client skips. ID stays raw so an exotic id shape on a server
// message never breaks decoding of the stream.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp: server error %d: %s", e.Code, e.Message) }

// requestHeader builds the spec-required headers for the next request: the
// Accept pair (the client MUST list both content types), the session id the
// server assigned at initialize (MUST be echoed on all subsequent requests),
// and the negotiated MCP-Protocol-Version (MUST accompany all requests after
// initialize).
func (c *Client) requestHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	if c.session != "" {
		h.Set("Mcp-Session-Id", c.session)
	}
	if c.protocol != "" {
		h.Set("MCP-Protocol-Version", c.protocol)
	}
	c.mu.Unlock()
	return h
}

// call performs one JSON-RPC request/response round-trip and returns the raw
// result. A server-side JSON-RPC error is returned as an error.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.id.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	resp, err := c.transport(ctx, body, c.requestHeader())
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("mcp: nil response to %s", method)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	msg, err := readMessage(resp, id)
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: %w", method, err)
	}
	if msg.Error != nil {
		return nil, msg.Error
	}
	return msg.Result, nil
}

// readMessage extracts the JSON-RPC response for request id from resp. The
// server chooses the shape — the spec requires the client to support both: a
// single application/json object, or a text/event-stream whose data: frames
// carry JSON-RPC messages (possibly interleaved server notifications/requests
// before the response), correlated by request id. Any other content type is
// rejected fail-closed.
func readMessage(resp *Response, id int64) (*rpcMessage, error) {
	if resp.Body == nil {
		return nil, errors.New("empty response to a request")
	}
	ct, _, _ := mime.ParseMediaType(resp.ContentType)
	switch ct {
	case "application/json":
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var msg rpcMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("bad response: %w", err)
		}
		if !idMatches(msg.ID, id) {
			return nil, fmt.Errorf("response id %s does not match request id %d", msg.ID, id)
		}
		return &msg, nil
	case "text/event-stream":
		return readSSE(resp.Body, id)
	default:
		return nil, fmt.Errorf("unexpected content type %q", resp.ContentType)
	}
}

// readSSE reads SSE events (per the SSE standard: data: lines accumulate, a
// blank line terminates the event, multiple data: lines join with \n) until it
// finds the JSON-RPC response with the given id. Priming events (empty data),
// comments, and server-initiated notifications/requests are skipped — this
// client answers no server request by design (no sampling). event:/id:/retry:
// fields are ignored; reconnection/resumption (Last-Event-ID) is not
// implemented (see FRAGEN.md).
func readSSE(body io.Reader, id int64) (*rpcMessage, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), maxSSELine)
	var data []string
	flush := func() (*rpcMessage, bool) {
		if len(data) == 0 {
			return nil, false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var msg rpcMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, false // not a JSON-RPC message; keep waiting for ours
		}
		if msg.Method != "" || !idMatches(msg.ID, id) {
			return nil, false // a server notification/request, or another id
		}
		return &msg, true
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "": // event boundary
			if msg, ok := flush(); ok {
				return msg, nil
			}
		case strings.HasPrefix(line, ":"): // comment / keep-alive
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default: // event:, id:, retry:, unknown fields — ignored
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if msg, ok := flush(); ok { // stream ended right after the final frame
		return msg, nil
	}
	return nil, errors.New("event stream ended without a response")
}

// idMatches reports whether a response's raw id equals our numeric request id.
func idMatches(raw json.RawMessage, id int64) bool {
	return string(bytes.TrimSpace(raw)) == strconv.FormatInt(id, 10)
}

// notify sends a JSON-RPC notification (no id — no response expected; the spec
// requires the server to answer 202 Accepted with no body, and the transport
// surfaces any HTTP error status as err).
func (c *Client) notify(ctx context.Context, method string, params any) error {
	body, err := json.Marshal(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	resp, err := c.transport(ctx, body, c.requestHeader())
	if err != nil {
		return err
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return nil
}

// Session returns the server-assigned session id (empty if none). The client
// echoes it on subsequent requests itself; this accessor is informational.
func (c *Client) Session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// Initialize performs the MCP lifecycle handshake: the initialize request
// (protocolVersion + capabilities + clientInfo), version negotiation (the
// server may answer with a different revision; an unsupported one fails
// closed), capture of the server-assigned Mcp-Session-Id, and the
// notifications/initialized notification. It must be called once before
// ListTools or CallTool. The client declares NO capabilities: nocturn refuses
// sampling/elicitation/roots by construction — a remote server must never
// drive our LLM.
func (c *Client) Initialize(ctx context.Context) error {
	res, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": latestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "nocturn", "version": "0"},
	})
	if err != nil {
		return err
	}
	var out struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return fmt.Errorf("mcp: bad initialize result: %w", err)
	}
	if !supportedProtocolVersions[out.ProtocolVersion] {
		return fmt.Errorf("mcp: unsupported server protocol version %q", out.ProtocolVersion)
	}
	c.mu.Lock()
	c.protocol = out.ProtocolVersion
	c.mu.Unlock()
	return c.notify(ctx, "notifications/initialized", nil)
}

// ListTools fetches the server's advertised tools (tools/list), following
// nextCursor pagination to the end (capped at maxToolPages, fail closed).
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var tools []ToolInfo
	cursor := ""
	for range maxToolPages {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var out struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &out); err != nil {
			return nil, fmt.Errorf("mcp: bad tools/list result: %w", err)
		}
		tools = append(tools, out.Tools...)
		if out.NextCursor == "" {
			return tools, nil
		}
		cursor = out.NextCursor
	}
	return nil, fmt.Errorf("mcp: tools/list did not terminate within %d pages", maxToolPages)
}

// CallTool invokes a tool (tools/call) with JSON arguments and returns its text
// content. A tool that reports isError=true is returned as an error, so the
// model sees it as a failed tool call it can correct.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	res, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("mcp: bad tools/call result: %w", err)
	}
	var text strings.Builder
	for _, cnt := range out.Content {
		if cnt.Type == "text" {
			text.WriteString(cnt.Text)
		}
	}
	if out.IsError {
		return "", fmt.Errorf("mcp tool %s failed: %s", name, text.String())
	}
	return text.String(), nil
}
