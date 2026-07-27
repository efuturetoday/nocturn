package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/voice"
)

// The voice domain: speech rides this connection beside the tagged JSON rather than getting an
// endpoint of its own.
//
// /ws is already a multiplexer — one connection per device carrying chat, agents, approvals and
// reminders — so a second socket would need a second copy of every rule that hangs off a device's
// connection: the approval broker attach, the hub, last-used, presence. It would also mean the phone
// needing another connection later to do what a satellite does, since voice is a domain on the
// protocol it already speaks.
//
// Control is JSON like everything else; the audio itself is binary frames, which are the only frames
// on this socket that are not commands.

// VoiceWake asks for a spoken session on this device. It carries no audio: the frames that follow do.
type VoiceWake struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// VoiceEnd closes the spoken session. A device that simply stops sending is ended by its silence
// timer instead, so this is the polite path rather than the only one.
type VoiceEnd struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// VoiceState reports what the session is doing, so a device can show it without inferring. Sent on
// every transition.
type VoiceState struct {
	Type  string `json:"type"` // always "voice.state"
	Ws    string `json:"ws"`
	State string `json:"state"` // "listening" | "idle"
}

// VoiceInterrupt tells the device to drop what it has buffered but not yet played — the barge-in
// signal. It is the one control message the audio path itself produces.
type VoiceInterrupt struct {
	Type string `json:"type"` // always "voice.interrupt"
}

// voice routes the voice domain.
func (c *conn) voice(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "voice.wake":
		var m VoiceWake
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad voice.wake")
			return
		}
		c.voiceWake(ctx, m)
	case "voice.end":
		var m VoiceEnd
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad voice.end")
			return
		}
		c.voiceEnd(ctx, m)
	default:
		c.badRequest(ctx, "unknown voice command: "+cmd)
	}
}

func (c *conn) voiceWake(ctx context.Context, m VoiceWake) {
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	sessions := ws.VoiceSessions()
	if sessions == nil {
		// No live model is configured in this process. Saying so beats opening a session that can
		// never answer and leaving the device waiting for speech that will not come.
		c.badRequest(ctx, "voice is not configured on this daemon")
		return
	}

	// The pacer belongs to this session and is stopped with it, so a device that hangs up does not
	// leave a ticker metering speech to nobody.
	device := c.device
	hub := c.hub
	sink := &deviceSink{pace: newPacer(func(frame []byte) { hub.send(device, frame) }), hub: hub, device: device}

	// Nothing is seeded. A spoken exchange starts fresh rather than resuming whatever was last said,
	// because the person at a speaker has no way to see what it thinks the context is.
	sessions.Start(device, sink, nil, func(_ string, transcript []agentkit.Message, err error) {
		sink.pace.Close()
		c.log.Info("voice session finished", "messages", len(transcript), "err", err)
		hub.control(device, VoiceState{Type: "voice.state", Ws: m.Ws, State: "idle"})
	})
	c.send(ctx, VoiceState{Type: "voice.state", Ws: m.Ws, State: "listening"})
}

func (c *conn) voiceEnd(ctx context.Context, m VoiceEnd) {
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	if sessions := ws.VoiceSessions(); sessions != nil {
		sessions.Stop(c.device)
	}
}

// audioIn hands a binary frame to whichever workspace has a session open for this device.
//
// The frame carries no workspace of its own: a device has one conversation at a time, so the
// alternative would be a routing header on every twenty-millisecond chunk to say something the wake
// word already said.
func (c *conn) audioIn(pcm []byte) {
	for _, ws := range c.spaces {
		if sessions := ws.VoiceSessions(); sessions != nil && sessions.Active(c.device) {
			sessions.Feed(c.device, pcm)
			return
		}
	}
	// Audio with no session open: the device is streaming into nothing. Not an error — a wake word
	// may have been cancelled while frames were already in flight.
}

// endVoice closes any session this device has, for when its connection goes away.
//
// This is the one place a voice session is tied to a connection, and deliberately: audio is worth
// nothing with nobody to play it to, and a live model bills for every second. Chat sessions
// deliberately survive the same event, because their answer is still worth having later.
func (c *conn) endVoice() {
	for _, ws := range c.spaces {
		if sessions := ws.VoiceSessions(); sessions != nil {
			sessions.Stop(c.device)
		}
	}
}

// deviceSink is the writing half of a device: where a session's speech and its barge-in signal go.
//
// It addresses the device rather than the connection that started the session, so a reconnect
// mid-conversation reaches the new socket. The session itself is unaware either way.
type deviceSink struct {
	pace   *pacer
	hub    *hub
	device string
}

var _ voice.Sink = (*deviceSink)(nil)

// Play converts one chunk of speech to the rate the device runs at and hands it to the pacer, which
// releases it at the rate speech is heard rather than the rate a model produces it.
func (s *deviceSink) Play(pcm []byte) error {
	s.pace.Play(downsample24to16(pcm))
	return nil
}

// Interrupt drops the backlog here first, then tells the device to drop its own.
//
// Order matters: the message travels on the control queue and overtakes queued speech, so a device
// that flushed before this side stopped sending would immediately be handed more of the reply it was
// just told to abandon.
func (s *deviceSink) Interrupt() error {
	s.pace.Drop()
	s.hub.control(s.device, VoiceInterrupt{Type: "voice.interrupt"})
	return nil
}

// downsample24to16 converts the model's 24 kHz output to the 16 kHz a satellite runs at.
//
// It happens here rather than on the device because the ratio is exactly 3:2 and this side has the
// CPU to spare — a board's two cores are already carrying an audio front end, and a stall there is
// what breaks its echo cancellation. Three input samples become two output samples by linear
// interpolation, which for speech is indistinguishable from anything more careful.
func downsample24to16(pcm []byte) []byte {
	const inRate, outRate = 24000, 16000

	in := len(pcm) / 2
	if in < 2 {
		return pcm
	}
	sample := func(i int) int32 { return int32(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8)) }

	out := in * outRate / inRate
	res := make([]byte, out*2)
	for j := range out {
		// Position in the input, in 1/1000ths of a sample, to keep this integer-only.
		pos := j * inRate * 1000 / outRate
		i, frac := pos/1000, int32(pos%1000)
		next := i + 1
		if next >= in {
			next = in - 1
		}
		v := sample(i) + (sample(next)-sample(i))*frac/1000
		res[2*j] = byte(uint16(v))
		res[2*j+1] = byte(uint16(v) >> 8)
	}
	return res
}
