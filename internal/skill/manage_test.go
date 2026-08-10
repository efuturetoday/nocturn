package skill_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/skill"
)

// write lays down skills/<folder>/SKILL.md with the given frontmatter name.
func write(t *testing.T, dir, folder, name string) {
	t.Helper()
	sdir := filepath.Join(dir, folder)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: does a thing\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(sdir, skill.SkillFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func names(entries []skill.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// Skills are the deliberate exception to folder-is-identity in this tree: the frontmatter name wins.
// So every management call addresses the NAME and resolves the folder itself — a caller that treated
// the name as a path would miss this skill entirely, or hit a different one.
func TestManage_AddressesTheNameNotTheFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "some-folder", "deploy")
	write(t, dir, "deploy", "other") // the folder called "deploy" is a DIFFERENT skill

	body, err := skill.Read(dir, "deploy")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := "name: deploy"; !strings.Contains(body, want) {
		t.Fatalf("Read(%q) returned the wrong skill:\n%s", "deploy", body)
	}

	if err := skill.Remove(dir, "deploy"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "some-folder")); !os.IsNotExist(err) {
		t.Error("Remove left the skill it was asked to remove in place")
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy")); err != nil {
		t.Error("Remove deleted the folder whose NAME matched, not the skill")
	}
}

// Disabling moves a skill under .disabled/, which Discover skips — so the catalog loses it while the
// folder, and anything bundled beside its SKILL.md, stays put.
func TestManage_DisableHidesItFromDiscoveryAndBack(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "deploy", "deploy")
	write(t, dir, "notes", "notes")

	if err := skill.SetEnabled(dir, "deploy", false); err != nil {
		t.Fatalf("SetEnabled(off): %v", err)
	}

	set, _ := skill.Discover(dir, nil)
	if _, live := set["deploy"]; live {
		t.Error("a disabled skill is still in the catalog")
	}
	if _, live := set["notes"]; !live {
		t.Error("disabling one skill hid another")
	}

	// The listing still shows it — a list that omitted it could not offer switching it back on.
	all, err := skill.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := names(all); !slices.Equal(got, []string{"deploy", "notes"}) {
		t.Fatalf("List = %v, want both", got)
	}
	for _, e := range all {
		if e.Name == "deploy" && e.Enabled {
			t.Error("the disabled skill is listed as enabled")
		}
	}

	if err := skill.SetEnabled(dir, "deploy", true); err != nil {
		t.Fatalf("SetEnabled(on): %v", err)
	}
	set, _ = skill.Discover(dir, nil)
	if _, live := set["deploy"]; !live {
		t.Error("re-enabling did not put the skill back in the catalog")
	}
}

// Discover drops a duplicate name silently (first wins), so installing into a shadow would look like
// it worked and change nothing. Refusing is the only outcome a person can act on.
func TestWrite_RefusesAShadowedName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "deploy", "deploy")

	body := "---\nname: deploy\ndescription: a second one\n---\n\nDifferent.\n"
	if _, err := skill.Write(dir, "deploy-2", body); err == nil {
		t.Fatal("installing a skill whose name is already taken was allowed")
	}

	// Including when the shadowed one is only disabled — it is still there, and re-enabling it later
	// would then be ambiguous.
	if err := skill.SetEnabled(dir, "deploy", false); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Write(dir, "deploy-2", body); err == nil {
		t.Fatal("a disabled skill did not shadow the name it still holds")
	}
}

// A body without valid frontmatter is not a skill, and finding that out at install time beats
// discovering it as a silently skipped entry later.
func TestWrite_RejectsAnInvalidBody(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{
		"no frontmatter at all",
		"---\nname: ok\n---\n\nbody\n",         // no description
		"---\ndescription: only this\n---\n\n", // no name, and the folder is not one either
	} {
		if _, err := skill.Write(dir, "Bad Folder", body); err == nil {
			t.Fatalf("accepted an invalid skill: %q", body)
		}
	}
}

// A skill installed under a folder appears in the catalog exactly as Discover would read it.
func TestWrite_InstallsAndIsDiscovered(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: deploy\ndescription: ships things\n---\n\nDo the deploy.\n"
	e, err := skill.Write(dir, "deploy", body)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if e.Name != "deploy" || !e.Enabled || e.Bytes != len(body) {
		t.Fatalf("entry = %+v", e)
	}
	set, dirs := skill.Discover(dir, nil)
	if _, ok := set["deploy"]; !ok {
		t.Fatal("the installed skill is not in the catalog")
	}
	if dirs["deploy"] == "" {
		t.Fatal("the installed skill has no directory for skill_read")
	}
}
