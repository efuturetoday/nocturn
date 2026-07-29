package speaker

import "context"

// Identity is who the current speaker is taken to be. The zero value means nobody knows, which is a
// legitimate answer and must survive as one all the way to whatever reads it: an empty name is not
// an invitation to fall back on the most likely person.
type Identity struct {
	Name       string  `json:"name"`
	Confidence float32 `json:"confidence,omitempty"`
}

// Known reports whether a speaker was identified.
func (i Identity) Known() bool { return i.Name != "" }

type contextKey struct{}

// NewContext carries the current speaker into everything a turn touches.
//
// A FUNCTION rather than a value, because recognition keeps running while the conversation does. A
// tool called two minutes in should get who is speaking now, not who was speaking when the session
// opened — and a value captured at that moment cannot tell the difference.
//
// The point of putting it on the context is that a tool should not have to be told. A mail tool asked
// for "my messages" resolves the mailbox from here; the model never learns whose it was, cannot
// forget to pass it, and cannot be talked into passing someone else's. The same shape as the gate,
// which already rides here, and as credential injection, where the guest never sees the value.
func NewContext(ctx context.Context, who func() Identity) context.Context {
	if who == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, who)
}

// FromContext returns the current speaker, or the zero Identity when nobody installed one — which is
// every path that has no microphone behind it, and is why the zero value has to mean "unknown"
// rather than being a state a caller must check for separately.
func FromContext(ctx context.Context) Identity {
	who, ok := ctx.Value(contextKey{}).(func() Identity)
	if !ok || who == nil {
		return Identity{}
	}
	return who()
}
