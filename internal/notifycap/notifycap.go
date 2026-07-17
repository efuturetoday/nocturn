// Package notifycap is the notification capability: notify — the assistant
// reaching the user proactively (fire-and-forget), the OTHER half of HITL.
//
// HITL asks and waits; notify just tells ("flight delayed", "task done"). It is
// deliberately low-friction: a message to the user's OWN device is not a per-
// message-approvable mutation, so under the base policy it runs silently
// (Write:false → Allow, no "may I tell you X?" prompt). The controls that make that
// safe are STRUCTURAL, not a prompt:
//
//   - the destination is HOST-OWNED (the configured channel), never model-chosen,
//     so notify can't become an exfiltration channel to a third party — the model
//     supplies content, never the target (exactly like credential injection);
//   - the message is LEAK-SCANNED on egress, so a vault secret can't ride out in a
//     notification;
//   - it is RATE-LIMITED, so a prompt-injected caller can't spam the user's phone.
//
// It still passes the Guard, so a workspace/agent policy can tighten it to Ask.
package notifycap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// channel is the single host-owned notification target today. It is a Target (not
// a bare string) so a future cage could scope notify per channel (phone/email/…),
// and so the base policy's target-bearing wildcard rule matches (a targetless call
// would fail the "*" rule and be denied).
const channel = "user"

// Pusher delivers a notification to the user's channel. It is a PORT: ntfy (phone)
// is one adapter, a TUI line (attended) another — so notifycap stays transport-
// agnostic exactly like hitl.Notifier.
type Pusher interface {
	Push(ctx context.Context, title, message string) error
}

// Notifier is the notification capability group: the shared Guard, the transport,
// and the egress leak scanner. Anti-spam rate limiting is NOT here — it lives in the
// Guard's gated pipeline (per-family, on every authorized path, including the base
// Allow that notify takes), so a rate refusal comes back as a gateway.RateLimitedError
// with a retry-after the model can act on.
type Notifier struct {
	Guard   *gateway.Guard
	Push    Pusher
	Scanner *secret.Scanner
}

// New builds the notification capability over guard, delivering via push and
// egress-scanning with scanner.
func New(guard *gateway.Guard, push Pusher, scanner *secret.Scanner) *Notifier {
	return &Notifier{Guard: guard, Push: push, Scanner: scanner}
}

// Tools exposes the capability as the single notify tool.
func (n *Notifier) Tools() []tool.Tool { return []tool.Tool{n.notifyTool()} }

func (n *Notifier) notifyTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: "notify",
			Description: "Send a notification to the user's own device (fire-and-forget). Use it to proactively " +
				"tell the user something — a result, a reminder, a heads-up. It does NOT ask a question or wait " +
				"for a reply (use an approval for that). Returns {\"sent\":true}.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"message":{"type":"string","description":"The notification text"},` +
				`"title":{"type":"string","description":"Optional short title"}` +
				`},"required":["message"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Message string `json:"message"`
				Title   string `json:"title"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(a.Message) == "" {
				return "", errors.New("missing required field: message")
			}
			// Write:false — a message to the user's own device runs silently under base
			// policy. Target is the host-owned channel, never model-chosen. The message
			// text is the egress surface: a vault secret in it is blocked before it leaves.
			call := capability.Call{Family: "notify", Write: false, Target: channel}
			intent := "notify: " + truncate(a.Message, 80)
			return gateway.Do(ctx, n.Guard, call, intent,
				gateway.ScanEgress(n.Scanner, func() []string { return []string{a.Title, a.Message} }),
				func() (string, error) {
					if err := n.Push.Push(ctx, a.Title, a.Message); err != nil {
						return "", err
					}
					return `{"sent":true}`, nil
				})
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
