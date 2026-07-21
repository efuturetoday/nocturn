package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/app/secret"
)

// NotifyKind is the gate Kind the notify tool checks. Its Target is the host-owned constant channel
// "user" — never model-chosen — so a policy can gate "reaching the user" without the model choosing a
// different destination. Under the base policy notify runs silently (Allowed); the gate is present so
// a stricter per-agent policy can tighten it to Ask.
const NotifyKind = "notify"

// notifyChannel is the single, host-owned destination a notify targets. The model supplies the text,
// never the channel.
const notifyChannel = "user"

// Notifier reaches the user out of band — a phone push, or the terminal. A nil Notifier means the
// notify tool is not offered at all.
type Notifier interface {
	Notify(ctx context.Context, title, message string) error
}

// notify is the proactive-notification tool group.
type notify struct {
	notifier Notifier
	scanner  *secret.Scanner
}

func (n notify) tool() (agentkit.Tool, error) {
	return agentkit.NewTool("notify",
		`Send a notification to the user out of band (e.g. a phone push). Returns {"sent":true}.`,
		n.send,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("message", agentkit.String("The notification message")),
			agentkit.Prop("title", agentkit.String("Optional title")),
		).Require("message")),
	)
}

func (n notify) send(ctx context.Context, args string) (string, error) {
	var a struct {
		Message string `json:"message"`
		Title   string `json:"title"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Message == "" {
		return "", errors.New("missing required field: message")
	}
	if err := gate.Check(ctx, gate.Action{Kind: NotifyKind, Target: notifyChannel}, nil); err != nil {
		return "", err
	}
	// Egress: the text leaves the box to the user's device — block a smuggled vault secret before it
	// goes out.
	if n.scanner != nil {
		if err := n.scanner.ScanEgress(a.Title, a.Message); err != nil {
			return "", fmt.Errorf("egress blocked: %w", err)
		}
	}
	if err := n.notifier.Notify(ctx, a.Title, a.Message); err != nil {
		return "", fmt.Errorf("notify: %w", err)
	}
	return `{"sent":true}`, nil
}
