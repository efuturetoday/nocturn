package serve

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// captureDirEnv arms uplink capture when it names a directory. Enrolling a voice needs recordings
// from the microphone, room and echo canceller it will later be recognised through, and a laptop
// microphone is a different channel — close-talking, no beamformer, no AEC residue. This is the only
// way to obtain audio from the real one.
//
// It is off unless the variable is set, because it writes what the room said to disk. Arming it logs
// a warning, and every file written is logged by name.
const captureDirEnv = "NOCTURN_VOICE_CAPTURE"

// captureChunkBytes is how much speech makes one recording — eight seconds, comfortably more than
// the second an embedding needs and short enough to be one thought.
//
// Recordings are cut by length rather than by session, because a voice session has no useful end for
// this purpose: the satellite opens one on the wake word and never closes it, so waiting for the end
// would mean waiting for the connection to drop. Cutting by length means a person can say the wake
// word once and then simply talk, and a file appears for every eight seconds they speak.
const captureChunkBytes = 8 * 2 * 16000

// captureSilenceFloor is the mean absolute sample below which a frame counts as silence: the zeros
// the half-duplex gate sends upstream while the board is speaking, and the room between sentences.
// Low enough to keep quiet speech — an int16 full scale is 32767.
const captureSilenceFloor = 200

// captureGapBytes is how much continuous silence ends a recording — four hundred milliseconds, long
// enough not to trigger between words.
//
// Silence marks a boundary and is never removed from inside a recording. Cutting quiet stretches out
// and joining what remains leaves a discontinuity at every join: audible as a click, and spectrally a
// broadband smear that a filterbank faithfully turns into features of something nobody said. Audio
// within one file is therefore exactly what the microphone heard, gaps included.
const captureGapBytes = 400 * 2 * 16000 / 1000

// capture accumulates one session's uplink audio and writes it as a WAV file when the session ends.
//
// Silent frames are dropped. While the board plays speech its half-duplex gate sends zeros upstream
// instead of the microphone, and those stretches carry no voice — keeping them would pad every
// recording with silence that says nothing about the speaker. The result is therefore a splice of
// the parts where the microphone was live, which is what enrolment wants and is NOT a faithful
// record of the session's timeline. Nothing here should be used to reason about timing.
type capture struct {
	dir    string
	device string
	log    *slog.Logger

	mu      sync.Mutex
	pcm     []byte
	silence int // trailing silent bytes in pcm, the candidate cut point
}

// newCapture returns nil unless capture is armed, so the cost when it is off is one nil check.
func newCapture(device string, log *slog.Logger) *capture {
	dir := os.Getenv(captureDirEnv)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Error("voice capture is armed but its directory is unusable", "dir", dir, "err", err)
		return nil
	}
	log.Warn("voice capture is armed — uplink audio from this device is being written to disk",
		"dir", dir, "device", device, "disable", "unset "+captureDirEnv)
	return &capture{dir: dir, device: device, log: log}
}

// add appends a frame, and ends the recording when the talking stops or it has run long enough. A
// nil capture accepts frames and does nothing, so callers need no branch.
func (c *capture) add(pcm []byte, at time.Time) {
	if c == nil {
		return
	}
	silent := isSilent(pcm)

	c.mu.Lock()
	switch {
	case len(c.pcm) == 0 && silent:
		c.mu.Unlock() // a recording starts at the first sound, not at the wake word
		return
	case silent:
		c.silence += len(pcm)
	default:
		c.silence = 0
	}
	c.pcm = append(c.pcm, pcm...)
	done := c.silence >= captureGapBytes || len(c.pcm) >= captureChunkBytes
	c.mu.Unlock()

	if done {
		c.flush(at)
	}
}

// flush writes what has accumulated and starts a fresh recording. Calling it on a capture holding
// nothing does nothing, so the several paths a session can end by may all call it.
func (c *capture) flush(at time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	pcm := c.pcm[:len(c.pcm)-c.silence] // trailing silence belongs to neither recording
	c.pcm, c.silence = nil, 0
	c.mu.Unlock()

	if len(pcm) < 2*16000 { // under a second is a throat-clear, not an utterance
		return
	}
	name := filepath.Join(c.dir, fmt.Sprintf("%s-%s.wav", c.device, at.UTC().Format("20060102-150405.000")))
	if err := writeWAV(name, pcm); err != nil {
		c.log.Error("writing voice capture", "file", name, "err", err)
		return
	}
	c.log.Info("wrote voice capture", "file", name, "seconds", float64(len(pcm))/2/16000)
}

// isSilent reports whether a frame carries no speech worth keeping, by mean absolute amplitude.
// Mean rather than peak: a single click should not rescue an otherwise silent frame.
func isSilent(pcm []byte) bool {
	if len(pcm) < 2 {
		return true
	}
	var sum int64
	n := len(pcm) / 2
	for i := range n {
		v := int64(int16(binary.LittleEndian.Uint16(pcm[i*2:])))
		if v < 0 {
			v = -v
		}
		sum += v
	}
	return sum/int64(n) < captureSilenceFloor
}

// writeWAV wraps 16 kHz mono 16-bit little-endian PCM in the canonical 44-byte RIFF header — the
// format the uplink already carries, so the samples are copied rather than converted.
func writeWAV(path string, pcm []byte) error {
	const (
		sampleRate = 16000
		channels   = 1
		bits       = 16
	)
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)                         // fmt chunk size
	binary.LittleEndian.PutUint16(header[20:], 1)                          // PCM, uncompressed
	binary.LittleEndian.PutUint16(header[22:], channels)                   //
	binary.LittleEndian.PutUint32(header[24:], sampleRate)                 //
	binary.LittleEndian.PutUint32(header[28:], sampleRate*channels*bits/8) // byte rate
	binary.LittleEndian.PutUint16(header[32:], channels*bits/8)            // block align
	binary.LittleEndian.PutUint16(header[34:], bits)                       //
	copy(header[36:], "data")                                              //
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))           //

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(header); err != nil {
		return err
	}
	_, err = f.Write(pcm)
	return err
}
