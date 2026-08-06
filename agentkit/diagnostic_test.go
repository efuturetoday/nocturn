package agentkit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestDiagnostics_ConcurrentFeeders(t *testing.T) {
	var d agentkit.Diagnostics
	const feeders, each = 8, 50
	var wg sync.WaitGroup
	for i := range feeders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				d.Warn("subject", "msg")
				_ = i
			}
		}()
	}
	wg.Wait()
	if got := d.Len(); got != feeders*each {
		t.Fatalf("Len = %d, want %d", got, feeders*each)
	}
	if len(d.All()) != feeders*each {
		t.Fatalf("All len = %d, want %d", len(d.All()), feeders*each)
	}
}

func TestDiagnostics_HasErrors(t *testing.T) {
	var d agentkit.Diagnostics
	d.Info("s", "i")
	d.Warn("s", "w")
	if d.HasErrors() {
		t.Fatal("HasErrors = true with only info/warn, want false")
	}
	d.Error("s", "e")
	if !d.HasErrors() {
		t.Fatal("HasErrors = false after an Error, want true")
	}
}

func TestDiagnose_NoCollector_NoOp(t *testing.T) {
	// Fail-open: no collector attached → no panic.
	agentkit.Diagnose(context.Background(), agentkit.Warn, "s", "m")
	if d := agentkit.DiagnosticsFrom(context.Background()); d != nil {
		t.Fatalf("DiagnosticsFrom(bg) = %v, want nil", d)
	}
}

func TestLevel_String(t *testing.T) {
	tests := []struct {
		level agentkit.Level
		want  string
	}{
		{agentkit.Info, "info"},
		{agentkit.Warn, "warn"},
		{agentkit.Error, "error"},
		{agentkit.Level(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Fatalf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}
