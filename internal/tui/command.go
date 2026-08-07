package tui

import (
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/tui/transcript"
)

// command runs a slash command typed into the composer and reports whether it handled the line. An
// unknown /word is NOT handled — it goes to the model, the way it always has, because the assistant
// is allowed to be asked about things that start with a slash.
//
// These are the typist's way in and stay exactly as they were. The palette (Ctrl+P) is the other
// way in, for the far more common case of not knowing the name of the thing you want.
func (a *app) command(line string) bool {
	verb, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	rest = strings.TrimSpace(rest)

	switch verb {
	case "/quit", "/exit":
		a.quit()
	case "/help":
		// "What can I do" is a list of things to do, and the palette IS that list. It used to be one
		// line of slash commands written into the transcript, where it sat between a question and its
		// answer for the rest of the conversation.
		a.openPalette()
	case "/new":
		a.newChat()
	case "/open":
		a.openCommand(rest)
	case "/chats":
		a.gotoList()
	case "/agents":
		a.listAgents()
	case "/fire":
		a.fire(rest)
	default:
		return false
	}
	return true
}

// notice puts a line the UI itself produced into the transcript, where the answer it is about is.
// Reserved for things that belong to the CONVERSATION — a notification that arrived while it was on
// screen, a workspace that failed to open. Answers to questions about the UI go to the palette or
// the flash line, not here: the transcript is a record of what was said, and it is kept.
func (a *app) notice(text string) {
	a.view.Set(transcript.PushNotice(a.view.Get(), text))
}

// openCommand opens a conversation by id, whichever manager owns it. It used to force "user", which
// meant an agent run could be listed, clicked and opened from the sidebar but never named by id —
// the same conversation reachable one way and not the other.
func (a *app) openCommand(id string) {
	if id == "" {
		a.say("usage: /open <id>")
		return
	}
	if !a.ready() {
		a.say("still opening the workspace…")
		return
	}
	kind := "user"
	if m, ok := a.metaFor(id); ok && m.Source == chat.SourceAgent {
		kind = "agent"
	}
	a.openIn(kind, id)
}

// listAgents answers "what can I run" by opening the palette on that question. The answer used to be
// written into the transcript, one notice per agent, which put UI output in the record of the
// conversation and left it there.
func (a *app) listAgents() {
	if !a.ready() {
		a.say("still opening the workspace…")
		return
	}
	if len(a.ws.Agents()) == 0 {
		a.say("no agents declared — see docs/agents")
		return
	}
	a.openPalette()
	a.goToStep(stepFire)
}

func (a *app) fire(rest string) {
	name, task, _ := strings.Cut(rest, " ")
	if name == "" {
		a.say("usage: /fire <name> <task> — Ctrl+P lists them")
		return
	}
	if !a.ready() {
		a.say("still opening the workspace…")
		return
	}
	id, err := a.ws.FireAgent(a.ctx, name, strings.TrimSpace(task))
	if err != nil {
		a.say("fire " + name + ": " + err.Error())
		return
	}
	a.say("fired " + name)
	// Open what was just started. Asking for a run and then being left in the conversation you were
	// already in is the version this replaces: the only sign anything had happened was a line that
	// faded after four seconds, and the run itself streamed into a chat nobody was looking at — the
	// event loop routes by the OPEN chat's id, so its tokens went to the background and its "thinking"
	// never reached the bar.
	//
	// By the time FireAgent returns, Fire has stamped the owner, opened the session, recorded the task
	// and touched the store — so the run is in the list and Inflight already carries the task, which
	// is what Seed replays. There is no window where this opens an empty transcript.
	a.openRun(id)
	// openIn zeroes the clock, because a turn that was already running when it was opened has no
	// start anyone recorded. This one does: it started here.
	a.turnStart = time.Now()
}
