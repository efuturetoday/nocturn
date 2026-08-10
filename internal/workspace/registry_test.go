package workspace_test

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// Create publishes a workspace or it does not — never a hole. A placeholder in the map would be a
// nil *Workspace that Get reports as FOUND, and every caller dereferences what Get hands back: one
// device creating a workspace while another names it in a command would be a nil-pointer panic
// reachable over the wire.
func TestRegistry_CreateNeverPublishesAHole(t *testing.T) {
	reg := newRegistry(t, t.TempDir())

	var wg sync.WaitGroup
	wg.Go(func() { _, _ = reg.Create("work", "Arbeit") })
	wg.Go(func() {
		for range 200 {
			if ws, ok := reg.Get("work"); ok && ws == nil {
				t.Error("Get reported a workspace that is nil — a reader saw the hole")
				return
			}
		}
	})
	wg.Wait()

	ws, ok := reg.Get("work")
	if !ok || ws == nil {
		t.Fatal("the created workspace is missing")
	}
	if ws.Title() != "Arbeit" {
		t.Errorf("title = %q, want \"Arbeit\"", ws.Title())
	}
}

// Two devices creating the same name must not both open the directory — that would put two vaults
// and two sets of timers on the same files. Exactly one wins.
func TestRegistry_ConcurrentCreateOfOneName(t *testing.T) {
	reg := newRegistry(t, t.TempDir())

	var mu sync.Mutex
	var won int
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := reg.Create("work", ""); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if won != 1 {
		t.Fatalf("%d creates succeeded for one name, want exactly 1", won)
	}
}

// Delete is a move, not a removal: the folder is every conversation, every note and a vault, removed
// from a list on a phone. Recovering has to be `mv`.
func TestRegistry_DeleteMovesToTrash(t *testing.T) {
	root := t.TempDir()
	reg := newRegistry(t, root)

	if _, err := reg.Create("work", "Arbeit"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := reg.Delete("work"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := reg.Get("work"); ok {
		t.Error("the deleted workspace still resolves")
	}
	if _, err := os.Stat(filepath.Join(root, "work")); !os.IsNotExist(err) {
		t.Error("the folder is still in place")
	}

	trashed, err := os.ReadDir(filepath.Join(root, ".trash"))
	if err != nil || len(trashed) != 1 {
		t.Fatalf("trash = %v, %v; want exactly one entry", trashed, err)
	}
	// Its content came along — including the title, so a restored folder is the workspace it was.
	body, err := os.ReadFile(filepath.Join(root, ".trash", trashed[0].Name(), "workspace.json"))
	if err != nil || len(body) == 0 {
		t.Fatalf("the moved folder lost its workspace.json: %v", err)
	}
}

// The trash is a dot-directory so the next start skips it. Without that, a deleted workspace would
// come back as one — with its own vault and its own schedulers.
func TestRegistry_TrashIsNotReopened(t *testing.T) {
	root := t.TempDir()
	reg := newRegistry(t, root)
	if _, err := reg.Create("work", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := reg.Delete("work"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reg.Close()

	reopened := newRegistry(t, root)
	if got := reopened.Names(); !slices.Equal(got, []string{workspace.DefaultWorkspace}) {
		t.Fatalf("after a restart, names = %v — the trash came back as a workspace", got)
	}
}

// Deleting the default would appear to work and then undo itself at the next start.
func TestRegistry_DeleteRefusesTheDefault(t *testing.T) {
	reg := newRegistry(t, t.TempDir())
	if err := reg.Delete(workspace.DefaultWorkspace); err == nil {
		t.Fatal("deleting the default workspace was allowed")
	}
}

// The title is a label; the folder is identity. Renaming must never move the folder — the folder name
// is the input to this workspace's vault key and to every plugin and MCP shard key.
func TestRegistry_SetTitleLeavesTheFolderAlone(t *testing.T) {
	root := t.TempDir()
	reg := newRegistry(t, root)
	if _, err := reg.Create("work", "Arbeit"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := reg.SetTitle("work", "Büro"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	ws, _ := reg.Get("work")
	if ws.Title() != "Büro" {
		t.Errorf("Title() = %q, want \"Büro\"", ws.Title())
	}
	if ws.Name() != "work" {
		t.Errorf("Name() = %q — the identity moved with the label", ws.Name())
	}
	if _, err := os.Stat(filepath.Join(root, "work")); err != nil {
		t.Errorf("the folder is no longer at its name: %v", err)
	}

	// It survives a reopen, and clearing it shows the folder name again.
	reg.Close()
	reopened := newRegistry(t, root)
	back, _ := reopened.Get("work")
	if back.Title() != "Büro" {
		t.Errorf("after reopen, Title() = %q, want \"Büro\"", back.Title())
	}
	if err := back.SetTitle("  "); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	if back.Title() != "work" {
		t.Errorf("cleared title = %q, want the folder name", back.Title())
	}
}

// A workspace created at runtime is not a second-class one: the registry runs OnOpen for it, which is
// how the daemon wires its chat activity, event streams and schedulers.
func TestRegistry_CreateRunsOnOpen(t *testing.T) {
	reg := newRegistry(t, t.TempDir())

	var opened []string
	reg.OnOpen(func(ws *workspace.Workspace) { opened = append(opened, ws.Name()) })

	if _, err := reg.Create("work", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !slices.Equal(opened, []string{"work"}) {
		t.Fatalf("OnOpen saw %v, want [work]", opened)
	}
}
