package serve

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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

	// listenInterval is the least time between two identifications, and it exists because there was
	// none: add started a new one the moment the last finished, so while somebody spoke the daemon
	// embedded back to back — every 300 to 600 ms, each run holding every core for the better part of
	// a second. That competes with the work a conversation is made of: forwarding microphone frames
	// upstream, taking speech back, resampling it, writing it to the socket.
	//
	// Recognising is worth a burst; confirming is not. The answer converges within a handful of
	// windows and then moves by thousandths.
	listenInterval = 3 * time.Second

	// listenSettled is how long between identifications once the answer has stopped moving. It is not
	// zero because somebody else can start talking, and that has to be noticed eventually — just not
	// at the cost of pinning the machine for the rest of the conversation.
	listenSettled = 20 * time.Second

	// listenStable is how many identifications in a row must agree before the interval stretches.
	listenStable = 3

	// listenMinimum is the least speech worth an attempt at all, and it is the shortest the embedder
	// accepts rather than anything longer.
	//
	// Latency here is not computation — an embedding costs a few hundred milliseconds — it is how long
	// somebody has to talk. And they talk in bursts: while the board answers, its half-duplex gate
	// sends silence upstream so the model never hears its own voice, and nothing accumulates for as
	// long as the reply lasts. Measured on a real exchange, a first question of 1.28 s fell under a
	// two-second minimum and recognition waited out a fourteen-second answer for no gain.
	//
	// A one-second window embeds more noisily, which the running average across windows corrects
	// within a window or two. Being roughly right after the first sentence beats being precise after
	// the third.
	listenMinimum = speaker.MinSamples
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

	// Counters, so a session can be judged on what actually happened rather than on the one line it
	// printed when a name changed.
	//
	// deferred counts FRAMES that did not start an identification — because one was already running,
	// or because the interval had not elapsed. No audio is lost: every frame is appended to the window
	// before this is reached, so the number is a rate and not a loss. It is expected to be large. At
	// fifty frames a second against a three-second interval, well over a hundred frames pass between
	// two identifications by design, and only a value near zero would be worth worrying about.
	deferred int
	spent    time.Duration
	last     time.Time // when the last identification started
	stable   int       // consecutive identifications that agreed
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
	if len(l.speech) < listenMinimum {
		l.mu.Unlock()
		return
	}
	if l.busy || time.Since(l.last) < l.interval() {
		l.deferred++
		l.mu.Unlock()
		return
	}
	l.busy = true
	l.last = time.Now()
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

	started := time.Now()
	embedding, err := l.embed.Embed(window)
	if err != nil {
		l.log.Warn("could not embed a window of speech", "samples", len(window), "err", err)
		return
	}
	elapsed := time.Since(started)

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

	ranked := l.profiles.Rank(speaker.Normalize(mean))
	found := speaker.Identity{}
	if len(ranked) > 0 && ranked[0].Confidence >= speaker.DefaultThreshold {
		found = speaker.Identity{Name: ranked[0].Name, Confidence: ranked[0].Confidence}
	}

	l.mu.Lock()
	changed := found.Name != l.identity.Name
	if changed {
		l.stable = 0
	} else {
		l.stable++
	}
	l.identity = found
	l.spent += elapsed
	windows, deferred := l.seen, l.deferred
	l.mu.Unlock()

	// Every window, at debug: this is the view that answers "is it recognising me, how well, and how
	// close is the runner-up" — which one line per name change cannot show. Run with NOCTURN_LOG=debug.
	l.log.Debug("speaker window",
		"seconds", float64(len(window))/speaker.SampleRate,
		// Milliseconds as a number: the JSON handler renders a Duration as raw nanoseconds, which is
		// unreadable in exactly the log this line exists to be read in.
		"took_ms", elapsed.Milliseconds(),
		"windows", windows,
		"frames_deferred", deferred,
		"threshold", speaker.DefaultThreshold,
		"ranking", ranking(ranked))

	if changed {
		if found.Known() {
			l.log.Info("speaker recognised",
				"name", found.Name, "confidence", found.Confidence,
				"windows", windows, "runner_up", runnerUp(ranked))
		} else {
			l.log.Info("speaker no longer recognised",
				"best", ranking(ranked), "threshold", speaker.DefaultThreshold, "windows", windows)
		}
	}
}

// interval is how long to wait before identifying again. The caller holds the lock.
//
// Short while the answer is still moving, long once it has settled — the difference between finding
// out who is in the room and checking that they have not been replaced.
func (l *listener) interval() time.Duration {
	if l.stable >= listenStable {
		return listenSettled
	}
	return listenInterval
}

// summary is what a finished session is worth knowing: whether recognition kept up, how sure it
// ended up, and how much of the conversation it spent computing.
func (l *listener) summary() []any {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return []any{
		"speaker", l.identity.Name,
		"confidence", l.identity.Confidence,
		"windows", l.seen,
		"frames_deferred", l.deferred,
		"embedding_ms", l.spent.Milliseconds(),
	}
}

// ranking renders the scores compactly enough to read in a log line: "oliver=0.71 anna=0.22".
func ranking(matches []speaker.Match) string {
	if len(matches) == 0 {
		return "nobody enrolled"
	}
	var b strings.Builder
	for i, m := range matches {
		if i == 4 {
			fmt.Fprintf(&b, " (+%d more)", len(matches)-i)
			break
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%.3f", m.Name, m.Confidence)
	}
	return b.String()
}

// runnerUp is how much room the winner had. A second place just behind it is recognition that is
// about to confuse two people, and it is invisible in a single answer.
func runnerUp(matches []speaker.Match) string {
	if len(matches) < 2 {
		return "none"
	}
	return fmt.Sprintf("%s=%.3f (behind by %.3f)",
		matches[1].Name, matches[1].Confidence, matches[0].Confidence-matches[1].Confidence)
}
