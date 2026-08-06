package script_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/script"
)

// recordingTool is a fake agentkit.Tool that captures the args it was called with
// and returns a fixed output — enough to prove the gate dispatches correctly.
func recordingTool(t *testing.T, name, out string, gotArgs *string) agentkit.Tool {
	t.Helper()
	tl, err := agentkit.NewTool(name, "desc",
		func(_ context.Context, args string) (string, error) {
			if gotArgs != nil {
				*gotArgs = args
			}
			return out, nil
		})
	if err != nil {
		t.Fatalf("NewTool(%q): %v", name, err)
	}
	return tl
}

// toolset builds an agentkit.ToolSet from the given tools, failing the test on a
// bad/colliding spec.
func toolset(t *testing.T, tools ...agentkit.Tool) agentkit.ToolSet {
	t.Helper()
	ts, err := agentkit.NewToolSet(tools...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	return ts
}

// requireInterpreter skips the test if the embedded QuickJS interpreter cannot be
// compiled (a build-environment problem, not a behavior under test).
func requireInterpreter(t *testing.T) {
	t.Helper()
	if _, err := script.Engine(); err != nil {
		t.Skipf("QuickJS interpreter engine unavailable: %v", err)
	}
}

// Run returns exactly what the script printed to stdout.
func TestRunner_Run_ReturnsStdout(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(), `console.log("hello from js")`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "hello from js" {
		t.Fatalf("stdout = %q, want %q", out, "hello from js")
	}
}

// An uncaught top-level throw exits non-zero; Run returns an error carrying the
// guest's stderr, so the failure reason is not swallowed.
func TestRunner_Run_TrapSurfacesStderr(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(), `throw new Error("kaboom");`)
	if err == nil {
		t.Fatalf("expected an error for an uncaught throw, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("err = %v, want it to carry the guest stderr (kaboom)", err)
	}
}

// A runaway script is trapped by the sandbox's wall-clock deadline (wazero has no
// fuel — CPU is bounded by the deadline + memory cap).
func TestRunner_Run_RunawayTrapped(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)
	r.Timeout = 300 * time.Millisecond

	if _, err := r.Run(context.Background(), `while (true) {}`); err == nil {
		t.Fatal("expected the runaway script to be trapped, got nil error")
	}
}

// The guest sees the real clock and real entropy, and both matter for a reason that is not comfort.
//
// wazero's defaults are deterministic stand-ins — a clock frozen at 2022-01-01 and a fixed random
// stream — and under them QuickJS returned the byte-identical Math.random() on every run of every
// script, because it seeds that PRNG from the clock. The prelude picked multipart boundaries from
// it. Two runs are enough to catch a regression here: with the frozen defaults every field below is
// identical between them.
func TestRunner_Run_ClockAndEntropyAreReal(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	const probe = `console.log(JSON.stringify({
		math: Math.random(),
		bytes: Array.from(crypto.getRandomValues(new Uint8Array(16))),
		year: new Date().getUTCFullYear(),
	}));`
	type sample struct {
		Math  float64 `json:"math"`
		Bytes []int   `json:"bytes"`
		Year  int     `json:"year"`
	}
	var got [2]sample
	for i := range got {
		out, err := r.Run(context.Background(), probe)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if err := json.Unmarshal([]byte(out), &got[i]); err != nil {
			t.Fatalf("run %d: %v (output %q)", i, err, out)
		}
	}

	if got[0].Math == got[1].Math {
		t.Errorf("Math.random() returned %v on both runs — the guest clock is frozen again", got[0].Math)
	}
	if slices.Equal(got[0].Bytes, got[1].Bytes) {
		t.Errorf("crypto.getRandomValues returned the same 16 bytes twice: %v", got[0].Bytes)
	}
	// Not "is it exactly now" — that would be a clock-skew test. 2022 is wazero's frozen default and
	// the only value this needs to rule out; anything past it means a real clock reached the guest.
	if got[0].Year <= 2022 {
		t.Errorf("the guest thinks the year is %d, so it is reading wazero's fake clock", got[0].Year)
	}
}

