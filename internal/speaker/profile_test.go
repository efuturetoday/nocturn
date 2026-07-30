package speaker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/speaker"
)

// vec builds a unit vector pointing along one axis, so similarities are exact and readable: two
// vectors on the same axis score 1, on different axes 0.
func vec(dims, axis int) []float32 {
	v := make([]float32, dims)
	v[axis] = 1
	return v
}

func TestProfilesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voices.json")

	p, err := speaker.OpenProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("oliver", "satellite", vec(8, 0), vec(8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("anna", "phone", vec(8, 2)); err != nil {
		t.Fatal(err)
	}

	reopened, err := speaker.OpenProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Names()); got != 2 {
		t.Fatalf("reopened with %d voices, want 2", got)
	}
	if got := reopened.Identify(vec(8, 1), 0.5); got.Name != "oliver" {
		t.Errorf("identified %q, want oliver", got.Name)
	}
}

// A workspace where nobody has enrolled is the normal starting state.
func TestProfilesMissingFileIsEmpty(t *testing.T) {
	p, err := speaker.OpenProfiles(filepath.Join(t.TempDir(), "nothing-here.json"))
	if err != nil {
		t.Fatalf("a missing file should be an empty set, got %v", err)
	}
	if got := len(p.Names()); got != 0 {
		t.Errorf("%d voices from a missing file, want 0", got)
	}
}

// A corrupt file must NOT read as "nobody is enrolled". Silently starting from nothing would make
// the assistant forget the household, and the first sign of it would be the forgetting.
func TestProfilesCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voices.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := speaker.OpenProfiles(path); err == nil {
		t.Fatal("a corrupt profile file returned no error")
	}
}

// The property the whole design rests on: below the threshold the answer is nobody, never the
// closest guess.
func TestIdentifyBelowThresholdIsNobody(t *testing.T) {
	p, err := speaker.OpenProfiles(filepath.Join(t.TempDir(), "voices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("oliver", "satellite", vec(8, 0)); err != nil {
		t.Fatal(err)
	}

	// Orthogonal: similarity 0, well under any threshold. Oliver is the only candidate, and must
	// still not be the answer.
	got := p.Identify(vec(8, 3), speaker.DefaultThreshold)
	if got.Known() {
		t.Errorf("identified %q at %v, want nobody", got.Name, got.Confidence)
	}
}

// Enrolling the same person again adds a channel rather than replacing one.
func TestEnrolAccumulates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voices.json")
	p, err := speaker.OpenProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("oliver", "satellite", vec(8, 0)); err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("oliver", "phone", vec(8, 5)); err != nil {
		t.Fatal(err)
	}

	// Both channels still recognise him; the phone take did not displace the satellite one.
	for _, axis := range []int{0, 5} {
		if got := p.Identify(vec(8, axis), 0.5); got.Name != "oliver" {
			t.Errorf("axis %d identified %q, want oliver", axis, got.Name)
		}
	}
}

func TestForget(t *testing.T) {
	p, err := speaker.OpenProfiles(filepath.Join(t.TempDir(), "voices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("oliver", "satellite", vec(8, 0)); err != nil {
		t.Fatal(err)
	}
	if err := p.Forget("oliver"); err != nil {
		t.Fatal(err)
	}
	if got := p.Identify(vec(8, 0), 0.5); got.Known() {
		t.Errorf("a forgotten voice was still identified as %q", got.Name)
	}
	if err := p.Forget("nobody"); err == nil {
		t.Error("forgetting an unknown name returned no error")
	}
}

func TestEnrolRejectsNothing(t *testing.T) {
	p, err := speaker.OpenProfiles(filepath.Join(t.TempDir(), "voices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("", "satellite", vec(8, 0)); err == nil {
		t.Error("enrolling without a name returned no error")
	}
	if err := p.Enrol("oliver", "satellite"); err == nil {
		t.Error("enrolling with no recordings returned no error")
	}
}

// Rank exists so a near-miss is visible. A runner-up half a point behind the winner is the
// difference between recognition that works and recognition about to confuse two people, and the
// single answer Identify gives cannot show it.
func TestRank(t *testing.T) {
	p, err := speaker.OpenProfiles(filepath.Join(t.TempDir(), "voices.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Oliver is enrolled twice, on two channels. Anna sits on a third axis, so she is orthogonal
	// to the query and scores 0.
	if err := p.Enrol("oliver", "satellite", vec(8, 0)); err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("oliver", "phone", vec(8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("anna", "phone", vec(8, 2)); err != nil {
		t.Fatal(err)
	}

	ranked := p.Rank(vec(8, 1))
	if len(ranked) != 2 {
		t.Fatalf("ranked %d people, want 2", len(ranked))
	}
	if ranked[0].Name != "oliver" || ranked[1].Name != "anna" {
		t.Fatalf("order = %s, %s; want oliver, anna", ranked[0].Name, ranked[1].Name)
	}
	// The best single take, not an average over the person's takes: averaging 1 and 0 would put
	// Oliver at 0.5 and punish him for having enrolled on a second device.
	if ranked[0].Confidence != 1 {
		t.Errorf("oliver scored %v, want 1 — the best take, not the mean", ranked[0].Confidence)
	}
	// The device says which channel recognised him, which is the whole reason takes are kept apart.
	if ranked[0].Device != "phone" {
		t.Errorf("matched via %q, want phone", ranked[0].Device)
	}
	// Rank ignores the threshold on purpose — that is what makes the near-miss visible.
	if ranked[1].Confidence != 0 {
		t.Errorf("anna scored %v, want 0", ranked[1].Confidence)
	}
}

// Equal scores must resolve the same way every call, or a log line flaps between two tied people
// and reads as instability that is not there.
func TestRankBreaksTiesByName(t *testing.T) {
	p, err := speaker.OpenProfiles(filepath.Join(t.TempDir(), "voices.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zoe", "anna", "mo"} {
		if err := p.Enrol(name, "satellite", vec(8, 0)); err != nil {
			t.Fatal(err)
		}
	}
	// Map iteration is randomised, so a stable result over repeated calls is the assertion.
	for range 20 {
		ranked := p.Rank(vec(8, 0))
		got := []string{ranked[0].Name, ranked[1].Name, ranked[2].Name}
		if got[0] != "anna" || got[1] != "mo" || got[2] != "zoe" {
			t.Fatalf("order = %v, want [anna mo zoe]", got)
		}
	}
}

// A take embedded by a different model has a different width and cannot be compared. That is not a
// failure, but it must not score zero either — a zero would rank the person as present and wrong,
// where absent is the truth.
func TestRankSkipsIncomparableTakes(t *testing.T) {
	p, err := speaker.OpenProfiles(filepath.Join(t.TempDir(), "voices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("oliver", "satellite", vec(8, 0)); err != nil {
		t.Fatal(err)
	}
	if err := p.Enrol("stale", "satellite", vec(4, 0)); err != nil {
		t.Fatal(err)
	}

	ranked := p.Rank(vec(8, 0))
	if len(ranked) != 1 || ranked[0].Name != "oliver" {
		t.Fatalf("ranked %v, want oliver alone", ranked)
	}
}
