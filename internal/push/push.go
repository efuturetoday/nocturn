// Package push is the native mobile-push side of the companion app: a registry of the
// devices that have registered to receive pushes, and a provider-agnostic Sender port that
// delivers one. It carries NO approval authority — a push is only a WAKE ("you have a
// pending approval"); the actual approve/deny happens in-app over the authenticated
// WebSocket, so the signed HITL token never leaves the daemon.
//
// The Registry is a self-contained STATE service (persisted, its own synchronization), like
// grantstore: callers deal in device tokens, never the file. The Sender is a port — the FCM
// adapter (iOS via APNs, Android natively) implements it; tests use a fake.
package push

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Message is one push to deliver. Title/Body are the user-visible notification; Data is an
// opaque key/value payload the app reads on tap (e.g. the workspace to open). It never
// carries an approval token — resolution is in-app over the WebSocket.
type Message struct {
	Title string
	Body  string
	Data  map[string]string
}

// Sender delivers a Message to a set of device tokens. The FCM adapter implements it; a nil
// Sender (no provider configured) means push is simply off.
type Sender interface {
	Send(ctx context.Context, m Message, tokens []string) error
}

// Device is one registered device — its push token and a human label.
type Device struct {
	Token string    `json:"token"`
	Name  string    `json:"name"`
	Added time.Time `json:"added"`
}

// Registry owns the set of registered devices, persisted to a 0600 file so registrations
// survive a restart. It is safe for concurrent use (multiple app clients register at once).
type Registry struct {
	path string

	mu      sync.RWMutex
	devices map[string]Device // token -> device
}

// LoadRegistry reads the registry at path (an absent or unreadable file yields an empty
// registry — fail-safe: nothing registered, so nothing is pushed).
func LoadRegistry(path string) *Registry {
	r := &Registry{path: path, devices: map[string]Device{}}
	if b, err := os.ReadFile(path); err == nil {
		var stored []Device
		if json.Unmarshal(b, &stored) == nil {
			for _, d := range stored {
				if d.Token != "" {
					r.devices[d.Token] = d
				}
			}
		}
	}
	return r
}

// Register adds (or refreshes) a device by its push token and persists. A repeat token
// updates the name/time rather than duplicating.
func (r *Registry) Register(token, name string) error {
	if token == "" {
		return nil
	}
	r.mu.Lock()
	r.devices[token] = Device{Token: token, Name: name, Added: time.Now()}
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	return r.persist(snapshot)
}

// Unregister removes a device by token and persists.
func (r *Registry) Unregister(token string) error {
	r.mu.Lock()
	delete(r.devices, token)
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	return r.persist(snapshot)
}

// Tokens returns every registered device token (a copy).
func (r *Registry) Tokens() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.devices))
	for t := range r.devices {
		out = append(out, t)
	}
	return out
}

func (r *Registry) snapshotLocked() []Device {
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, d)
	}
	return out
}

// persist writes the given snapshot atomically-ish (write + rename) at 0600. I/O happens
// outside the lock (the snapshot was taken under it), so a slow disk never blocks readers.
func (r *Registry) persist(devices []Device) error {
	b, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}
