package serve

import "net/http"

// Option configures a daemon. Serve takes what it cannot run without as parameters and everything
// else here, which is what keeps a new front-end from widening a signature every other caller then
// has to pass nil into.
type Option func(*options)

// options is the configurable half of a daemon. Its zero value is the daemon as it was before any of
// this existed: protocol only, no assets, version unknown.
type options struct {
	webUI            http.Handler
	version          string
	onDevicesChanged func()
}

// WithWebUI serves h for every path the protocol does not claim, so the browser front-end and the
// WebSocket it opens share one origin and one port.
//
// Passing a handler is what makes this package indifferent to where the assets come from: it never
// imports internal/webui, so a daemon with no UI is not a daemon with a disabled feature, it is one
// that was never given one. That is also all `--no-web` does.
func WithWebUI(h http.Handler) Option {
	return func(o *options) { o.webUI = h }
}

// WithVersion sets the version reported by GET /daemon.json, so a browser can say what it is talking
// to. The binary's version lives in package main, which this package must not import.
func WithVersion(v string) Option {
	return func(o *options) { o.version = v }
}

// OnDevicesChanged runs fn after the device registry is modified from the wire.
//
// It exists for one thing that would otherwise be a trap: the daemon writes its own command line a
// credential (mode 0600, beside the vault), and forgetting that device from the UI would leave
// `nocturn pair` answering 401 until someone restarted the daemon — a rough edge discovered at
// exactly the wrong moment. With this hook the daemon re-mints it immediately, so forgetting the tool
// row ROTATES the credential instead of disabling it.
//
// Rotating is the correct meaning, not a softening. The reason to revoke a local credential is that
// the file leaked, and the old token stays dead either way; whoever can read the replacement could
// have read the original, because the authority being expressed is "can read this directory". What
// the revocation buys is that the copy someone walked off with stops working, and that survives.
//
// A callback rather than this package doing it: where the credential lives and what it is called are
// cmd/nocturn's business, and one writer owns that file. serve knows only that the registry moved.
//
// Named On… rather than With…, which breaks the usual functional-option convention on purpose: it
// registers a hook, not a value, and On… is what this codebase already calls that everywhere it
// happens (workspace.OnChatUpdate, OnNotification, OnReminderChange, chat.Manager.OnEvent). Reading
// consistently with its neighbours is worth more here than reading consistently with WithWebUI.
func OnDevicesChanged(fn func()) Option {
	return func(o *options) { o.onDevicesChanged = fn }
}

// apply folds opts onto the defaults.
func apply(opts []Option) options {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	if o.version == "" {
		o.version = "dev"
	}
	return o
}
