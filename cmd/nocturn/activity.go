package main

import (
	"sync"

	"github.com/efuturetoday/nocturn/internal/appserver"
)

// syncHub is the process-wide fan-out for client-sync signals: every workspace's chat manager
// emits here (a chat had activity → badge it; the chat list changed → re-push the list), and
// every connected app client subscribes. It is the ONE producer side the appserver reads
// through Workspaces.WatchSync — the server never touches a manager, only this stream.
//
// Delivery is best-effort: a slow subscriber's buffered signal is dropped (a badge is a hint,
// a list-change self-heals on the next push / a reconnect re-list) — never durable state.
type syncHub struct {
	mu   sync.Mutex
	subs map[int]chan appserver.Sync
	next int
}

func newSyncHub() *syncHub {
	return &syncHub{subs: map[int]chan appserver.Sync{}}
}

// emitActivity badges one chat (a turn ended, an approval is pending).
func (h *syncHub) emitActivity(ws, id, kind string) {
	h.emit(appserver.Sync{Activity: &appserver.ChatActivity{WS: ws, ID: id, Kind: kind}})
}

// emitList marks a workspace domain's list as changed so every client re-pushes that full
// list (chats today; agents/reminders/settings/jobs later — same call, different domain).
func (h *syncHub) emitList(domain appserver.Domain, ws string) {
	h.emit(appserver.Sync{Domain: domain, WS: ws})
}

func (h *syncHub) emit(s appserver.Sync) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- s:
		default: // subscriber behind: drop — the next signal (or a reconnect re-sync) corrects it
		}
	}
}

// Watch registers a subscriber and returns its stream plus an unsubscribe that closes it (so
// the reader's range loop exits).
func (h *syncHub) Watch() (<-chan appserver.Sync, func()) {
	ch := make(chan appserver.Sync, 32)
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(ch)
		}
		h.mu.Unlock()
	}
}
