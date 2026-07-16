package timecap_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/timecap"
)

func TestClock_Now(t *testing.T) {
	// A fixed clock in a known zone makes every field deterministic.
	zone := time.FixedZone("TST", 2*3600) // +02:00
	fixed := time.Date(2026, 7, 17, 15, 4, 5, 0, zone)
	c := &timecap.Clock{Now: func() time.Time { return fixed }}

	tools := c.Tools()
	if len(tools) != 1 || tools[0].Name != "time.now" {
		t.Fatalf("tools = %+v, want one time.now", tools)
	}

	out, err := tools[0].Invoke(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("time.now: %v", err)
	}
	var got struct {
		Unix          int64  `json:"unix"`
		ISO           string `json:"iso"`
		UTC           string `json:"utc"`
		Timezone      string `json:"timezone"`
		OffsetSeconds int    `json:"offset_seconds"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if got.Unix != fixed.Unix() {
		t.Errorf("unix = %d, want %d", got.Unix, fixed.Unix())
	}
	if got.ISO != "2026-07-17T15:04:05+02:00" {
		t.Errorf("iso = %q", got.ISO)
	}
	if got.UTC != "2026-07-17T13:04:05Z" {
		t.Errorf("utc = %q", got.UTC)
	}
	if got.Timezone != "TST" || got.OffsetSeconds != 7200 {
		t.Errorf("zone = %q offset = %d, want TST/7200", got.Timezone, got.OffsetSeconds)
	}
}

// A nil Now falls back to the real wall clock without panicking.
func TestClock_DefaultNow(t *testing.T) {
	out, err := timecap.New().Tools()[0].Invoke(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("time.now: %v", err)
	}
	var got struct {
		Unix int64 `json:"unix"`
	}
	json.Unmarshal([]byte(out), &got)
	if got.Unix <= 0 {
		t.Errorf("unix = %d, want a real timestamp", got.Unix)
	}
}
