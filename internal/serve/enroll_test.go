package serve

import (
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// quiet renders n bytes below the silence floor — room tone, not digital silence.
func quiet(n int) []byte {
	pcm := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], uint16(int16(50)))
	}
	return pcm
}

// loud renders n bytes of audio comfortably above the silence floor.
func loud(n int) []byte {
	pcm := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], uint16(int16(4000)))
	}
	return pcm
}

// The header this writes is parsed by internal/speaker's test reader and by sox, neither of which
// this package can call, so the fields are checked here against the RIFF layout directly.
func TestWriteWAVHeader(t *testing.T) {
	pcm := make([]byte, 640) // 20 ms at 16 kHz, the uplink frame size
	for i := range pcm {
		pcm[i] = byte(i)
	}
	path := filepath.Join(t.TempDir(), "out.wav")
	if err := writeWAV(path, pcm); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 44+len(pcm) {
		t.Fatalf("file is %d bytes, want %d", len(raw), 44+len(pcm))
	}

	u32 := func(at int) uint32 { return binary.LittleEndian.Uint32(raw[at:]) }
	u16 := func(at int) uint16 { return binary.LittleEndian.Uint16(raw[at:]) }
	for _, tc := range []struct {
		what string
		got  any
		want any
	}{
		{"RIFF tag", string(raw[0:4]), "RIFF"},
		{"chunk size", u32(4), uint32(36 + len(pcm))},
		{"WAVE tag", string(raw[8:12]), "WAVE"},
		{"fmt tag", string(raw[12:16]), "fmt "},
		{"fmt size", u32(16), uint32(16)},
		{"format", u16(20), uint16(1)},
		{"channels", u16(22), uint16(1)},
		{"sample rate", u32(24), uint32(16000)},
		{"byte rate", u32(28), uint32(32000)},
		{"block align", u16(32), uint16(2)},
		{"bits", u16(34), uint16(16)},
		{"data tag", string(raw[36:40]), "data"},
		{"data size", u32(40), uint32(len(pcm))},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.what, tc.got, tc.want)
		}
	}
	if string(raw[44:]) != string(pcm) {
		t.Error("samples were not written verbatim")
	}
}

func TestWriteWAVRefusesToOverwrite(t *testing.T) {
	// Recordings are named by the second they ended; two in one second must not silently become one.
	path := filepath.Join(t.TempDir(), "out.wav")
	if err := writeWAV(path, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	if err := writeWAV(path, make([]byte, 64)); err == nil {
		t.Error("writing over an existing recording returned no error")
	}
}

func TestCaptureIsOffUnlessArmed(t *testing.T) {
	t.Setenv(captureDirEnv, "")
	if got := newCapture("dev", slog.Default()); got != nil {
		t.Fatal("capture is armed without the environment variable")
	}
	// A nil capture must swallow both calls, since every call site relies on that.
	var c *capture
	c.add([]byte{1, 2}, time.Now())
	c.flush(time.Now())
}

func TestCaptureDropsSilenceAndShortSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(captureDirEnv, dir)
	c := newCapture("dev", slog.Default())
	if c == nil {
		t.Fatal("capture did not arm with the environment variable set")
	}

	// A recording starts at the first sound, so silence before anyone speaks is not buffered.
	c.add(make([]byte, 640), time.Now())
	c.add(quiet(640), time.Now())
	if len(c.pcm) != 0 {
		t.Errorf("silence before the first sound contributed %d bytes", len(c.pcm))
	}

	// Under a second of audio is a throat-clear rather than an utterance.
	c.add(loud(4), time.Now())
	c.flush(time.Now())
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("a %d-byte session was written as %d file(s)", 4, len(left))
	}
}

// Recordings are cut by length, not by session: the satellite never closes a voice session, so
// waiting for one to end would mean never writing a file at all.
func TestCaptureCutsBySpeechLength(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(captureDirEnv, dir)
	c := newCapture("kitchen", slog.Default())

	// Two chunks' worth of speech, fed a frame at a time the way the uplink delivers it.
	frame := loud(640)
	for range 2 * captureChunkBytes / len(frame) {
		c.add(frame, time.Now())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d recordings written, want 2", len(entries))
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Size() - 44; got != captureChunkBytes {
			t.Errorf("%s holds %d bytes of audio, want %d", e.Name(), got, captureChunkBytes)
		}
	}
}

// The property that matters for what the recordings sound like: quiet stretches inside an utterance
// are kept, so the audio in one file is continuous. Removing them and joining what is left leaves a
// click at every join and smears the spectrum the filterbank then reports as real.
func TestCaptureKeepsShortGapsInsideAnUtterance(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(captureDirEnv, dir)
	c := newCapture("kitchen", slog.Default())

	// A word, a breath well under the gap that ends a recording, another word.
	c.add(loud(16000), time.Now())
	c.add(quiet(3200), time.Now()) // 100 ms
	c.add(loud(16000), time.Now())

	if want := 16000 + 3200 + 16000; len(c.pcm) != want {
		t.Errorf("buffered %d bytes, want %d — the pause was not kept", len(c.pcm), want)
	}
	if c.silence != 0 {
		t.Errorf("%d bytes still count as trailing silence after speech resumed", c.silence)
	}
}

// A long enough pause ends the recording, and the silence itself is not part of it.
func TestCaptureCutsOnASilentGap(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(captureDirEnv, dir)
	c := newCapture("kitchen", slog.Default())

	c.add(loud(2*16000), time.Now())
	c.add(quiet(captureGapBytes), time.Now())

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d recordings written, want 1", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Size() - 44; got != 2*16000 {
		t.Errorf("recording holds %d bytes, want %d — trailing silence was kept", got, 2*16000)
	}
	if len(c.pcm) != 0 || c.silence != 0 {
		t.Errorf("%d bytes and %d silent bytes survived the cut", len(c.pcm), c.silence)
	}
}

func TestCaptureWritesAndResets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(captureDirEnv, dir)
	c := newCapture("kitchen", slog.Default())

	c.add(loud(2*16000), time.Now()) // one second, well above the silence floor
	c.flush(time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC))

	want := filepath.Join(dir, "kitchen-20260728-093000.000.wav")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s: %v", want, err)
	}
	// Flushing must leave nothing behind, or the next utterance would start with this one.
	if len(c.pcm) != 0 {
		t.Errorf("%d bytes survived the flush", len(c.pcm))
	}
	c.flush(time.Date(2026, 7, 28, 9, 30, 1, 0, time.UTC))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("an empty flush wrote a second file (%d total)", len(entries))
	}
}
