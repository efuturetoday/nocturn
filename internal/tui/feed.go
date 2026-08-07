package tui

import (
	"context"
	"sync"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// Kind tags what an Item carries. The UI switches on it exhaustively.
type Kind uint8

const (
	KindChat   Kind = iota // an event from a user chat
	KindAgent              // an event from an agent run
	KindMeta               // a chat's metadata changed: the list needs redrawing
	KindNotify             // the notify tool reached the user
)

// Item is one thing that happened, on its way to the event loop.
//
// A KindMeta carries only the id it is about. The metadata that came with it is deliberately NOT
// passed along: the UI redraws both lists from the store, which is where the truth is and which
// costs nothing worth measuring, and a copy travelling beside the id would only be the one that goes
// stale between being sent and being read.
type Item struct {
	Kind   Kind
	ChatID string
	Event  agentkit.Event
	Note   tools.Notification
}

// Feed carries everything the workspace pushes at the UI over one channel, so the event loop folds
// it in arrival order with no locking of its own.
//
// A channel rather than App.QueueUpdate, for two reasons that both bite. The sinks must be
// registered right after the workspaces open and BEFORE the agent schedulers start — the chat
// manager snapshots its sink once when a session's pump starts, so a session opened earlier never
// emits — and at that point there is no App to queue against. And QueueUpdate drops when its
// channel is full: a dropped Token silently corrupts an answer, a dropped ToolEnd leaves a call
// spinning forever. Here the buffer is generous and the send is lossless while the UI lives,
// falling through only once Close has run.
type Feed struct {
	items chan Item
	done  chan struct{}
	once  sync.Once
}

// NewFeed returns a feed with room for a long burst — a fast model streaming into a UI that is busy
// laying out a wide transcript.
func NewFeed() *Feed {
	return &Feed{items: make(chan Item, 8192), done: make(chan struct{})}
}

// Items is the stream the UI watches.
func (f *Feed) Items() <-chan Item { return f.items }

// Close stops accepting; senders fall through instead of blocking on a UI that has gone.
func (f *Feed) Close() { f.once.Do(func() { close(f.done) }) }

// Attach registers the feed as the workspace's one set of sinks. Call it after the workspaces are
// open and before the agent schedulers start.
func (f *Feed) Attach(ws *workspace.Workspace) {
	ws.Chats().OnEvent(func(id string, ev agentkit.Event) {
		f.push(Item{Kind: KindChat, ChatID: id, Event: ev})
	})
	ws.AgentChats().OnEvent(func(id string, ev agentkit.Event) {
		f.push(Item{Kind: KindAgent, ChatID: id, Event: ev})
	})
	ws.OnChatUpdate(func(m chat.Meta) {
		f.push(Item{Kind: KindMeta, ChatID: m.ID})
	})
	ws.OnNotification(func(n tools.Notification) {
		f.push(Item{Kind: KindNotify, ChatID: n.ChatID, Note: n})
	})
}

func (f *Feed) push(it Item) {
	select {
	case f.items <- it:
	case <-f.done:
	}
}

// notifier routes the notify tool into the feed, so a proactive message lands in the transcript as
// a notice instead of being printed over the UI.
type notifier struct{ feed *Feed }

var _ tools.Notifier = notifier{}

// NewNotifier returns the terminal UI's tools.Notifier.
func NewNotifier(f *Feed) tools.Notifier { return notifier{feed: f} }

func (n notifier) Notify(_ context.Context, note tools.Notification) error {
	n.feed.push(Item{Kind: KindNotify, ChatID: note.ChatID, Note: note})
	return nil
}
