package library

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sync"
)

// serialFile records the highest serial this daemon has accepted per plugin, beside the catalog cache.
const serialFile = "catalog-serials.json"

// Freshness is the question a signature cannot answer.
//
// A signature says "we published these bytes". It does not say "this is the newest thing we
// published", and nothing in a fetch can tell the two apart: a host that has been taken over, or a
// stale mirror, can serve an OLD and perfectly signed entry forever — including one that was
// withdrawn because it turned out to be wrong. Fetching more often does not help. A liar asked twice
// lies twice; what is missing is not frequency but MEMORY.
//
// So each entry carries a serial that only goes up, the serial is inside the signed statement, and
// this remembers the highest one accepted per plugin. A lower one is refused.
//
// Two limits, stated rather than left to be discovered:
//
//   - The FIRST sight of a plugin has nothing to compare against and is accepted — trust on first
//     use. Protection starts at the second fetch, which is the same shape SSH host keys have.
//   - A plugin REMOVED from the catalog cannot be detected this way. Serving an old catalog that
//     still contains it presents every entry at a serial this daemon already accepted, and nothing
//     here says how many entries there should be. Catching that needs a signature over the SET —
//     which means the signing key on every publish, including one that only edits a skill, and that
//     is a release-process decision rather than a code one.
type freshness struct {
	path string
	log  *slog.Logger

	mu   sync.Mutex
	seen map[string]int // plugin id → highest serial accepted
}

// openFreshness loads the remembered serials, treating a missing or unreadable file as none. Losing
// the file costs the protection until each plugin is seen once more; it can never cause a refusal,
// which is the right direction for a cache of "what I have seen".
func openFreshness(path string, log *slog.Logger) *freshness {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	f := &freshness{path: path, log: log, seen: map[string]int{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return f
	}
	if err := json.Unmarshal(data, &f.seen); err != nil {
		log.Warn("library: unreadable serial memory — starting from none", "path", path, "err", err)
		f.seen = map[string]int{}
	}
	return f
}

// check reports whether an entry may be offered: its serial must be at least the highest accepted for
// that plugin. A nil freshness accepts everything, so a Store built without one is not a refusal
// machine.
func (f *freshness) check(it PluginItem) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if was, ok := f.seen[it.ID]; ok && it.Serial < was {
		return fmt.Errorf("serial %d is older than the %d this daemon already accepted — "+
			"a signed entry cannot go backwards", it.Serial, was)
	}
	return nil
}

// accept records an entry's serial as seen. Called only for entries that passed everything else, so
// a malformed or unsigned entry cannot raise the floor and lock out the real one.
func (f *freshness) accept(it PluginItem) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if was, ok := f.seen[it.ID]; ok && it.Serial <= was {
		return
	}
	f.seen[it.ID] = it.Serial
	f.save()
}

// save persists the serials atomically. Callers hold f.mu. Best-effort: a failed write costs the
// memory, never the catalog in hand — the same trade the catalog cache makes.
func (f *freshness) save() {
	data, err := json.MarshalIndent(f.seen, "", "  ")
	if err != nil {
		return
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		f.log.Warn("library: recording catalog serials", "err", err)
		return
	}
	if err := os.Rename(tmp, f.path); err != nil {
		f.log.Warn("library: recording catalog serials", "err", err)
	}
}

// snapshot returns what is remembered, for a test to read without reaching into the lock.
func (f *freshness) snapshot() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.seen)
}
