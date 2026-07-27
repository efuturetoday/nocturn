package serve

import (
	"sync"
	"time"
)

// A live model produces speech far faster than it is spoken: a sentence arrives as one burst and
// then nothing. Handing that to a device as it lands makes the device's problem — it needs seconds
// of buffer to absorb a burst, and a codec that runs dry between bursts repeats its last block,
// which is heard as a skipping record.
//
// So the burst is held here and metered out at the rate speech is actually consumed. The device then
// needs only enough buffer for network jitter, and never sees a burst at all.
//
// The reason that matters most is not memory. Audio queued ahead on a device is audio that cannot be
// taken back: interrupt it and the speaker keeps talking for however much is already buffered, and
// whatever reached the codec's DMA is unstoppable. Holding the backlog here means an interruption
// discards it in one assignment, and only the device's small jitter buffer is left to flush —
// which is the difference between a satellite that stops when spoken over and one that talks on.
const (
	// One frame, at 16 kHz mono PCM16. Espressif's guidance for realtime audio is 20 to 40 ms a
	// chunk, and this sits inside it.
	paceFrameMS    = 32
	paceFrameBytes = 16000 * 2 * paceFrameMS / 1000
)

// pacer meters a burst of speech out at the rate it is heard.
type pacer struct {
	send func([]byte)

	mu   sync.Mutex
	buf  []byte
	stop chan struct{}
	once sync.Once
}

func newPacer(send func([]byte)) *pacer {
	p := &pacer{send: send, stop: make(chan struct{})}
	go p.run()
	return p
}

// Play adds speech to the backlog. It never blocks: the caller is the session's event loop, and the
// backlog is bounded by the model's own turn rather than by anything that could run away.
func (p *pacer) Play(pcm []byte) {
	p.mu.Lock()
	p.buf = append(p.buf, pcm...)
	p.mu.Unlock()
}

// Drop discards the backlog — a barge-in. What is queued answers a question the person has already
// abandoned.
func (p *pacer) Drop() {
	p.mu.Lock()
	p.buf = p.buf[:0]
	p.mu.Unlock()
}

// Close stops the pacer. It is safe to call more than once.
func (p *pacer) Close() { p.once.Do(func() { close(p.stop) }) }

func (p *pacer) run() {
	tick := time.NewTicker(paceFrameMS * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
			p.mu.Lock()
			n := min(len(p.buf), paceFrameBytes)
			var frame []byte
			if n > 0 {
				frame = make([]byte, n)
				copy(frame, p.buf[:n])
				p.buf = p.buf[n:]
			}
			p.mu.Unlock()
			// Nothing to send is not something to report: between turns there is simply no speech,
			// and the device fills its own silence.
			if frame != nil {
				p.send(frame)
			}
		}
	}
}
