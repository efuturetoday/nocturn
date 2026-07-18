package main

import (
	"sync"

	"github.com/efuturetoday/nocturn/internal/appserver"
)

// activityHub is the process-wide fan-out for background-chat activity: every workspace's
// chat manager emits a lightweight signal here (a turn ended, an approval is pending), and
// every connected app client subscribes to badge its chat list without polling. It is the
// PRODUCER side that the appserver reads through Workspaces.WatchActivity — the server never
// touches a manager, only this stream.
//
// Delivery is best-effort: a slow subscriber's buffered signal is dropped (a badge is a
// hint, not state — the client re-syncs the true counts with listChats when it opens a chat).
type activityHub struct {
	mu   sync.Mutex
	subs map[int]chan appserver.ChatActivity
	next int
}

func newActivityHub() *activityHub {
	return &activityHub{subs: map[int]chan appserver.ChatActivity{}}
}

// emit broadcasts one chat's activity to every current subscriber, dropping on any that
// can't keep up. Safe for concurrent callers (each workspace's manager pump calls it).
func (h *activityHub) emit(ws, id, kind string) {
	a := appserver.ChatActivity{WS: ws, ID: id, Kind: kind}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- a:
		default: // subscriber behind: drop — the badge is a hint, not durable state
		}
	}
}

// Watch registers a subscriber and returns its stream plus an unsubscribe that closes it
// (so the reader's range loop exits). It mirrors chat.Chat.Subscribe's shape.
func (h *activityHub) Watch() (<-chan appserver.ChatActivity, func()) {
	ch := make(chan appserver.ChatActivity, 32)
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
