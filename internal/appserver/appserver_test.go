package appserver_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/chat"
)

// fakeRunner is a test double for a workspace's turn loop: a controllable event channel,
// a fixed snapshot, and a record of the commands that reached it.
type fakeRunner struct {
	events chan chat.Event
	snap   chat.Snapshot

	mu      sync.Mutex
	submits []string
}

func (f *fakeRunner) Subscribe() (<-chan chat.Event, func()) { return f.events, func() {} }
func (f *fakeRunner) Snapshot() chat.Snapshot                { return f.snap }
func (f *fakeRunner) Submit(_ chat.Source, input string) {
	f.mu.Lock()
	f.submits = append(f.submits, input)
	f.mu.Unlock()
}
func (f *fakeRunner) SubmitInput(_ chat.Source, _, _ string) {}
func (f *fakeRunner) SubmitAgent(_, _, _ string)             {}
func (f *fakeRunner) Cancel()                                {}
func (f *fakeRunner) Reset()                                 {}
func (f *fakeRunner) Resolve(string, int)                    {}
func (f *fakeRunner) gotSubmit(input string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.submits, input)
}

// fakeWorkspaces is a test double for the state service — it never touches a filesystem,
// exactly as the real one must not. It holds one workspace ("work") and records writes.
type fakeWorkspaces struct {
	runner *fakeRunner
	syncs  chan appserver.Sync // client-sync signals the test can push onto

	mu      sync.Mutex
	persona string
	revoked []string
}

func (w *fakeWorkspaces) getPersona() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.persona
}
func (w *fakeWorkspaces) List() []appserver.WorkspaceSummary {
	return []appserver.WorkspaceSummary{{Name: "work", Running: true, Agents: 2, Skills: 3, PersonaSet: w.getPersona() != ""}}
}
func (w *fakeWorkspaces) Get(name string) (appserver.WorkspaceState, bool) {
	if name != "work" {
		return appserver.WorkspaceState{}, false
	}
	return appserver.WorkspaceState{
		Name:    "work",
		Persona: w.getPersona(),
		Agents:  []appserver.AgentInfo{{Name: "researcher", Description: "digs"}},
	}, true
}
func (w *fakeWorkspaces) SetPersona(name, text string) error {
	w.mu.Lock()
	w.persona = text
	w.mu.Unlock()
	return nil
}

// The fake models one chat "c1" whose live runner is the shared fakeRunner.
func (w *fakeWorkspaces) Chats(ws string) ([]appserver.ChatMeta, bool) {
	if ws != "work" {
		return nil, false
	}
	return []appserver.ChatMeta{{ID: "c1", Name: "first", Updated: "2026-07-18T00:00:00Z", Turns: 1}}, true
}
func (w *fakeWorkspaces) NewChat(ws, name string) (appserver.ChatMeta, bool) {
	if ws != "work" {
		return appserver.ChatMeta{}, false
	}
	return appserver.ChatMeta{ID: "c2", Name: name}, true
}
func (w *fakeWorkspaces) OpenChat(ws, id string) (appserver.Runner, bool) {
	if ws != "work" || id != "c1" {
		return nil, false
	}
	return w.runner, true
}
func (w *fakeWorkspaces) RenameChat(ws, id, name string) bool { return ws == "work" }
func (w *fakeWorkspaces) DeleteChat(ws, id string) bool       { return ws == "work" }
func (w *fakeWorkspaces) Reminders(ws string) ([]appserver.ReminderMeta, bool) {
	if ws != "work" {
		return nil, false
	}
	return []appserver.ReminderMeta{{ID: "r1", FireAt: "2026-07-19T09:00:00Z", Message: "standup"}}, true
}
func (w *fakeWorkspaces) WatchSync() (<-chan appserver.Sync, func()) {
	if w.syncs == nil {
		w.syncs = make(chan appserver.Sync, 8)
	}
	return w.syncs, func() {}
}
func (w *fakeWorkspaces) OpenJoins() []appserver.JoinMeta {
	return []appserver.JoinMeta{{JoinID: "j1", Name: "iPad", Code: "4821", Platform: "ios"}}
}

