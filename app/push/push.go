// Package push is the native mobile-push side of the companion app: a provider-agnostic Sender port
// plus the APNs adapter (apns.go) that implements it. It carries NO approval authority — a push is
// only a WAKE ("you have a pending approval"); the approve/deny happens in-app over the authenticated
// WebSocket, so the signed HITL decision never leaves the daemon. Which devices are reachable is the
// auth store's (a push token lives on the paired device); this package only delivers to tokens the
// caller supplies.
package push

import "context"

// Message is one push to deliver. Title/Body are the user-visible notification; Data is an opaque
// key/value payload the app reads on tap. It never carries an approval token — resolution is in-app.
type Message struct {
	Title string
	Body  string
	Data  map[string]string
}

// Sender delivers a Message to a set of device tokens. The APNs adapter implements it; a nil Sender
// (no provider configured) means push is simply off.
type Sender interface {
	Send(ctx context.Context, m Message, tokens []string) error
}
