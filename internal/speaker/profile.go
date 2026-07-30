package speaker

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
)

// DefaultThreshold is the similarity a match must reach to count as a person rather than as nobody.
//
// It is 0.45 because that is what the satellite measured, not what the literature suggests. Through a
// far-field microphone array the same voice's own worst pair scored 0.4873 while the best stranger
// reached 0.3922 — so the 0.50 that close-talking recordings support would turn a household member
// away. A threshold belongs to a channel; see testdata/README.md for both measurements.
const DefaultThreshold = 0.45

// Take is one enrolled recording of a voice: the embedding, and which device heard it.
//
// The device is kept because a person enrolled on a phone and recognised through a hallway speaker is
// two different channels, and averaging them into one vector produces a point that matches neither.
// Whether that actually costs anything is not yet measured — but keeping the takes apart costs
// nothing and leaves the question open, while averaging them closes it by accident.
type Take struct {
	Device string    `json:"device"`
	Vector []float32 `json:"vector"`
}

// Profiles is the set of voices one workspace knows, persisted as JSON.
//
// Safe for concurrent use: recognition reads it from a voice session while enrolment may be writing.
type Profiles struct {
	path string

	mu     sync.RWMutex
	people map[string][]Take
}

// file is the on-disk shape. A struct around the takes rather than a bare list, so a later field
// (a per-person threshold, a display name) does not change the format of what is already written.
type file map[string]struct {
	Takes []Take `json:"takes"`
}

// OpenProfiles loads the voices at path. A missing file is an empty set — a workspace where nobody
// has enrolled yet is the normal starting state, not an error.
//
// A corrupt file IS an error. Recognition that silently starts from nothing would quietly stop
// knowing anyone, and the first sign of it would be an assistant that has forgotten the household.
func OpenProfiles(path string) (*Profiles, error) {
	p := &Profiles{path: path, people: map[string][]Take{}}

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("speaker: reading %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("speaker: %s is not valid: %w", path, err)
	}
	for name, entry := range f {
		p.people[name] = entry.Takes
	}
	return p, nil
}

// Names lists the enrolled voices.
func (p *Profiles) Names() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.people))
	for name := range p.people {
		out = append(out, name)
	}
	return out
}

// Enrol adds recordings of one voice and saves. Takes accumulate rather than replace: enrolling the
// same person from a second device is how a profile learns that channel, not how it forgets the first.
func (p *Profiles) Enrol(name, device string, vectors ...[]float32) error {
	if name == "" {
		return fmt.Errorf("speaker: a profile needs a name")
	}
	if len(vectors) == 0 {
		return fmt.Errorf("speaker: enrolling %q with no recordings", name)
	}

	p.mu.Lock()
	for _, v := range vectors {
		p.people[name] = append(p.people[name], Take{Device: device, Vector: v})
	}
	err := p.save()
	p.mu.Unlock()
	return err
}

// Forget removes a voice entirely, and saves. Somebody who leaves the household should be able to
// take their voiceprint with them.
func (p *Profiles) Forget(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.people[name]; !ok {
		return fmt.Errorf("speaker: no profile named %q", name)
	}
	delete(p.people, name)
	return p.save()
}

// Match is how well one enrolled person fits an embedding, and through which of their devices.
type Match struct {
	Name       string
	Confidence float32
	Device     string // the take that matched, which says which channel it recognised them through
}

// Rank scores every enrolled voice against an embedding, best first, ignoring the threshold.
//
// A person's score is their best single take, not an average over them. Averaging would punish
// somebody for having enrolled on a device other than the one they are speaking through, which is
// the whole reason the takes are kept apart.
//
// The full ranking exists for looking at: a runner-up half a point behind the winner is the
// difference between recognition that works and recognition that is about to confuse two people,
// and a single answer cannot show it.
func (p *Profiles) Rank(embedding []float32) []Match {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]Match, 0, len(p.people))
	for name, takes := range p.people {
		best := Match{Name: name}
		found := false // a person whose every take is incomparable is left out, not scored zero
		for _, take := range takes {
			score, err := Similarity(embedding, take.Vector)
			if err != nil {
				continue // a take from a different model cannot be compared, and is not a failure here
			}
			if !found || score > best.Confidence {
				best.Confidence, best.Device = score, take.Device
				found = true
			}
		}
		if found {
			out = append(out, best)
		}
	}
	// Best first; equal scores fall back to the name, so a log line does not flap between two
	// people who are genuinely tied.
	slices.SortFunc(out, func(a, b Match) int {
		return cmp.Or(cmp.Compare(b.Confidence, a.Confidence), strings.Compare(a.Name, b.Name))
	})
	return out
}

// Identify returns whose voice an embedding is closest to, or the zero Identity when nothing reaches
// the threshold.
func (p *Profiles) Identify(embedding []float32, threshold float32) Identity {
	ranked := p.Rank(embedding)
	if len(ranked) == 0 || ranked[0].Confidence < threshold {
		return Identity{} // below the line is nobody, never the nearest guess
	}
	return Identity{Name: ranked[0].Name, Confidence: ranked[0].Confidence}
}

// save writes the set. The caller holds the lock.
//
// Written to a temporary file and renamed, the same way grants are (internal/workspace/grantstore.go):
// a half-written profile set that replaced a good one would lose a household's enrolments to a power
// cut, and 0600 because a voiceprint is nobody else's business.
func (p *Profiles) save() error {
	f := make(file, len(p.people))
	for name, takes := range p.people {
		f[name] = struct {
			Takes []Take `json:"takes"`
		}{Takes: takes}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}
