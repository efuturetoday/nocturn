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
