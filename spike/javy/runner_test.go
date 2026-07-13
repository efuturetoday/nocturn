// Package javyspike is a THROWAWAY evaluation of the "own raw wazero host +
// Javy for JS" path. Own go.mod so its deps never touch the real module.
//
// It proves: a TypeScript code-interpreter skill (skill.ts -> esbuild ->
// skill.js -> javy -> skill.wasm) runs on OUR OWN raw wazero host with only
// bounded stdio granted — no framework, minimal TCB (just wazero). The guest
// imports ONLY wasi stdio; we grant NO filesystem, NO args, NO env, so it has
// zero ambient authority beyond the two pipes we hand it.
package javyspike

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

//go:embed skill.wasm
var skillWasm []byte

func TestJavy_CodeInterpreter_OnOwnRawHost(t *testing.T) {
	ctx := context.Background()

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	// Grant a narrow, bounded slice of WASI: stdio only. Not the sledgehammer —
	// we set only stdin/stdout and mount no filesystem, pass no args/env.
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	in := bytes.NewReader([]byte(`{"name":"nocturn","numbers":[3,4,5]}`))
	var out bytes.Buffer

	cfg := wazero.NewModuleConfig().
		WithStdin(in).
		WithStdout(&out)
		// deliberately absent: WithFSConfig, WithArgs, WithEnv -> no fs, no ambient authority

	_, err := r.InstantiateWithConfig(ctx, skillWasm, cfg)
	if err != nil {
		// Javy calls proc_exit(0) on normal completion; treat exit 0 as success.
		if ee, ok := err.(*sys.ExitError); !ok || ee.ExitCode() != 0 {
			t.Fatalf("javy code-interpreter failed: %v", err)
		}
	}

	var res struct {
		Greeting string `json:"greeting"`
		Sum      int    `json:"sum"`
		Max      int    `json:"max"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("guest produced non-JSON output %q: %v", out.String(), err)
	}

	if res.Greeting != "hello, nocturn" || res.Sum != 12 || res.Max != 5 {
		t.Fatalf("unexpected transform result: %+v (raw: %s)", res, out.String())
	}
	t.Logf("TS code-interpreter ran on own raw host, output: %s", out.String())
}
