package session

import "context"

// ApprovalSink surfaces an approval request to a client and remembers how to enact
// it. *Runner implements it. Whoever needs a human decision (the gateway's attended
// notifier) finds the sink on ctx — stamped by the Runner running the turn — so the
// request lands on the right session's event stream. The engine stays free of any
// approval-mechanism types: intent + labels are shown, apply(choice) is an opaque
// callback the caller supplies to enact the chosen option (e.g. resolve a hitl token).
type ApprovalSink interface {
	// PresentApproval surfaces the request on the stream and records how to enact it.
	PresentApproval(intent string, labels []string, apply func(choice int))
	// HasClients reports whether a real client is watching right now — the router reads it
	// at Ask-time to decide whether the request must ALSO go out-of-band to the phone.
	HasClients() bool
	// ClearPending drops the parked request and announces it resolved — called when a
	// decision arrives through any channel (including out of band), so the snapshot never
	// shows a phantom prompt. Idempotent.
	ClearPending()
}

type approvalSinkKey struct{}

// WithApprovalSink stamps the sink onto ctx for the duration of a turn.
func WithApprovalSink(ctx context.Context, s ApprovalSink) context.Context {
	return context.WithValue(ctx, approvalSinkKey{}, s)
}

// ApprovalSinkFrom returns the sink carried by ctx (nil for an unattended run — the
// caller then falls back to its out-of-band channel).
func ApprovalSinkFrom(ctx context.Context) ApprovalSink {
	s, _ := ctx.Value(approvalSinkKey{}).(ApprovalSink)
	return s
}
