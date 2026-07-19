package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/efuturetoday/nocturn/internal/device"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/push"
)

// pushNotifier is the out-of-band channel over native mobile push. It satisfies BOTH ports the
// ntfy publisher used to: hitl.Notifier (an approval wake) and notifycap.Pusher (a proactive
// notify). It fans a message to every registered device token; the actual approve/deny happens
// in-app over the WebSocket, so a push carries no token — it is only a wake. It deliberately does
// NOT implement Resolved() (fire-and-forget, per the hitl contract).
type pushNotifier struct {
	sender  push.Sender
	devices *device.Store
	log     *slog.Logger
}

// Notify wakes a phone about a pending approval. No reachable device → an error, so the engine
// fails closed (nobody could be asked out of band).
func (n *pushNotifier) Notify(intent string, _ []hitl.Option) error {
	return n.send("Approval needed", intent, "approval")
}

// Push delivers a proactive notification (the notify tool) to the phone.
func (n *pushNotifier) Push(_ context.Context, title, message string) error {
	return n.send(title, message, "notify")
}

// send fans one message to the APNs-reachable (iOS) device tokens. Android (FCM) tokens are
// skipped until an FCM sender lands; the platform on each target routes the provider.
func (n *pushNotifier) send(title, body, kind string) error {
	var tokens []string
	for _, t := range n.devices.PushTargets() {
		if t.Platform == "" || t.Platform == device.PlatformIOS { // empty = legacy iOS
			tokens = append(tokens, t.Token)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := n.sender.Send(ctx, push.Message{Title: title, Body: body, Data: map[string]string{"type": kind}}, tokens)
	// The one line that answers "did the push reach APNs, and if not why" — the APNs reason
	// (BadDeviceToken, InvalidProviderToken, …) rides in err.
	if err != nil {
		n.log.ErrorContext(ctx, "push send failed", slog.String("kind", kind), slog.Int("tokens", len(tokens)), slog.Any("err", err))
	} else {
		n.log.InfoContext(ctx, "push sent", slog.String("kind", kind), slog.Int("tokens", len(tokens)))
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
