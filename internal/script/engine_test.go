package script_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// The shared interpreter engine is compiled once and reused, so per-call
// statelessness must hold across runs on the SAME engine: each Run gets a fresh
// module instance with a fresh QuickJS heap. Run 1 sets a global; run 2 must not
// see it.
func TestEngine_Isolation_NoStateAcrossRuns(t *testing.T) {
	r := script.New(tool.NewRegistry())
	ctx := context.Background()

	if _, err := r.Run(ctx, "globalThis.leak = 42;"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	out, err := r.Run(ctx, `print(typeof globalThis.leak);`)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if got := strings.TrimSpace(out); got != "undefined" {
		t.Fatalf("state leaked across runs: typeof globalThis.leak = %q, want \"undefined\"", got)
	}
}

// Concurrent runs on the shared engine must each get their own isolated instance —
// no shared heap, no crash, race-clean. Each run sets and prints its own value;
// every run must read back exactly its own. Run under -race.
func TestEngine_Isolation_Concurrent(t *testing.T) {
	r := script.New(tool.NewRegistry())
	const n = 16
	outs := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := fmt.Sprintf("globalThis.v = %d; print(globalThis.v);", i)
			outs[i], errs[i] = r.Run(context.Background(), src)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if got, want := strings.TrimSpace(outs[i]), strconv.Itoa(i); got != want {
			t.Errorf("run %d saw %q, want %q — heap shared across concurrent runs", i, got, want)
		}
	}
}
