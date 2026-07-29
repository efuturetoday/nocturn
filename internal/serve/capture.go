package serve

import (
	"context"
	"encoding/json"
	"time"
)

// The capture domain: asking an appliance to record its own microphone, so a voice can be enrolled
// through the channel it will later be recognised through.
//
// It is deliberately not part of the voice domain. A voice session opens a live model, bills for
// every second, streams the room to a provider, and mutes the microphone upstream whenever the board
// speaks — all of which enrolment neither needs nor wants. What it needs is the microphone and
// nothing else, which is a different thing to ask for and therefore a different command.
//
// Nothing here writes audio. The device streams on the uplink it already has, and the daemon writes
// it exactly as it writes speech during a conversation — see enroll.go, and note the recording only
// happens at all when NOCTURN_VOICE_CAPTURE armed it.

// CaptureStart asks a device to begin streaming its microphone. Device is named rather than implied,
// because the connection asking is not the one recording: enrolment is driven from a phone or a
// command line, and the microphone is in the hallway.
type CaptureStart struct {
	Cmd    string `json:"cmd"`
	Device string `json:"device"`
}

// CaptureStop ends it. The device also stops on its own after a bounded time, so a caller that dies
// mid-recording cannot leave a microphone streaming — this is the polite path, not the only one.
type CaptureStop struct {
	Cmd    string `json:"cmd"`
	Device string `json:"device"`
}

// CaptureCommand is what reaches the device. The wire shape is flat and the device matches on the
// command string alone, since it already knows which device it is.
type CaptureCommand struct {
	Type string `json:"type"` // "capture.start" | "capture.stop"
}

// captureCmd routes the capture domain.
func (c *conn) captureCmd(ctx context.Context, cmd string, data []byte) {
	// Recording a room is not something an appliance may ask of another appliance: a speaker in the
	// hallway switching on the microphone in the bedroom is exactly the shape this refuses. The check
	// is on the CALLER's class, and the answer says nothing about which devices exist.
	if !c.can.captureAudio {
		c.badRequest(ctx, "this device may not start a recording")
		return
	}

	var m struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Device == "" {
		c.badRequest(ctx, "bad "+cmd+": a device is required")
		return
	}

	switch cmd {
	case "capture.start", "capture.stop":
		// control reaches the device's newest connection, or nobody when it is offline. Silence is
		// the honest answer: the daemon cannot know whether a device it cannot reach is recording.
		c.hub.control(m.Device, CaptureCommand{Type: cmd})
		c.log.Info("capture command relayed", "cmd", cmd, "target", m.Device)

		// Stopping ends the last recording here too. Nothing else would: recordings are cut by
		// silence or by length, and a device that has stopped sending produces neither — the tail
		// would sit in a buffer until the connection happened to drop.
		if cmd == "capture.stop" {
			if target := c.hub.newest(m.Device); target != nil {
				target.capture.flush(time.Now())
			}
		}
	default:
		c.badRequest(ctx, "unknown command: "+cmd)
	}
}
