// Package timecap is the clock capability group: the one tool, time.now, that
// tells the guest what time it is.
//
// It is deliberately NOT gated. A time read leaks nothing and mutates nothing —
// it carries zero authority, like skill.read's ingestion — so it needs no Guard,
// no broker, and no HITL: it is a plain tool.Tool the model/script/plugin can call
// freely, and it is still observed through the shared Registry like any other call.
// It exists as a host primitive only because the sandbox guest has no clock of its
// own (the QuickJS build has no Date.now / wall clock), so without this a skill
// could not answer "what is due today".
package timecap

import (
	"context"
	"encoding/json"
	"time"

	"github.com/efuturetoday/nocturn/internal/tool"
)

// Clock provides the time.now tool. Now is injectable so tests are deterministic
// (the same WithClock pattern the rest of the codebase uses); a nil Now falls back
// to time.Now.
type Clock struct {
	Now func() time.Time
}

// New builds the clock capability group. Pass a fixed Now in tests; omit for the
// real wall clock.
func New() *Clock { return &Clock{} }

func (c *Clock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Tools exposes the clock as a single ungated tool.
func (c *Clock) Tools() []tool.Tool {
	return []tool.Tool{c.nowTool()}
}

func (c *Clock) nowTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: "time.now",
			Description: "Return the current date and time. Returns a JSON object " +
				"{unix, iso, utc, timezone, offset_seconds} — iso is local time, utc is UTC.",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Invoke: func(_ context.Context, _ string) (string, error) {
			t := c.now()
			zone, offset := t.Zone()
			out, err := json.Marshal(struct {
				Unix          int64  `json:"unix"`
				ISO           string `json:"iso"`
				UTC           string `json:"utc"`
				Timezone      string `json:"timezone"`
				OffsetSeconds int    `json:"offset_seconds"`
			}{
				Unix:          t.Unix(),
				ISO:           t.Format(time.RFC3339),
				UTC:           t.UTC().Format(time.RFC3339),
				Timezone:      zone,
				OffsetSeconds: offset,
			})
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	}
}
