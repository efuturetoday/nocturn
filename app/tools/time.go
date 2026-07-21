package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

// timeTool builds time_now. It is UNGATED — zero authority, no Target, no gate.Check — because a
// sandboxed guest has no wall-clock of its own and the host time reveals nothing sensitive, yet
// almost every agent needs to know the current date/time. Returns a JSON object so a caller gets the
// unix seconds, an RFC3339 string, and the host timezone in one shot.
func timeTool() (agentkit.Tool, error) {
	return agentkit.NewTool("time_now",
		"Return the current date and time. Takes no arguments. Returns a JSON object "+
			`{"unix", "iso" (local RFC3339), "utc", "timezone", "offset_seconds"}.`,
		func(context.Context, string) (string, error) {
			now := time.Now()
			zone, offset := now.Zone()
			out, err := json.Marshal(struct {
				Unix          int64  `json:"unix"`
				ISO           string `json:"iso"`
				UTC           string `json:"utc"`
				Timezone      string `json:"timezone"`
				OffsetSeconds int    `json:"offset_seconds"`
			}{now.Unix(), now.Format(time.RFC3339), now.UTC().Format(time.RFC3339), zone, offset})
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	)
}
