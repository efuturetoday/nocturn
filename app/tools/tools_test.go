package tools_test

import (
	"sort"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/tools"
)

// names returns the sorted tool names in a base slice.
func names(ts []agentkit.Tool) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Spec().Name)
	}
	sort.Strings(out)
	return out
}

// setNames returns the sorted tool names in a ToolSet (map).
func setNames(ts agentkit.ToolSet) []string {
	out := make([]string, 0, len(ts))
	for name := range ts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func has(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestBase_TogglesByConfig proves each Config field toggles exactly its tools: the always-present net
// tools + time_now, plus file tools only with a Root, notify only with a Notifier, wake only with a
// Waker.
func TestBase_TogglesByConfig(t *testing.T) {
	always := []string{"dns_resolve", "http_read", "http_write", "ping", "time_now"}
	fileTools := []string{"file_list", "file_move", "file_read", "file_remove", "file_search", "file_stat", "file_write"}

	cases := []struct {
		name string
		cfg  tools.Config
		want []string
	}{
		{
			name: "minimal (nil everything)",
			cfg:  tools.Config{},
			want: always,
		},
		{
			name: "with root adds file tools",
			cfg:  tools.Config{Root: t.TempDir()},
			want: append(append([]string{}, always...), fileTools...),
		},
		{
			name: "with notifier adds notify",
			cfg:  tools.Config{Notifier: &fakeNotifier{}},
			want: append(append([]string{}, always...), "notify"),
		},
		{
			name: "with waker adds wake",
			cfg:  tools.Config{Waker: tools.NewWaker()},
			want: append(append([]string{}, always...), "wake"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, err := tools.Base(tc.cfg)
			if err != nil {
				t.Fatalf("Base: %v", err)
			}
			got := names(ts)
			sort.Strings(tc.want)
			if len(got) != len(tc.want) {
				t.Fatalf("tool set = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("tool set = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestBase_AlwaysIncludesNetAndTime proves the network tools and time_now are present under any config,
// even the empty one (they carry no per-workspace dependency).
func TestBase_AlwaysIncludesNetAndTime(t *testing.T) {
	ts, err := tools.Base(tools.Config{})
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	got := names(ts)
	for _, want := range []string{"http_read", "http_write", "dns_resolve", "ping", "time_now"} {
		if !has(got, want) {
			t.Fatalf("Base is missing always-on tool %q; got %v", want, got)
		}
	}
}

// TestCompose_NoCodeRun_ReturnsCageUnchanged proves that without code_run Compose returns the cage
// verbatim — no interpreter is woven in.
func TestCompose_NoCodeRun_ReturnsCageUnchanged(t *testing.T) {
	base, err := tools.Base(tools.Config{})
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	cage, err := agentkit.NewToolSet(base...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	out, err := tools.Compose(cage, false)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(out) != len(cage) {
		t.Fatalf("Compose(false) changed the cage size: %d -> %d", len(cage), len(out))
	}
	if _, ok := out[tools.CodeRunTool]; ok {
		t.Fatal("Compose(false) added code_run")
	}
}

// TestCompose_CodeRun_AddsBoundedInterpreter proves Compose(true) adds exactly code_run to the cage and
// nothing else — the dispatch set the interpreter can reach IS the cage, so a script can never widen
// authority beyond it.
func TestCompose_CodeRun_AddsBoundedInterpreter(t *testing.T) {
	base, err := tools.Base(tools.Config{})
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	cage, err := agentkit.NewToolSet(base...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	out, err := tools.Compose(cage, true)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if _, ok := out[tools.CodeRunTool]; !ok {
		t.Fatal("Compose(true) did not add code_run")
	}
	// Exactly the cage plus code_run: no other tool appeared, and every cage tool survived.
	if len(out) != len(cage)+1 {
		t.Fatalf("Compose(true) set size = %d, want cage+1 = %d", len(out), len(cage)+1)
	}
	for _, name := range setNames(cage) {
		if _, ok := out[name]; !ok {
			t.Fatalf("Compose(true) dropped cage tool %q", name)
		}
	}
	// code_run is NOT part of its own dispatch set: reentry is refused by construction.
	if _, ok := cage[tools.CodeRunTool]; ok {
		t.Fatal("the cage handed to Compose already contained code_run (reentry surface)")
	}
}
