package chat

import "context"

// ConvoRef identifies a conversation: its workspace and chat id. It is stamped onto every turn's
// ctx (by the manager's decorator), so an out-of-band effect raised DURING a turn — an approval
// or a notify push — can carry a deep-link back to the exact chat the app should open.
type ConvoRef struct {
	WS     string
	ChatID string
}

type convoKey struct{}

// WithConvo stamps the conversation ref onto ctx.
func WithConvo(ctx context.Context, ws, chatID string) context.Context {
	return context.WithValue(ctx, convoKey{}, ConvoRef{WS: ws, ChatID: chatID})
}

// ConvoFrom returns the conversation ref carried by ctx, or ok=false on a bare context.
func ConvoFrom(ctx context.Context) (ConvoRef, bool) {
	r, ok := ctx.Value(convoKey{}).(ConvoRef)
	return r, ok
}
