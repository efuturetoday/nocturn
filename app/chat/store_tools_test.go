package chat_test

import (
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/chat"
)

// The per-turn tool forest must persist beside the transcript, index-aligned to turns, and neither
// Save nor AppendTools may clobber the other's field (both do read-modify-write under the store lock).
func TestStore_AppendTools_IndexAlignedAndNoLostUpdate(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "abc123"

	// Turn 1: transcript then its forest group.
	turn1 := []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}, {Role: agentkit.RoleAssistant, Content: "ok"}}
	if err := st.Save(id, turn1); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendTools(id, []chat.ToolNode{{ID: 1, Tool: "code_run"}, {ID: 2, Parent: 1, Tool: "http_read"}}); err != nil {
		t.Fatal(err)
	}

	// Turn 2: a longer transcript (Save overwrites Messages) then a second group.
	turn2 := append(turn1, agentkit.Message{Role: agentkit.RoleUser, Content: "again"}, agentkit.Message{Role: agentkit.RoleAssistant, Content: "done"})
	if err := st.Save(id, turn2); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendTools(id, []chat.ToolNode{{ID: 3, Tool: "time_now"}}); err != nil {
		t.Fatal(err)
	}

	// Forest survived both Saves and stayed one group per turn, in order.
	forest, err := st.LoadTools(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(forest) != 2 {
		t.Fatalf("groups = %d, want 2", len(forest))
	}
	if len(forest[0]) != 2 || forest[0][1].Parent != 1 {
		t.Fatalf("group 0 = %+v, want [code_run, http_read(parent=1)]", forest[0])
	}
	if len(forest[1]) != 1 || forest[1][0].Tool != "time_now" {
		t.Fatalf("group 1 = %+v, want [time_now]", forest[1])
	}

	// Transcript survived the AppendTools writes (no lost update the other way).
	msgs, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != len(turn2) {
		t.Fatalf("messages = %d, want %d", len(msgs), len(turn2))
	}
}

// AppendTools on a chat with no transcript yet is a silent no-op (not an error, nothing written).
func TestStore_AppendTools_NoTranscript_NoOp(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendTools("dead", []chat.ToolNode{{ID: 1, Tool: "x"}}); err != nil {
		t.Fatalf("AppendTools on unknown chat = %v, want nil", err)
	}
	forest, err := st.LoadTools("dead")
	if err != nil {
		t.Fatal(err)
	}
	if forest != nil {
		t.Fatalf("forest = %+v, want nil", forest)
	}
}
