package library

import (
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A signature says "we published these bytes", never "this is current". So a host that has been taken
// over — or a stale mirror — can serve an OLD and perfectly signed entry forever, including one
// withdrawn because it turned out to be wrong. Asking more often changes nothing; remembering does.
func TestFreshness_ARolledBackEntryIsRefused(t *testing.T) {
	pub, priv := testKey(t)
	t.Setenv(devKeyEnv, base64.StdEncoding.EncodeToString(pub))
	dir := t.TempDir()

	current := serialItem(t, priv, 7)
	old := serialItem(t, priv, 3)

	seen := openFreshness(filepath.Join(dir, serialFile), nil)
	if got := validPlugins([]PluginItem{current}, quiet(), signaturesRequired, seen); len(got) != 1 {
		t.Fatalf("the current entry was refused: %+v", got)
	}
	if got := seen.snapshot()["gmail"]; got != 7 {
		t.Fatalf("remembered serial = %d, want 7", got)
	}

	var log strings.Builder
	if got := validPlugins([]PluginItem{old}, slog.New(slog.NewTextHandler(&log, nil)), signaturesRequired, seen); len(got) != 0 {
		t.Errorf("a correctly signed OLDER entry was offered: %+v", got)
	}
	if !strings.Contains(log.String(), "backwards") {
		t.Errorf("the log does not say why: %q", log.String())
	}
	// And the floor did not move down.
	if got := seen.snapshot()["gmail"]; got != 7 {
		t.Errorf("remembered serial = %d after a refused rollback, want 7", got)
	}
}

// The memory outlives the process, or the protection lasts exactly one run.
func TestFreshness_SurvivesAReopen(t *testing.T) {
	pub, priv := testKey(t)
	t.Setenv(devKeyEnv, base64.StdEncoding.EncodeToString(pub))
	path := filepath.Join(t.TempDir(), serialFile)

	first := openFreshness(path, nil)
	validPlugins([]PluginItem{serialItem(t, priv, 5)}, quiet(), signaturesRequired, first)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing was recorded: %v", err)
	}
	reopened := openFreshness(path, nil)
	if got := validPlugins([]PluginItem{serialItem(t, priv, 4)}, quiet(), signaturesRequired, reopened); len(got) != 0 {
		t.Error("a rollback was accepted after a restart")
	}
	if got := validPlugins([]PluginItem{serialItem(t, priv, 6)}, quiet(), signaturesRequired, reopened); len(got) != 1 {
		t.Error("moving forward was refused after a restart")
	}
}

// Trust on first use, stated as a test because it is a limit rather than an accident: the first sight
// of a plugin has nothing to compare against. Protection starts at the second fetch.
func TestFreshness_TheFirstSightIsAccepted(t *testing.T) {
	pub, priv := testKey(t)
	t.Setenv(devKeyEnv, base64.StdEncoding.EncodeToString(pub))

	seen := openFreshness(filepath.Join(t.TempDir(), serialFile), nil)
	if got := validPlugins([]PluginItem{serialItem(t, priv, 1)}, quiet(), signaturesRequired, seen); len(got) != 1 {
		t.Error("the first sight of a plugin was refused; there is nothing to compare it against")
	}
}

// A refused entry must not raise the floor, or an attacker could lock out the real one by serving a
// bad entry with an enormous serial.
func TestFreshness_ARefusedEntryDoesNotRaiseTheFloor(t *testing.T) {
	pub, priv := testKey(t)
	t.Setenv(devKeyEnv, base64.StdEncoding.EncodeToString(pub))

	forged := serialItem(t, priv, 1)
	forged.Serial = 9000 // signed for 1, claiming 9000 — the signature no longer covers it

	seen := openFreshness(filepath.Join(t.TempDir(), serialFile), nil)
	if got := validPlugins([]PluginItem{forged}, quiet(), signaturesRequired, seen); len(got) != 0 {
		t.Fatal("an entry whose serial is outside its signature was offered")
	}
	if got, ok := seen.snapshot()["gmail"]; ok {
		t.Errorf("a refused entry raised the floor to %d", got)
	}
}

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// installableManifest is what a plugin entry has to carry to get past checkManifest — validPlugins
// runs the loader's own parser, so a fixture with a placeholder manifest would be dropped for a
// reason that has nothing to do with freshness.
const installableManifest = `{"name":"gmail","version":"1",` +
	`"tools":[{"name":"search","parameters":{"type":"object"}}],"uses":["http_read"]}`

// serialItem is a fully consistent, signed, installable entry at the given serial.
func serialItem(t *testing.T, priv ed25519.PrivateKey, serial int) PluginItem {
	t.Helper()
	it := signedItem(t, priv, "gmail", installableManifest, "// code", "")
	it.Serial = serial
	it.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, SignedStatement(Signed{
		ID: it.ID, Folder: it.Folder,
		ManifestSHA: it.ManifestSHA, ScriptSHA: it.ScriptSHA, SkillSHA: it.SkillSHA,
		ListingSHA: it.listingDigest(), Serial: it.Serial,
	})))
	return it
}
