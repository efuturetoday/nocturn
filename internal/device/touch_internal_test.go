package device

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// diskLastUsed reads the persisted store and returns the LastUsed of the device with id.
func diskLastUsed(t *testing.T, path, id string) time.Time {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]Device
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, d := range m {
		if d.ID == id {
			return d.LastUsed
		}
	}
	return time.Time{}
}

func memLastUsed(s *Store, id string) time.Time {
	for _, d := range s.List() {
		if d.ID == id {
			return d.LastUsed
		}
	}
	return time.Time{}
}

// Touch updates LastUsed in memory immediately but coalesces disk writes: a reconnect within the
// flush interval must not hit the file; past the interval (or via Flush) it does.
func TestStore_TouchCoalesces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	s := Load(path)
	clk := time.Unix(1000, 0)
	s.now = func() time.Time { return clk }

	id, _, _ := s.Add("phone", "ios") // persists → lastPersist = 1000, LastUsed still zero

	// A touch within the interval: memory updates, disk does NOT.
	clk = clk.Add(time.Minute)
	s.Touch(id)
	if got := memLastUsed(s, id); !got.Equal(clk) {
		t.Fatalf("in-memory LastUsed = %v, want %v (immediate)", got, clk)
	}
	if got := diskLastUsed(t, path, id); !got.IsZero() {
		t.Fatalf("disk LastUsed = %v, want zero (write throttled within the interval)", got)
	}

	// A touch past the interval flushes.
	clk = clk.Add(touchFlushInterval)
	s.Touch(id)
	if got := diskLastUsed(t, path, id); !got.Equal(clk) {
		t.Fatalf("disk LastUsed = %v after the interval, want %v (flushed)", got, clk)
	}

	// Flush writes a later throttled touch regardless of the interval.
	clk = clk.Add(time.Second)
	s.Touch(id) // throttled (just flushed)
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := diskLastUsed(t, path, id); !got.Equal(clk) {
		t.Fatalf("disk LastUsed = %v after Flush, want %v", got, clk)
	}
}
