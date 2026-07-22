package serve

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// fakeLLM answers every turn immediately — enough to Open a real workspace without a live endpoint.
// The read paths under test (open/list/markRead/cancel on a fresh workspace) never run a turn, so
// Next is never actually called here.
type fakeLLM struct{}

func (fakeLLM) Next(_ context.Context, _ []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	return agentkit.Step{Answer: "ok"}, nil
}

// openWorkspace opens a real, empty workspace on a temp dir — its manager/store are real, so the
// handlers exercise genuine delegation. It is closed on cleanup.
func openWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	h := workspace.Host{LLM: fakeLLM{}, Log: slog.New(slog.DiscardHandler)}
	ws, err := workspace.Open(h, "main", t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	t.Cleanup(ws.Chats().CloseAll)
	return ws
}

// connWith builds a conn whose single workspace "main" is ws.
func connWith(ws *workspace.Workspace) *conn {
	c := testConn()
	c.spaces["main"] = ws
	return c
}

// ── chatEvent: every agentkit event maps to its tagged wire form ─────────────────────────────────

func TestChatEvent_TagsChatID_AllVariants(t *testing.T) {
	const id = "abc123"

	t.Run("TurnStart", func(t *testing.T) {
		msg, ok := chatEvent(id, agentkit.TurnStart{Frame: 0})
		if !ok {
			t.Fatal("ok = false, want true")
		}
		got, isType := msg.(ChatTurnStart)
		if !isType {
			t.Fatalf("msg = %T, want ChatTurnStart", msg)
		}
		if got.Type != "chat.turnStart" || got.ChatID != id {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("Token", func(t *testing.T) {
		msg, ok := chatEvent(id, agentkit.Token{Frame: 2, Text: "hello"})
		if !ok {
			t.Fatal("ok = false")
		}
		got := msg.(ChatToken)
		if got.Type != "chat.token" || got.ChatID != id || got.Frame != 2 || got.Text != "hello" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("Thinking", func(t *testing.T) {
		msg, ok := chatEvent(id, agentkit.Thinking{Frame: 1, Text: "hmm"})
		if !ok {
			t.Fatal("ok = false")
		}
		got := msg.(ChatThinking)
		if got.Type != "chat.thinking" || got.ChatID != id || got.Text != "hmm" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("ToolStart carries no result/err/duration", func(t *testing.T) {
		msg, ok := chatEvent(id, agentkit.ToolStart{Frame: 0, ID: 5, Tool: "http_read", Args: `{"url":"x"}`})
		if !ok {
			t.Fatal("ok = false")
		}
		got := msg.(ChatTool)
		if got.Phase != "start" || got.ID != 5 || got.Tool != "http_read" || got.Args != `{"url":"x"}` {
			t.Errorf("got %+v", got)
		}
		if got.Result != "" || got.Err != "" || got.DurationMs != 0 {
			t.Errorf("start must not carry result/err/duration, got %+v", got)
		}
	})

	t.Run("ToolEnd carries result/err/duration", func(t *testing.T) {
		msg, ok := chatEvent(id, agentkit.ToolEnd{
			Frame: 0, ID: 5, Tool: "http_read", Args: "a",
			Result: "body", Err: errors.New("boom"), Duration: 1500 * time.Millisecond,
		})
		if !ok {
			t.Fatal("ok = false")
		}
		got := msg.(ChatTool)
		if got.Phase != "end" || got.Result != "body" || got.Err != "boom" || got.DurationMs != 1500 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("TurnEnd carries tokens and err", func(t *testing.T) {
		msg, ok := chatEvent(id, agentkit.TurnEnd{Frame: 0, Err: errors.New("stopped"), Tokens: agentkit.TokenCount{Total: 42}})
		if !ok {
			t.Fatal("ok = false")
		}
		got := msg.(ChatTurnEnd)
		if got.Type != "chat.turnEnd" || got.ChatID != id || got.Err != "stopped" || got.Tokens != 42 {
			t.Errorf("got %+v", got)
		}
	})
}

// A nil error on ToolEnd/TurnEnd renders as an empty Err string (errText), not "<nil>".
func TestChatEvent_NilErr_EmptyString(t *testing.T) {
	msg, _ := chatEvent("x", agentkit.ToolEnd{ID: 1, Tool: "t"})
	if got := msg.(ChatTool).Err; got != "" {
		t.Errorf("Err = %q, want empty for a nil error", got)
	}
	msg, _ = chatEvent("x", agentkit.TurnEnd{})
	if got := msg.(ChatTurnEnd).Err; got != "" {
		t.Errorf("Err = %q, want empty for a nil error", got)
	}
}

// ── chat dispatch: malformed and unknown commands ────────────────────────────────────────────────

func TestChatDispatch_BadJSON_PerCommand(t *testing.T) {
	cmds := []string{"chat.submit", "chat.open", "chat.list", "chat.cancel", "chat.markRead"}
	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			c := testConn()
			c.chat(context.Background(), cmd, []byte(`{not json`))
			recvError(t, c, "bad "+cmd)
		})
	}
}

func TestChat_UnknownAction_Error(t *testing.T) {
	c := testConn()
	c.chat(context.Background(), "chat.bogus", []byte(`{"cmd":"chat.bogus"}`))
	recvError(t, c, "unknown action")
}

// ── chatSubmit: client-minted id is validated before it is ever a filesystem key ─────────────────

func TestChatSubmit_InvalidID_SendsBadChatID_NoSubmit(t *testing.T) {
	c := testConn()
	c.spaces["main"] = &workspace.Workspace{} // reached but never dereferenced: invalid id returns first
	c.chatSubmit(context.Background(), ChatSubmit{Ws: "main", ID: "../evil", Text: "hi"})
	recvError(t, c, "bad chat id")
}

func TestChatSubmit_UnknownWorkspace_Errors(t *testing.T) {
	c := testConn() // empty spaces
	c.chatSubmit(context.Background(), ChatSubmit{Ws: "ghost", ID: "abc", Text: "hi"})
	recvError(t, c, "unknown workspace")
}

// ── chatOpen / chatList / markRead / cancel against a real, empty workspace ───────────────────────

// An idle (unknown) chat opens to a snapshot whose Messages and Tools are empty ARRAYS (not null) and
// whose Inflight is nil — the reopen-mid-turn state only appears for a running turn.
func TestChatOpen_IdleSnapshot_EmptyArrays_NilInflight(t *testing.T) {
	c := connWith(openWorkspace(t))
	c.chatOpen(context.Background(), ChatOpen{Ws: "main", ID: "deadbeef"})

	snap, ok := recv(t, c).(ChatSnapshot)
	if !ok {
		t.Fatalf("want ChatSnapshot")
	}
	if snap.Type != "chat.snapshot" || snap.ID != "deadbeef" {
		t.Errorf("got %+v", snap)
	}
	if snap.Messages == nil || len(snap.Messages) != 0 {
		t.Errorf("Messages = %v, want non-nil empty", snap.Messages)
	}
	if snap.Tools == nil || len(snap.Tools) != 0 {
		t.Errorf("Tools = %v, want non-nil empty", snap.Tools)
	}
	if snap.Inflight != nil {
		t.Errorf("Inflight = %+v, want nil for an idle chat", snap.Inflight)
	}

	// The wire form carries [] not null.
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !contains(s, `"messages":[]`) || !contains(s, `"tools":[]`) {
		t.Errorf("wire = %s, want messages:[] and tools:[]", s)
	}
	if contains(string(b), `"inflight"`) {
		t.Errorf("idle snapshot must omit inflight, wire = %s", b)
	}
}

func TestChatOpen_UnknownWorkspace_Errors(t *testing.T) {
	c := testConn()
	c.chatOpen(context.Background(), ChatOpen{Ws: "ghost", ID: "abc"})
	recvError(t, c, "unknown workspace")
}

func TestChatList_EmptyWorkspace_EmptyList(t *testing.T) {
	c := connWith(openWorkspace(t))
	c.chatList(context.Background(), "main")
	res, ok := recv(t, c).(ChatListResult)
	if !ok {
		t.Fatalf("want ChatListResult")
	}
	if res.Type != "chat.list" || res.Ws != "main" {
		t.Errorf("got %+v", res)
	}
	if res.Chats == nil || len(res.Chats) != 0 {
		t.Errorf("Chats = %v, want non-nil empty", res.Chats)
	}
}

// markRead / cancel delegate to the workspace/manager; for an unknown chat both are silent no-ops
// (no panic, no message).
func TestChatMarkRead_UnknownChat_NoOp(t *testing.T) {
	ws := openWorkspace(t)
	c := connWith(ws)
	c.chat(context.Background(), "chat.markRead", []byte(`{"cmd":"chat.markRead","ws":"main","id":"deadbeef"}`))
	assertNoMessage(t, c)
}

func TestChatCancel_UnknownChat_NoOp(t *testing.T) {
	ws := openWorkspace(t)
	c := connWith(ws)
	c.chat(context.Background(), "chat.cancel", []byte(`{"cmd":"chat.cancel","ws":"main","id":"deadbeef"}`))
	assertNoMessage(t, c)
}

func assertNoMessage(t *testing.T, c *conn) {
	t.Helper()
	select {
	case msg := <-c.out:
		t.Fatalf("expected no message, got %v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}
