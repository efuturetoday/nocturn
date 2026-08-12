package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// Seeing and revoking the household's devices.
//
// Revocation is the exit from the one state that had none. A phone that is lost, sold or wiped keeps
// a working bearer for as long as the registry exists, and until now the only way to take it back was
// to edit devices.json by hand and restart the daemon — a remedy nobody discovers at the moment they
// need it, and one that requires shell access to a machine the user may be nowhere near.
//
// On the WebSocket rather than as HTTP routes, deliberately. The device doing the forgetting is by
// definition already paired and connected; POST /devices is HTTP only because an appliance being
// enrolled has no socket of its own yet. A second surface for a job the existing one does would be
// two things to keep in step.

// DeviceList requests the household's devices (client → server).
//
// Gated on the same capability as enrolling. Adding a device and removing one are the same authority
// pointed in opposite directions, so a holder that may not do the first has no business doing the
// second — and the list is also the household's shape, which is not a stranger's to read.
type DeviceList struct {
	Cmd string `json:"cmd"`
}

// DeviceForget removes one device by id (client → server).
type DeviceForget struct {
	Cmd string `json:"cmd"`
	ID  string `json:"id"`
}

// DeviceListResult carries the household's devices (server → client). Bearer hashes never appear —
// auth.Store.List blanks them at the source.
type DeviceListResult struct {
	Type    string        `json:"type"`
	Devices []auth.Device `json:"devices"`
	// Self is the id of the device this connection belongs to, so a client can mark "this device" and
	// warn before signing itself out. Cheaper and more honest than making the client guess by name.
	Self string `json:"self"`
}

// device dispatches a device.* action.
func (c *conn) deviceCmd(ctx context.Context, cmd string, data []byte) {
	if !c.can.enrol {
		c.badRequest(ctx, "this device may not manage the household's devices")
		return
	}
	switch cmd {
	case "device.list":
		c.sendDevices(ctx)
	case "device.forget":
		var m DeviceForget
		if err := json.Unmarshal(data, &m); err != nil || m.ID == "" {
			c.badRequest(ctx, "bad device.forget")
			return
		}
		gone, err := c.devices.Forget(m.ID)
		if err != nil {
			c.failed(ctx, "forget the device", err)
			return
		}
		if !gone {
			c.badRequest(ctx, "no such device")
			return
		}
		// Before anything else, and before the caller is told it worked: the row is gone, but the
		// sockets that row admitted are still open and still carry the capabilities they resolved at
		// accept. A revocation that leaves the stolen phone's connection alive has revoked nothing
		// that matters.
		if n := c.hub.closeDevice(m.ID); n > 0 {
			c.log.Info("closed the revoked device's connections", "device", m.ID, "conns", n)
		}
		// Let the daemon repair what it owns before anyone is told the registry moved. Forgetting the
		// command line's own row would otherwise leave `nocturn pair` answering 401 until a restart;
		// this makes that a rotation instead — a fresh credential in the same 0600 file, with the
		// copy that was revoked still dead.
		c.hub.notifyDevicesChanged()
		// To every device that may see the roster, not just the one that asked: a household with two
		// admin screens open should not show two different answers.
		c.hub.broadcastTo(func(o *conn) bool { return o.can.enrol }, DeviceListResult{
			Type: "device.list", Devices: c.devices.List(),
		})
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// sendDevices answers this connection with the roster, tagged with which entry is itself.
func (c *conn) sendDevices(ctx context.Context) {
	c.send(ctx, DeviceListResult{Type: "device.list", Devices: c.devices.List(), Self: c.device})
}
