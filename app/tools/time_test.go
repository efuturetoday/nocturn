package tools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/app/tools"
)

// TestTimeNow_Ungated_JSONShape proves time_now carries zero authority (no gate.Check: it runs even
// under a deny-all policy) and returns the fixed {unix, iso, utc, timezone, offset_seconds} shape, with
// utc parseable as RFC3339.
func TestTimeNow_Ungated_JSONShape(t *testing.T) {
	timeNow := toolFrom(t, tools.Config{}, "time_now")

	// A deny-all policy would block any GATED tool; time_now must ignore it entirely.
	ctx := gate.With(context.Background(), gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Denied() }), nil, nil)
	out, err := timeNow.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("time_now errored under a deny-all policy (should be ungated): %v", err)
	}

	var got struct {
		Unix          int64  `json:"unix"`
		ISO           string `json:"iso"`
		UTC           string `json:"utc"`
		Timezone      string `json:"timezone"`
		OffsetSeconds int    `json:"offset_seconds"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("time_now output not JSON: %v (%q)", err, out)
	}
	if got.Unix == 0 || got.ISO == "" || got.UTC == "" || got.Timezone == "" {
		t.Fatalf("time_now returned an incomplete shape: %+v", got)
	}
	if _, err := time.Parse(time.RFC3339, got.UTC); err != nil {
		t.Fatalf("utc field is not RFC3339: %q (%v)", got.UTC, err)
	}
}
