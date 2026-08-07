// Package tui is nocturn's terminal surface: a full-screen chat over the workspace facade. It is
// the sibling of internal/serve — one drives a person at a keyboard, the other a paired device over
// a WebSocket — and neither knows about the other. Both sit on the same workspace and fold the same
// agentkit event stream.
//
// The package imports go-tui unaliased, so inside these files every tui.X is the LIBRARY; this
// package's own names are unqualified.
//
// What is testable is deliberately kept out of the templates. internal/tui/transcript is the pure
// fold from events to render state, internal/tui/logring is the log buffer, and the approver here
// is plain channels — all three are covered without a terminal. The assembled loop is not: go-tui
// offers no way to drive an App against a mock terminal, so the layout is verified by running it.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"

	tui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/tui/logring"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// ErrNoTerminal is returned when stdin or stdout is not a terminal. The chat is the one nocturn
// command that cannot degrade to a pipe: there is no non-interactive chat mode, and writing escape
// sequences into a redirect is worse than saying so.
var ErrNoTerminal = errors.New("tui: the interactive chat needs a terminal")

// Deps are what the process spine hands the UI. The workspace itself is passed separately because
// it is the thing being driven, not a dependency of the drawing.
type Deps struct {
	Feed     *Feed
	Approver *Approver
	Ring     *logring.Ring
	Model    string // the model id, for the status line
}

// Opener produces the workspace the UI drives. It is a function rather than a value because
// opening one is SLOW — vault, MCP handshakes over the network, the embedder — and until the UI
// owns the terminal, every key the user presses is echoed by the shell and lost. So the screen is
// taken first and the workspace is opened behind it. The Opener is also where the feed is attached
// and the agent schedulers are started, in that order: the chat manager snapshots its event sink
// when a session's pump starts, so a session opened before Attach would never emit.
type Opener func(context.Context) (*workspace.Workspace, error)

// Run draws the chat until the user quits or ctx is cancelled, opening the workspace behind the
// first frame. It closes the workspace on the way out.
func Run(ctx context.Context, d Deps, open Opener) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return ErrNoTerminal
	}

	loaded := make(chan opened, 1)
	root := newApp(ctx, d, loaded)
	// The post-render hook is where a pane switch lands. Focus is restored by index after every
	// render, so a request made while handling a key would be overwritten by the render that
	// follows it; settleFocus runs once that render is done. See app.focusOn.
	// WithMouse is redundant today — full-screen mode enables the mouse by default (app.go:160) —
	// and stated anyway, because the UI now depends on it: the wheel scrolls the pane under the
	// pointer and a click moves the keyboard. Leaving that to a default would make a future change
	// of the default silently remove features rather than fail to build.
	instance, err := tui.NewApp(
		tui.WithRootComponent(root),
		tui.WithMouse(),
		tui.WithPostRenderHook(root.settleFocus),
	)
	if err != nil {
		return fmt.Errorf("tui: start: %w", err)
	}

	loading := make(chan struct{})
	go func() {
		defer close(loading)
		ws, err := open(ctx)
		loaded <- opened{ws: ws, err: err}
	}()

	// Close before anything else so the alternate screen is gone before a later error reaches
	// stderr — and before a panic unwinds past us, which would otherwise leave the terminal in raw
	// mode with no echo. The workspace is closed last, and only once the opener has finished: a
	// quit during startup must not race the goroutine still building it.
	defer func() {
		// Discarded deliberately: this is what restores the terminal, and if it fails there is no
		// terminal left to say so on.
		_ = instance.Close()
		d.Approver.Close()
		d.Feed.Close()
		<-loading
		select {
		case o := <-loaded:
			if o.ws != nil {
				o.ws.Close()
			}
		default:
			if root.ws != nil {
				root.ws.Close()
			}
		}
	}()

	// A cancelled ctx (a signal the framework did not take, a parent shutting down) stops the loop
	// the same way Ctrl+Q does. The goroutine ends with the app.
	go func() {
		select {
		case <-ctx.Done():
			instance.Stop()
		case <-instance.StopCh():
		}
	}()

	if err := instance.Run(); err != nil {
		return fmt.Errorf("tui: run: %w", err)
	}
	return nil
}

// isTerminal reports whether f is a character device. Stdlib-only: pipes and regular files are not.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
