package serve

import (
	"context"
	"testing"
)

// relayed drains the target device's connection and returns the capture command it was sent.
func relayed(t *testing.T, target *conn) CaptureCommand {
	t.Helper()
	msg := recv(t, target)
	got, ok := msg.(CaptureCommand)
	if !ok {
		t.Fatalf("device received %T, want CaptureCommand", msg)
	}
	return got
}

// wantError takes one message off the caller's queue and fails unless it is a refusal.
func wantError(t *testing.T, c *conn, context string) {
	t.Helper()
	if msg := recv(t, c); msg != nil {
		if _, ok := msg.(Error); !ok {
			t.Errorf("%s produced %T, want an Error", context, msg)
		}
	}
}

// household wires a caller and a satellite onto one hub, the way an accepted connection is wired.
func household(t *testing.T, callerCan capabilities) (caller, satellite *conn) {
	t.Helper()
	h := newHub(defaultHeartbeat)

	caller = testConn()
	caller.hub = h
	caller.device = "phone"
	caller.can = callerCan

	satellite = testConn()
	satellite.hub = h
	satellite.device = "kitchen"
	satellite.can = capabilitiesOf("appliance")

	h.add(caller)
	h.add(satellite)
	return caller, satellite
}

func TestCaptureRelaysToTheNamedDevice(t *testing.T) {
	caller, satellite := household(t, capabilities{captureAudio: true})

	for _, cmd := range []string{"capture.start", "capture.stop"} {
		caller.captureCmd(context.Background(), cmd, []byte(`{"cmd":"`+cmd+`","device":"kitchen"}`))
		if got := relayed(t, satellite); got.Type != cmd {
			t.Errorf("device received %q, want %q", got.Type, cmd)
		}
	}
}

// The check that carries the weight: a speaker in the hallway must not be able to switch on the
// microphone in the bedroom. Authority is the CALLER's, and an appliance has none.
func TestCaptureRefusesAnAppliance(t *testing.T) {
	caller, satellite := household(t, capabilitiesOf("appliance"))

	caller.captureCmd(context.Background(), "capture.start", []byte(`{"cmd":"capture.start","device":"kitchen"}`))

	msg := recv(t, caller)
	if _, ok := msg.(Error); !ok {
		t.Fatalf("caller received %T, want an Error", msg)
	}
	select {
	case got := <-satellite.control:
		t.Fatalf("the target was sent %v despite the refusal", got)
	default:
	}
}

func TestCaptureRequiresADevice(t *testing.T) {
	caller, _ := household(t, capabilities{captureAudio: true})

	for _, body := range []string{
		`{"cmd":"capture.start"}`,             // no device named
		`{"cmd":"capture.start","device":""}`, // named as empty
		`{`,                                   // not JSON at all
	} {
		caller.captureCmd(context.Background(), "capture.start", []byte(body))
		wantError(t, caller, body)
	}
}

func TestCaptureRejectsAnUnknownCommand(t *testing.T) {
	caller, _ := household(t, capabilities{captureAudio: true})

	caller.captureCmd(context.Background(), "capture.explode", []byte(`{"device":"kitchen"}`))
	wantError(t, caller, "capture.explode")
}

// A device that is not connected is not an error: the daemon cannot know whether an unreachable
// device is recording, and inventing an answer would be worse than saying nothing.
func TestCaptureForAnAbsentDeviceIsQuiet(t *testing.T) {
	caller, _ := household(t, capabilities{captureAudio: true})

	caller.captureCmd(context.Background(), "capture.start", []byte(`{"device":"garage"}`))
	select {
	case msg := <-caller.control:
		t.Fatalf("caller received %v, want silence", msg)
	default:
	}
}
