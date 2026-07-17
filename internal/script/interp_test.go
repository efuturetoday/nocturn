package script_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Shell A: a real interpreter on the sandbox evaluates arbitrary JS source and
// its stdout comes back. Pure compute, zero capabilities.
func TestInterp_EvalPureCompute(t *testing.T) {
	r := script.New(nil)

	out, err := r.Run(context.Background(), `console.log(2 + 2)`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "4" {
		t.Fatalf("stdout = %q, want 4", out)
	}
}

// A broader slice of the language works (not just arithmetic): JSON, arrays,
// template strings — proving it is a genuine interpreter, not a toy.
func TestInterp_LanguageSurface(t *testing.T) {
	r := script.New(nil)

	src := `
		const xs = [1, 2, 3].map(n => n * n);
		console.log(JSON.stringify({sum: xs.reduce((a, b) => a + b, 0), xs}));
	`
	out, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != `{"sum":14,"xs":[1,4,9]}` {
		t.Fatalf("stdout = %q", out)
	}
}

// Shell B through the REAL interpreter: a script calls nocturn.call(tool, args),
// the gate dispatches to the tool's Invoke, and the result returns into JS.
func TestInterp_GateReachesTool(t *testing.T) {
	var gotArgs string
	r := script.New(tool.NewRegistry().AddMany([]tool.Tool{recordingTool("greet", "hello from host", &gotArgs)}...))

	src := `
		const r = nocturn.call("greet", {name: "nocturn"});
		console.log(r.toUpperCase());
	`
	out, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "HELLO FROM HOST" {
		t.Fatalf("stdout = %q", out)
	}
	if gotArgs != `{"name":"nocturn"}` {
		t.Fatalf("tool received args %q", gotArgs)
	}
}

// A denied/failed effect surfaces as a catchable JS exception — the host does
// not crash and the script keeps control.
func TestInterp_DeniedEffectIsCatchable(t *testing.T) {
	deny := tool.Tool{
		Spec:   tool.Spec{Name: "http.write"},
		Invoke: func(context.Context, string) (string, error) { return "", errDenied },
	}
	r := script.New(tool.NewRegistry().AddMany([]tool.Tool{deny}...))

	src := `
		try {
			nocturn.call("http.write", {url: "http://evil"});
			console.log("REACHED");
		} catch (e) {
			console.log("caught: " + e.message);
		}
	`
	out, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "caught: ") || strings.Contains(out, "REACHED") {
		t.Fatalf("stdout = %q, want the denial caught in JS", out)
	}
}

// Models naturally write `await nocturn.call(...)` in loops and async IIFEs.
// The interpreter evals as async global code (top-level await is legal) and
// drives the job queue to completion, so awaited continuations actually run.
func TestInterp_TopLevelAwaitAndLoop(t *testing.T) {
	var calls int
	dnsTool := tool.Tool{
		Spec: tool.Spec{Name: "dns.resolve"},
		Invoke: func(_ context.Context, _ string) (string, error) {
			calls++
			return "1.2.3.4", nil
		},
	}
	r := script.New(tool.NewRegistry().AddMany([]tool.Tool{dnsTool}...))

	// The exact shape from the failing session: a top-level await loop.
	src := `
		const hosts = ["google.com", "wikipedia.org", "github.com"];
		for (const host of hosts) {
			const res = await nocturn.call("dns.resolve", { host });
			console.log(host + " -> " + res);
		}
	`
	out, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 3 {
		t.Fatalf("tool called %d times, want 3", calls)
	}
	for _, h := range []string{"google.com -> 1.2.3.4", "wikipedia.org -> 1.2.3.4", "github.com -> 1.2.3.4"} {
		if !strings.Contains(out, h) {
			t.Fatalf("stdout %q missing %q", out, h)
		}
	}
}

// Promise machinery (.then) also runs — the job queue is pumped, not dropped.
func TestInterp_PromiseThenRuns(t *testing.T) {
	r := script.New(nil)

	out, err := r.Run(context.Background(), `Promise.resolve(21).then(n => console.log(n * 2));`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "42" {
		t.Fatalf("stdout = %q, want 42", out)
	}
}

// A rejected top-level promise (an await that throws and isn't caught) surfaces
// as a run error, not a silent no-op.
func TestInterp_UnhandledRejectionIsError(t *testing.T) {
	deny := tool.Tool{
		Spec:   tool.Spec{Name: "dns.resolve"},
		Invoke: func(context.Context, string) (string, error) { return "", errDenied },
	}
	r := script.New(tool.NewRegistry().AddMany([]tool.Tool{deny}...))

	// No try/catch: the rejection propagates to the top-level promise.
	_, err := r.Run(context.Background(), `await nocturn.call("dns.resolve", {host: "x"});`)
	if err == nil {
		t.Fatal("expected an error for an unhandled rejection, got nil")
	}
}

// A runaway script is trapped by the sandbox's wall-clock deadline (wazero has
// no fuel — CPU is bounded by the deadline + memory cap).
func TestInterp_RunawayTrapped(t *testing.T) {
	r := script.New(nil)
	r.Timeout = 300 * time.Millisecond

	_, err := r.Run(context.Background(), `while (true) {}`)
	if err == nil {
		t.Fatal("expected the runaway script to be trapped, got nil error")
	}
}

var errDenied = &denyErr{}

type denyErr struct{}

func (*denyErr) Error() string { return "gateway: request denied" }
