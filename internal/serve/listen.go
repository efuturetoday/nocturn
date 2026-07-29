package serve

import (
	"encoding/binary"
	"log/slog"
	"sync"

	"github.com/efuturetoday/nocturn/internal/speaker"
)

// Recognising who is speaking, continuously, from the audio a session is already sending.
//
// It runs beside the conversation rather than in front of it. Nothing waits for an answer: a session
// opens the moment the wake word arrives, and by the time anything asks who is talking — a tool
// acting on someone's behalf, or the model calling whoami — several seconds of speech have gone past
// and the answer is ready. Identifying before the session opened would have bought the same answer
// for the price of a pause at the start of every conversation.

const (
	// listenWindow is how much recent speech an identification looks at. Long enough that the
	// embedding is about the speaker rather than about which vowels they happened to use — the
	// satellite measurement scored 0.63 on clips of one to five seconds, and short clips are why.
	listenWindow = 5 * speaker.SampleRate

	// listenMinimum is the least speech worth an attempt at all.
	listenMinimum = 2 * speaker.SampleRate
)

// listener turns a device's uplink into a running answer to "who is this".
type listener struct {
	embed    *speaker.Embedder
	profiles *speaker.Profiles
	log      *slog.Logger

	mu       sync.Mutex
	speech   []int16 // the recent window, silence excluded
	busy     bool    // an identification is running; only one at a time
	mean     []float32
	seen     int // embeddings folded into mean
	identity speaker.Identity
}

// newListener returns nil unless this daemon can actually recognise anyone — no model configured, or
// no voices enrolled. Nil is usable: add does nothing and who reports an unknown speaker, so every
// path downstream behaves exactly as it does today.
func newListener(embed *speaker.Embedder, profiles *speaker.Profiles, log *slog.Logger) *listener {
	if embed == nil || profiles == nil || len(profiles.Names()) == 0 {
		return nil
	}
	return &listener{embed: embed, profiles: profiles, log: log}
}

// who is what the voice session consults. Safe on a nil listener.
func (l *listener) who() speaker.Identity {
	if l == nil {
		return speaker.Identity{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.identity
}

// add takes one uplink frame.
//
// This runs on the connection's read loop, which must not stall — an embedding costs a few hundred
// milliseconds, so the work happens on its own goroutine and this only ever copies. Silence is
// skipped: the half-duplex gate sends zeros upstream while the board speaks, and a window full of
// those describes nobody.
func (l *listener) add(pcm []byte) {
	if l == nil || isSilent(pcm) {
		return
	}

	l.mu.Lock()
	for i := 0; i+1 < len(pcm); i += 2 {
		l.speech = append(l.speech, int16(binary.LittleEndian.Uint16(pcm[i:])))
	}
	if over := len(l.speech) - listenWindow; over > 0 {
		l.speech = l.speech[over:]
	}
	if l.busy || len(l.speech) < listenMinimum {
		l.mu.Unlock()
		return
	}
	l.busy = true
	window := make([]int16, len(l.speech))
	copy(window, l.speech)
	l.mu.Unlock()

	go l.identify(window)
}

// identify embeds one window and folds it into what is known so far.
//
// Averaging across windows rather than trusting the newest: this is the same reason enrolment keeps
// several recordings, and it is what turns a stream of short, noisy looks into one good one. The
// average is over the whole session, so confidence climbs the longer somebody talks.
func (l *listener) identify(window []int16) {
	defer func() {
		l.mu.Lock()
		l.busy = false
		l.mu.Unlock()
	}()

	embedding, err := l.embed.Embed(window)
	if err != nil {
		l.log.Debug("could not embed a window of speech", "err", err)
		return
	}

	l.mu.Lock()
	if l.mean == nil {
		l.mean = make([]float32, len(embedding))
	}
	if len(l.mean) != len(embedding) {
		l.mu.Unlock()
		return // a different model mid-session; nothing sensible to average
	}
	l.seen++
	for i, v := range embedding {
		l.mean[i] += (v - l.mean[i]) / float32(l.seen)
	}
	mean := make([]float32, len(l.mean))
	copy(mean, l.mean)
	l.mu.Unlock()

	found := l.profiles.Identify(speaker.Normalize(mean), speaker.DefaultThreshold)

	l.mu.Lock()
	changed := found.Name != l.identity.Name
	l.identity = found
	l.mu.Unlock()

	if changed {
		l.log.Info("speaker changed", "name", found.Name, "confidence", found.Confidence, "windows", l.seen)
	}
}