// A multipart boundary must be unguessable: it is the only thing separating the parts, so a
// predictable one lets a crafted field value close its part early and forge the rest of the body.
func TestPrelude_MultipartBoundaryIsUnpredictable(t *testing.T) {
	requireInterpreter(t)

	// The boundary is only observable where it is used: in the content_type http_write receives.
	var args string
	ts, err := agentkit.NewToolSet(recordingTool(t, "http_write", `{"status":200,"body":""}`, &args))
	if err != nil {
		t.Fatal(err)
	}
	r := script.New(ts)

	const probe = `const fd = new FormData();
		fd.append("field", "value");
		await fetch("https://example.invalid", { method: "POST", body: fd });`
	seen := make(map[string]bool)
	for range 2 {
		if _, err := r.Run(context.Background(), probe); err != nil {
			t.Fatalf("run: %v", err)
		}
		if !strings.Contains(args, "boundary=----NocturnFormBoundary") {
			t.Fatalf("no multipart boundary in %q", args)
		}
		if seen[args] {
			t.Fatalf("the same boundary twice: %q", args)
		}
		seen[args] = true
	}
}

// The one gate end to end, through the REAL interpreter: nocturn.call(tool, args)
// reaches the named tool's Call with its args (as the interpreter JSON-stringifies
// them), and the result flows back to the script — the SAME shared toolset the
// model dispatches through.
func TestRunner_Dispatch_RoutesThroughSharedToolset(t *testing.T) {
	requireInterpreter(t)
	var gotArgs string
	r := script.New(toolset(t, recordingTool(t, "echo", "PONG", &gotArgs)))

	out, err := r.Run(context.Background(), `console.log(nocturn.call("echo", {v: 42}))`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "PONG" {
		t.Fatalf("stdout = %q, want %q", out, "PONG")
	}
	if gotArgs != `{"v":42}` {
		t.Fatalf("tool received args %q, want %q", gotArgs, `{"v":42}`)
	}
}

// An argless call (whose args field the interpreter drops as undefined) reaches the
// tool as an empty JSON object, not an empty string — the dispatcher defaults it.
func TestRunner_Dispatch_EmptyArgsDefaultsToObject(t *testing.T) {
	requireInterpreter(t)
	var gotArgs string
	r := script.New(toolset(t, recordingTool(t, "echo", "ok", &gotArgs)))

	// Passing undefined makes JSON.stringify drop the args property entirely, so the
	// host sees no args and must default to "{}".
	if _, err := r.Run(context.Background(), `nocturn.call("echo", undefined)`); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotArgs != "{}" {
		t.Fatalf("tool received args %q, want %q", gotArgs, "{}")
	}
}

// The interpreter never dispatches itself: a script calling nocturn.call("code_run", …)
// is refused (no recursive interpreter), surfaced to the guest as a catchable error.
func TestRunner_Dispatch_RefusesCodeRunReentry(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(),
		`try { nocturn.call("code_run", {source: "1"}); console.log("REACHED"); } catch (e) { console.log(e.message); }`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "REACHED") || !strings.Contains(out, "code_run is not callable") {
		t.Fatalf("stdout = %q, want code_run rejected from within a script", out)
	}
}

// An unknown tool is fail-closed: the dispatcher returns an error, surfaced to the
// guest as a catchable JS exception — the host never crashes.
func TestRunner_Dispatch_UnknownTool_SurfacesErrorToGuest(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(),
		`try { nocturn.call("nope", {}); console.log("REACHED"); } catch (e) { console.log("caught: " + e.message); }`)
	if err != nil {
		t.Fatalf("Run should not fail on a guest-visible tool error: %v", err)
	}
	if strings.Contains(out, "REACHED") || !strings.Contains(out, "nope") {
		t.Fatalf("stdout = %q, want a caught error naming the unknown tool", out)
	}
}

