package script

import (
	"context"
	"slices"
	"testing"

	"github.com/tetratelabs/wazero"
)

// hostModule is sandbox's name for the one module a guest may import from. Spelled out rather than
// referenced, because it is unexported there — and because a test that reads the value it is meant
// to pin would agree with any change to it.
const hostModule = "nocturn"

// The interpreter guest's import surface IS its authority, and this pins it.
//
// The runtime already refuses an unknown import at instantiation: sandbox.New registers only the
// nocturn host module and WASI, so anything else fails to link. But that check is relative to
// EngineConfig.HostNames — grow that list and the permitted surface grows silently with it, while
// guest.go still promises "exactly ONE host import". This test is the promise, not the mechanism.
//
// The WASI list is pinned for a different reason: it is not fixed by us but by what quickjs-ng
// happens to call. A version bump that pulls in path_open or sock_accept would hand the guest a way
// to open files or sockets the moment a preopen exists — the two imports that would actually mean
// reach. Failing here is the point; the fix is to look at why, not to update the list reflexively.
func TestInterpreterGuest_ImportSurface(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })

	code, err := rt.CompileModule(ctx, interpreterGuest)
	if err != nil {
		t.Fatalf("compiling the embedded guest: %v", err)
	}

	// The eight WASI functions the interpreter needs: stdio, a clock, entropy, and a way to exit.
	// Sorted, because the order a module lists its imports in is not part of the contract.
	wantWASI := []string{
		"clock_time_get", "fd_close", "fd_fdstat_get", "fd_read",
		"fd_seek", "fd_write", "proc_exit", "random_get",
	}

	var gotWASI, gotHost []string
	for _, fn := range code.ImportedFunctions() {
		module, name, ok := fn.Import()
		if !ok {
			continue
		}
		switch module {
		case "wasi_snapshot_preview1":
			gotWASI = append(gotWASI, name)
		default:
			gotHost = append(gotHost, module+"."+name)
		}
	}
	slices.Sort(gotWASI)
	slices.Sort(gotHost)

	if want := []string{hostModule + "." + gateName}; !slices.Equal(gotHost, want) {
		t.Errorf("host imports = %v, want %v — the guest's authority surface changed", gotHost, want)
	}
	if !slices.Equal(gotWASI, wantWASI) {
		t.Errorf("WASI imports = %v, want %v", gotWASI, wantWASI)
	}
}

// The host side of the ABI: the sandbox writes a gate response into guest memory through the guest's
// own allocator, and reads the result out of its exported memory. A guest missing any of these still
// runs — and every tool result silently arrives empty, because writeToGuest gives up and returns 0.
// That is the kind of failure a rebuild could introduce with nothing else going red.
func TestInterpreterGuest_ExportsTheABI(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })

	code, err := rt.CompileModule(ctx, interpreterGuest)
	if err != nil {
		t.Fatalf("compiling the embedded guest: %v", err)
	}

	exported := code.ExportedFunctions()
	for _, name := range []string{"_start", "malloc", "free"} {
		if _, ok := exported[name]; !ok {
			t.Errorf("the guest does not export %q", name)
		}
	}
	if len(code.ExportedMemories()) == 0 {
		t.Error("the guest exports no memory, so the host cannot hand it anything")
	}
}
