package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
)

// Manager starts and resumes chats over a Store, using a Runtime to build each session (shared LLM,
// tools, gating, defaults). It is deliberately thin: the per-turn metadata bump lives in Store.Save,
// so a Manager only mints ids and hands out sessions. Live-session lifecycle (closing on switch) is
// the caller's — the terminal keeps one active chat.
type Manager struct {
	rt    *runtime.Runtime
	store *Store
}

// NewManager builds a Manager over a Runtime and a Store.
func NewManager(rt *runtime.Runtime, store *Store) *Manager {
	return &Manager{rt: rt, store: store}
}

// Start begins a new chat from its first message: it mints an id, builds a session bound to that id,
// and submits the text. The chat's file and name appear when the turn saves.
func (m *Manager) Start(ctx context.Context, text string) (string, *agentkit.Session) {
	id := NewID()
	sess := m.session(ctx, id)
	sess.Submit(text)
	return id, sess
}

// Open resumes an existing chat: a session bound to id, its transcript loaded from the store.
func (m *Manager) Open(ctx context.Context, id string) *agentkit.Session {
	return m.session(ctx, id)
}

func (m *Manager) session(ctx context.Context, id string) *agentkit.Session {
	return m.rt.Session(ctx, agentkit.WithStore(m.store, id))
}

// List returns every chat's metadata, most recent first.
func (m *Manager) List() ([]Meta, error) { return m.store.Metas() }

// Transcript returns a chat's persisted messages, for rendering a snapshot on open.
func (m *Manager) Transcript(id string) ([]agentkit.Message, error) { return m.store.Load(id) }

// Delete removes a chat's transcript.
func (m *Manager) Delete(id string) error { return m.store.Delete(id) }

// NewID mints a short random chat id.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
