package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/device"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/push"
)

// pushNotifier is the out-of-band channel over native mobile push. It backs both a proactive
// notify (notifycap.Pusher) and — via a per-request bound notifier (bound) — an approval push.
// Every push carries a `type` (approval|answer|notify) and, when known, `ws`+`chatId` so the app
// can deep-link to the exact chat. It is only a WAKE — the real state comes over the WebSocket,
// so a push carries no token or decision.
type pushNotifier struct {
	sender  push.Sender
	devices *device.Store
	log     *slog.Logger
}

// Push delivers a proactive notification (the notify tool) to the phone. It reads the conversation
// ref off ctx (stamped by the chat decorator) so the app can open the originating chat.
func (n *pushNotifier) Push(ctx context.Context, title, message string) error {
	data := map[string]string{"type": "notify"}
	if ref, ok := chat.ConvoFrom(ctx); ok {
		data["ws"], data["chatId"] = ref.WS, ref.ChatID
	}
	return n.send(title, message, data)
}

// Answer wakes a backgrounded user that a chat produced a reply — carries ws+chatId so a tap
// opens that chat.
func (n *pushNotifier) Answer(ws, chatID string) error {
	return n.send("Nocturn", "Your answer is ready", map[string]string{"type": "answer", "ws": ws, "chatId": chatID})
}

// bound returns an hitl.Notifier for one approval request, carrying the conversation ref the
// router extracted from the request ctx — so the approval push deep-links to the asking chat.
func (n *pushNotifier) bound(ref chat.ConvoRef) hitl.Notifier {
	return boundApproval{n: n, ref: ref}
}

type boundApproval struct {
	n   *pushNotifier
	ref chat.ConvoRef
}

// Notify wakes a phone about a pending approval. No reachable device → an error, so the engine
// fails closed (nobody could be asked out of band).
func (b boundApproval) Notify(intent string, _ []hitl.Option) error {
	return b.n.send("Approval needed", intent, map[string]string{"type": "approval", "ws": b.ref.WS, "chatId": b.ref.ChatID})
}

// send fans one message to the APNs-reachable (iOS) device tokens. Android (FCM) tokens are
// skipped until an FCM sender lands; the platform on each target routes the provider.
func (n *pushNotifier) send(title, body string, data map[string]string) error {
	var tokens []string
	for _, t := range n.devices.PushTargets() {
		if t.Platform == "" || t.Platform == device.PlatformIOS { // empty = legacy iOS
			tokens = append(tokens, t.Token)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := n.sender.Send(ctx, push.Message{Title: title, Body: body, Data: data}, tokens)
	// The one line that answers "did the push reach APNs, and if not why" — the APNs reason
	// (BadDeviceToken, InvalidProviderToken, …) rides in err.
	if err != nil {
		n.log.ErrorContext(ctx, "push send failed", slog.String("kind", data["type"]), slog.Int("tokens", len(tokens)), slog.Any("err", err))
	} else {
		n.log.InfoContext(ctx, "push sent", slog.String("kind", data["type"]), slog.Int("tokens", len(tokens)))
	}
	return err
}

// Available reports whether any device can currently receive an out-of-band push — the router
// reads it to decide whether a background approval can go to a phone at all.
func (n *pushNotifier) Available() bool { return n.devices.CanOOB() }

// buildAPNS constructs the APNs sender from NOCTURN_APNS_* env, or returns nil when unconfigured
// (push simply off, like ntfy-unset before). A dead token reported by APNs is pruned from the
// device store.
func buildAPNS(devices *device.Store) push.Sender {
	keyPath := os.Getenv("NOCTURN_APNS_KEY")
	keyID := os.Getenv("NOCTURN_APNS_KEY_ID")
	teamID := os.Getenv("NOCTURN_APNS_TEAM_ID")
	bundleID := os.Getenv("NOCTURN_APNS_BUNDLE_ID")
	if keyPath == "" || keyID == "" || teamID == "" || bundleID == "" {
		return nil
	}
	sender, err := push.NewAPNS(push.APNSConfig{
		KeyPath:    keyPath,
		KeyID:      keyID,
		TeamID:     teamID,
		BundleID:   bundleID,
		Production: os.Getenv("NOCTURN_APNS_PRODUCTION") == "1",
		OnBadToken: devices.RemovePushToken,
	})
	if err != nil {
		fmt.Printf("APNs push disabled: %v\n", err)
		return nil
	}
	return sender
}
