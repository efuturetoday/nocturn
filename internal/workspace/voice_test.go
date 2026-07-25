package workspace_test

import (
	"slices"
	"testing"
)

// The cage is the security boundary for a spoken session, so it is pinned by name rather than left
// to a comment. If a tool that changes the world ever appears in this list, an open microphone in a
// room can be talked into using it — by the household, a guest, or a television.
func TestVoiceCage_HoldsNoWritingTool(t *testing.T) {
	w := openWS(t, fakeLLM{})

	forbidden := []string{
		"file_write", "file_remove", "file_move", // the filesystem
		"http_write", // anything with a side effect on the network
		"code_run",   // a script's reach is its cage, and this cage is not it
	}
	got := w.VoiceTools()
	for _, name := range forbidden {
		if slices.Contains(got, name) {
			t.Errorf("%q is reachable by voice; the cage is an allowlist of read-only tools", name)
		}
	}
}

func TestVoiceCage_KeepsTheReadingTools(t *testing.T) {
	w := openWS(t, fakeLLM{})

	// Without these a spoken session has nothing useful to do, and the cage would be pointless
	// rather than protective.
	want := []string{"file_read", "http_read", "time_now"}
	got := w.VoiceTools()
	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("%q missing from the voice cage, have %v", name, got)
		}
	}
}

func TestVoice_BuildsADriver(t *testing.T) {
	w := openWS(t, fakeLLM{})
	if w.Voice(nil) == nil {
		t.Fatal("Voice returned nil")
	}
}