func (w *fakeWorkspaces) Devices() []appserver.DeviceMeta {
	return []appserver.DeviceMeta{{ID: "d1", Name: "iPhone", Platform: "ios", Added: "2026-07-19T00:00:00Z", HasPush: true}}
}

func (w *fakeWorkspaces) RevokeDevice(id string) bool {
	w.mu.Lock()
	w.revoked = append(w.revoked, id)
	w.mu.Unlock()
	return id == "d1"
}

// fakeConn scripts client→server messages (in) and captures server→client messages (out).
type fakeConn struct {
	in     chan []byte
	out    chan []byte
	closed chan struct{}
	once   sync.Once
}

func (c *fakeConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case m := <-c.in:
		return m, nil
	case <-c.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *fakeConn) Write(ctx context.Context, msg []byte) error {
	select {
	case c.out <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func newConn() *fakeConn {
	return &fakeConn{in: make(chan []byte, 8), out: make(chan []byte, 32), closed: make(chan struct{})}
}

// recvUntil reads messages until one has the wanted `type`, skipping interleaved others
// (control replies and chat events share the one outbound channel).
func recvUntil(t *testing.T, out <-chan []byte, typ string) map[string]any {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m := <-out:
			var v map[string]any
			if err := json.Unmarshal(m, &v); err != nil {
				t.Fatalf("bad json on conn: %v (%s)", err, m)
			}
			if v["type"] == typ {
				return v
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %q message", typ)
			return nil
		}
	}
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The server multiplexes the control plane (list/get/setPersona) and one open chat
// (snapshot + streamed events + routed commands) over one connection.
func TestServer_ControlPlaneAndChat(t *testing.T) {
	fr := &fakeRunner{events: make(chan chat.Event, 8), snap: chat.Snapshot{Running: true}}
	fw := &fakeWorkspaces{runner: fr, persona: "You are helpful."}
	fc := newConn()
	go func() { _ = appserver.NewServer(fw, appserver.NewPresence()).Handle(t.Context(), fc, "") }()

	// Control: list workspaces.
	fc.in <- []byte(`{"cmd":"listWorkspaces"}`)
	ws := recvUntil(t, fc.out, "workspaces")
	if items, _ := ws["items"].([]any); len(items) != 1 {
		t.Fatalf("workspaces = %v, want one item", ws["items"])
	}

	// Open a chat → its snapshot arrives.
	fc.in <- []byte(`{"cmd":"openChat","ws":"work","id":"c1"}`)
	if snap := recvUntil(t, fc.out, "snapshot"); snap["running"] != true {
		t.Fatalf("snapshot = %v, want running:true", snap)
	}

	// The open chat's events stream.
	fr.events <- chat.TokenEvent{Text: "hi"}
	if tok := recvUntil(t, fc.out, "token"); tok["text"] != "hi" {
		t.Fatalf("event = %v, want token(hi)", tok)
	}

	// A chat command routes to the OPEN chat's runner.
	fc.in <- []byte(`{"cmd":"submit","input":"hello"}`)
	eventually(t, func() bool { return fr.gotSubmit("hello") }, "submit did not reach the open runner")

	// Control: get the workspace detail (persona + agents).
	fc.in <- []byte(`{"cmd":"getWorkspace","ws":"work"}`)
	if got := recvUntil(t, fc.out, "workspace"); got["persona"] != "You are helpful." {
		t.Fatalf("workspace = %v, want persona 'You are helpful.'", got)
	}

	// Control: set the persona → the service is written and the new state is echoed.
	fc.in <- []byte(`{"cmd":"setPersona","ws":"work","text":"New persona."}`)
	if echoed := recvUntil(t, fc.out, "workspace"); echoed["persona"] != "New persona." {
		t.Fatalf("echoed workspace = %v, want the new persona", echoed)
	}
	if fw.getPersona() != "New persona." {
		t.Fatalf("service persona = %q, want it persisted", fw.getPersona())
	}
}

// Background-chat activity (a turn ended / an approval is pending in a chat the client has
// NOT opened) reaches the client as a lightweight chatActivity badge over the same conn.
func TestServer_BackgroundChatActivity(t *testing.T) {
	fw := &fakeWorkspaces{
		runner: &fakeRunner{events: make(chan chat.Event, 1)},
		syncs:  make(chan appserver.Sync, 8),
	}
	fc := newConn()
	go func() { _ = appserver.NewServer(fw, appserver.NewPresence()).Handle(t.Context(), fc, "") }()

	// A background chat in "work" finishes a turn — the manager would emit this badge.
	fw.syncs <- appserver.Sync{Activity: &appserver.ChatActivity{WS: "work", ID: "c9", Kind: appserver.ActivityTurnEnd}}

	got := recvUntil(t, fc.out, "chatActivity")
	if got["ws"] != "work" || got["id"] != "c9" || got["kind"] != appserver.ActivityTurnEnd {
		t.Fatalf("chatActivity = %v, want ws=work id=c9 kind=turnEnd", got)
	}
}

// A coarse chat-list change (Sync with a Domain) pushes that workspace's full chats list to
// the client unsolicited — no re-list round-trip.
func TestServer_ChatsListPush(t *testing.T) {
	fw := &fakeWorkspaces{
		runner: &fakeRunner{events: make(chan chat.Event, 1)},
		syncs:  make(chan appserver.Sync, 8),
	}
	fc := newConn()
	go func() { _ = appserver.NewServer(fw, appserver.NewPresence()).Handle(t.Context(), fc, "") }()

	// A chat was created/renamed/deleted in "work" — the manager would emit this.
	fw.syncs <- appserver.Sync{Domain: appserver.DomainChats, WS: "work"}

	got := recvUntil(t, fc.out, "chats")
	if got["ws"] != "work" {
		t.Fatalf("chats push = %v, want ws=work", got)
	}
	if items, _ := got["items"].([]any); len(items) == 0 {
		t.Fatalf("chats push carried no items: %v", got)
	}
}

// A reminder set/fired/cancelled (Sync with DomainReminders) pushes the workspace's full
// pending-reminder list to the client unsolicited — same coarse bus as chats.
func TestServer_RemindersListPush(t *testing.T) {
	fw := &fakeWorkspaces{
		runner: &fakeRunner{events: make(chan chat.Event, 1)},
		syncs:  make(chan appserver.Sync, 8),
	}
	fc := newConn()
	go func() { _ = appserver.NewServer(fw, appserver.NewPresence()).Handle(t.Context(), fc, "") }()

	fw.syncs <- appserver.Sync{Domain: appserver.DomainReminders, WS: "work"}

	got := recvUntil(t, fc.out, "reminders")
	if got["ws"] != "work" {
		t.Fatalf("reminders push = %v, want ws=work", got)
	}
	if items, _ := got["items"].([]any); len(items) == 0 {
		t.Fatalf("reminders push carried no items: %v", got)
	}
}

// Opening an unknown workspace replies with an error, not a snapshot — and keeps the
// connection alive.
func TestServer_UnknownWorkspaceErrors(t *testing.T) {
	fw := &fakeWorkspaces{runner: &fakeRunner{events: make(chan chat.Event, 1)}}
	fc := newConn()
	go func() { _ = appserver.NewServer(fw, appserver.NewPresence()).Handle(t.Context(), fc, "") }()

	fc.in <- []byte(`{"cmd":"getWorkspace","ws":"nope"}`)
	if e := recvUntil(t, fc.out, "error"); !strings.Contains(e["text"].(string), "unknown workspace") {
		t.Fatalf("error = %v, want 'unknown workspace'", e)
	}

	// still alive: a following list works.
	fc.in <- []byte(`{"cmd":"listWorkspaces"}`)
	recvUntil(t, fc.out, "workspaces")
}

// A connection is foreground-active on connect; setPresence toggles it in the shared tracker
// (which the approval router reads to choose WebSocket vs push), and a disconnect leaves the set.
func TestServer_Presence(t *testing.T) {
	fw := &fakeWorkspaces{runner: &fakeRunner{events: make(chan chat.Event, 1)}}
	fc := newConn()
	p := appserver.NewPresence()
	go func() { _ = appserver.NewServer(fw, p).Handle(t.Context(), fc, "") }()

	eventually(t, p.Active, "a fresh connection should be foreground-active")

	fc.in <- []byte(`{"cmd":"setPresence","active":false}`)
	eventually(t, func() bool { return !p.Active() }, "setPresence:false should clear active")

	fc.in <- []byte(`{"cmd":"setPresence","active":true}`)
	eventually(t, p.Active, "setPresence:true should restore active")

	fc.Close()
	eventually(t, func() bool { return !p.Active() }, "disconnect should leave the active set")
}

// A join change (Sync with DomainJoins) pushes the pending-join list (with codes) to the client
// unsolicited; listJoins is the explicit pull. Same coarse bus as chats/reminders.
func TestServer_JoinsPush(t *testing.T) {
	fw := &fakeWorkspaces{
		runner: &fakeRunner{events: make(chan chat.Event, 1)},
		syncs:  make(chan appserver.Sync, 8),
	}
	fc := newConn()
	go func() { _ = appserver.NewServer(fw, appserver.NewPresence()).Handle(t.Context(), fc, "") }()

	fw.syncs <- appserver.Sync{Domain: appserver.DomainJoins}
	got := recvUntil(t, fc.out, "joins")
	if items, _ := got["items"].([]any); len(items) != 1 {
		t.Fatalf("joins push items = %v, want one", got["items"])
	}

	fc.in <- []byte(`{"cmd":"listJoins"}`)
	recvUntil(t, fc.out, "joins")
}

// The paired-device list is pushed on a DomainDevices change and pulled via listDevices; a
// revokeDevice command routes to the state service. No secret ever crosses the wire.
func TestServer_DevicesAndRevoke(t *testing.T) {
	fw := &fakeWorkspaces{
		runner: &fakeRunner{events: make(chan chat.Event, 1)},
		syncs:  make(chan appserver.Sync, 8),
	}
	fc := newConn()
	// This connection authenticated as device "d1", so its own device is flagged self.
	go func() { _ = appserver.NewServer(fw, appserver.NewPresence()).Handle(t.Context(), fc, "d1") }()

	fw.syncs <- appserver.Sync{Domain: appserver.DomainDevices}
	got := recvUntil(t, fc.out, "devices")
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("devices push items = %v, want one", got["items"])
	}
	d0, _ := items[0].(map[string]any)
	if d0["bearer"] != nil || d0["id"] != "d1" {
		t.Fatalf("device meta = %v, want id=d1 and no bearer", items[0])
	}
	if d0["self"] != true {
		t.Fatalf("device meta self = %v, want true (the connection's own device)", d0["self"])
	}

	fc.in <- []byte(`{"cmd":"listDevices"}`)
	recvUntil(t, fc.out, "devices")

	fc.in <- []byte(`{"cmd":"revokeDevice","id":"d1"}`)
	eventually(t, func() bool {
		fw.mu.Lock()
		defer fw.mu.Unlock()
		return slices.Contains(fw.revoked, "d1")
	}, "revokeDevice did not reach the state service")
}

// Revoke closes the live connections of the revoked device (its bearer stops working at once,
// not just on the next reconnect).
func TestServer_RevokeDropsConnection(t *testing.T) {
	fw := &fakeWorkspaces{runner: &fakeRunner{events: make(chan chat.Event, 1)}}
	fc := newConn()
	srv := appserver.NewServer(fw, appserver.NewPresence())
	done := make(chan struct{})
	go func() { _ = srv.Handle(t.Context(), fc, "d1"); close(done) }()

	// Let the connection register, then revoke its device.
	fc.in <- []byte(`{"cmd":"listWorkspaces"}`)
	recvUntil(t, fc.out, "workspaces")
	srv.Revoke("d1")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Revoke did not end the revoked device's connection")
	}
}
