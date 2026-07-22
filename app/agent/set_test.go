package agent_test

import (
	"testing"

	"github.com/efuturetoday/nocturn/app/agent"
)

func TestSet_Get(t *testing.T) {
	t.Parallel()

	set := agent.Set{
		"alice": {Name: "alice"},
		"bob":   {Name: "bob"},
	}

	if got, ok := set.Get("alice"); !ok || got.Name != "alice" {
		t.Errorf("Get(alice) = (%+v, %v), want alice present", got, ok)
	}
	if got, ok := set.Get("nobody"); ok {
		t.Errorf("Get(nobody) = (%+v, %v), want absent", got, ok)
	}
}

func TestSet_All_SortedByName(t *testing.T) {
	t.Parallel()

	set := agent.Set{
		"charlie": {Name: "charlie"},
		"alice":   {Name: "alice"},
		"bob":     {Name: "bob"},
	}

	all := set.All()
	want := []string{"alice", "bob", "charlie"}
	if len(all) != len(want) {
		t.Fatalf("All() len = %d, want %d", len(all), len(want))
	}
	for i, name := range want {
		if all[i].Name != name {
			t.Errorf("All()[%d].Name = %q, want %q (order: %v)", i, all[i].Name, name, names(all))
		}
	}
}

func TestSet_All_Empty(t *testing.T) {
	t.Parallel()

	if all := (agent.Set{}).All(); len(all) != 0 {
		t.Errorf("empty Set All() = %v, want empty", all)
	}
}

func names(agents []agent.Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.Name
	}
	return out
}
