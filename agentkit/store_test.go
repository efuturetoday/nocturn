package agentkit_test

import (
	"reflect"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestMemStore_SaveLoad_CopySemantics(t *testing.T) {
	store := agentkit.NewMemStore()
	orig := []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}
	if err := store.Save("id", orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Mutating the caller's slice must not touch stored state.
	orig[0].Content = "mutated"

	got, err := store.Load("id")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Content != "hi" {
		t.Fatalf("Load = %+v, want the copy {user hi}", got)
	}
	// Mutating the loaded copy must not touch stored state either.
	got[0].Content = "changed"
	again, _ := store.Load("id")
	if again[0].Content != "hi" {
		t.Fatalf("second Load = %q, want hi (Load returns a fresh copy)", again[0].Content)
	}
}

func TestMemStore_Load_UnknownID_NilNoError(t *testing.T) {
	got, err := agentkit.NewMemStore().Load("nope")
	if err != nil || got != nil {
		t.Fatalf("Load(unknown) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestMemStore_List_Sorted(t *testing.T) {
	store := agentkit.NewMemStore()
	for _, id := range []string{"c", "a", "b"} {
		if err := store.Save(id, nil); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("List = %v, want %v", ids, want)
	}
}

// (approxTokenizer/nopLogger are unexported so their concrete asserts live in production code;
// gate.Grants/MemGrants sit in a separate module. MemStore is the externally-assertable one.)

var _ agentkit.Store = (*agentkit.MemStore)(nil)