// Every convenience wrapper the prelude publishes bottoms out at a tool name the
// host actually registers. The wrappers used to call dotted names (file.read,
// time.now, dns.resolve) that no toolset ever had, so nocturn.fs and nocturn.now
// failed with "unknown tool" while every test still passed — nothing exercised
// them. Each case asserts the dispatched NAME, which is the part that rotted.
func TestPrelude_WrappersDispatchToRegisteredNames(t *testing.T) {
	requireInterpreter(t)

	// Output each wrapper can digest: raw text where it returns the string as-is,
	// a JSON array where it JSON.parses a list, an object everywhere else.
	outputs := map[string]string{
		"file_read": "contents", "file_write": "", "file_remove": "", "file_move": "",
		"file_list": "[]", "file_search": "[]", "file_stat": `{"exists":true,"isDir":false,"size":0}`,
		"time_now": "{}", "ping": "{}", "dns_resolve": "{}",
		"notify": "{}", "remind": "{}", "wake": "{}", "skill_read": "# a bundled file",
		"http_read":  `{"status":200,"statusText":"OK","headers":{},"body":"hi"}`,
		"http_write": `{"status":201,"statusText":"Created","headers":{},"body":""}`,
	}

	var called string
	tools := make([]agentkit.Tool, 0, len(outputs))
	for name, out := range outputs {
		tl, err := agentkit.NewTool(name, "desc",
			func(_ context.Context, _ string) (string, error) {
				called = name
				return out, nil
			})
		if err != nil {
			t.Fatalf("NewTool(%q): %v", name, err)
		}
		tools = append(tools, tl)
	}
	r := script.New(toolset(t, tools...))

	// name is an identifier, not the snippet: a subtest name is escaped by the test
	// runner, and the JS carries slashes, spaces and quotes. The snippet goes in the
	// failure message instead, where it stays readable.
	cases := []struct {
		name string
		js   string
		want string
	}{
		{"fs_readFile", `nocturn.fs.readFile("a.txt")`, "file_read"},
		{"fs_writeFile", `nocturn.fs.writeFile("a.txt", "x")`, "file_write"},
		{"fs_list", `nocturn.fs.list("d")`, "file_list"},
		{"fs_stat", `nocturn.fs.stat("a.txt")`, "file_stat"},
		{"fs_remove", `nocturn.fs.remove("a.txt")`, "file_remove"},
		{"fs_search", `nocturn.fs.search("*.md")`, "file_search"},
		{"fs_move", `nocturn.fs.move("a.txt", "b.txt")`, "file_move"},
		{"nodeshim_readFileSync", `require("fs").readFileSync("a.txt")`, "file_read"},
		{"nodeshim_renameSync", `require("fs").renameSync("a.txt", "b.txt")`, "file_move"},
		{"now", `nocturn.now()`, "time_now"},
		{"ping", `nocturn.ping("example.com")`, "ping"},
		{"resolve", `nocturn.resolve("example.com")`, "dns_resolve"},
		{"notify", `nocturn.notify("hi")`, "notify"},
		{"remind", `nocturn.remind("in 1h", "hi")`, "remind"},
		{"wake", `nocturn.wake(1, "note")`, "wake"},
		{"skillFile", `nocturn.skillFile("summarize-url", "template.md")`, "skill_read"},
		{"fetch_get", `fetch("https://example.com/")`, "http_read"},
		{"fetch_post", `fetch("https://example.com/", {method: "POST", body: "x"})`, "http_write"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called = ""
			if _, err := r.Run(context.Background(), c.js); err != nil {
				t.Fatalf("Run(%q): %v", c.js, err)
			}
			if called != c.want {
				t.Fatalf("%s dispatched to %q, want %q", c.js, called, c.want)
			}
		})
	}
}

// Tool exposes the runner as code_run: it parses its {"source": "..."} argument,
// runs that source, and returns the script's stdout; a missing or malformed argument
// is rejected rather than running an empty script.
func TestRunner_Tool_ParsesSourceArg(t *testing.T) {
	requireInterpreter(t)
	var gotArgs string
	r := script.New(toolset(t, recordingTool(t, "echo", "OK", &gotArgs)))

	tl, err := r.Tool()
	if err != nil {
		t.Fatalf("Tool: %v", err)
	}
	if tl.Spec().Name != "code_run" {
		t.Fatalf("tool name = %q, want code_run", tl.Spec().Name)
	}

	args, _ := json.Marshal(map[string]string{"source": `console.log(nocturn.call("echo", {a: 1}))`})
	out, err := tl.Call(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if strings.TrimSpace(out) != "OK" {
		t.Fatalf("out = %q, want OK", out)
	}
	if gotArgs != `{"a":1}` {
		t.Fatalf("tool received args %q, want %q", gotArgs, `{"a":1}`)
	}

	// Bad arguments are reported, not run.
	if _, err := tl.Call(context.Background(), `{}`); err == nil {
		t.Fatal("expected an error for missing source, got nil")
	}
	if _, err := tl.Call(context.Background(), `not json`); err == nil {
		t.Fatal("expected an error for invalid arguments, got nil")
	}
}
