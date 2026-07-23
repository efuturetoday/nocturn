package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A Wednesday afternoon (2026-07-22 is a Wednesday) and a Sunday (2026-01-04)
// so day-of-week cases can pin Sunday == 0.
var (
	wed = time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC) // min 30, hour 14, dom 22, month 7, dow 3
	sun = time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)    // dow 0 (Sunday)
	thu = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)    // dow 4 (Thursday)
)

// TestNextAfter_Wildcards_Ranges_Steps_Lists pins the cron grammar through the solver: asking for
// the next occurrence one minute BEFORE a matching time must land exactly on it, and must not land
// on it when the spec does not match.
func TestNextAfter_Wildcards_Ranges_Steps_Lists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
		when time.Time
		want bool
	}{
		{"all wildcards", "* * * * *", wed, true},
		{"too few fields (4)", "* * * *", wed, false},
		{"too many fields (6)", "* * * * * *", wed, false},
		{"exact minute match", "30 * * * *", wed, true},
		{"exact minute miss", "31 * * * *", wed, false},
		{"range hit", "0-40 * * * *", wed, true},
		{"range miss", "0-10 * * * *", wed, false},
		{"step hit (*/15 at :30)", "*/15 * * * *", wed, true},
		{"hour step miss (*/5 at hour 14)", "* */5 * * * ", wed, false},
		{"step miss on minute", "*/7 * * * *", wed, false},
		{"range step hit (0-30/10 at :30)", "0-30/10 * * * *", wed, true},
		{"range step miss (0-30/10 at :30 wrong offset)", "0-25/10 * * * *", wed, false},
		{"list hit", "0,15,30 * * * *", wed, true},
		{"list miss", "0,15,45 * * * *", wed, false},
		{"dow sunday==0 hit", "0 0 * * 0", sun, true},
		{"dow sunday==0 miss on thursday", "0 0 * * 0", thu, false},
		{"full spec exact hit", "30 14 22 7 *", wed, true},
		{"full spec month miss", "30 14 22 8 *", wed, false},
		{"full spec dom miss", "30 14 21 7 *", wed, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := nextAfter(tt.spec, tt.when.Add(-time.Minute))
			hit := ok && got.Equal(tt.when)
			if hit != tt.want {
				t.Errorf("nextAfter(%q, %v) = (%v, %v), want landing on %v = %v",
					tt.spec, tt.when.Add(-time.Minute).Format(time.RFC3339), got.Format(time.RFC3339), ok,
					tt.when.Format(time.RFC3339), tt.want)
			}
		})
	}
}

// TestNextAfter_SkipsAndRollover covers the stepping paths a minute-by-minute scan would hide:
// strictly-after semantics, hour/day/month rollover, and a spec that can never occur.
func TestNextAfter_SkipsAndRollover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
		from time.Time
		want time.Time
		ok   bool
	}{
		{
			name: "strictly after — a match at from resolves to the NEXT one",
			spec: "* * * * *",
			from: time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC),
			want: time.Date(2026, 7, 22, 14, 31, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name: "hour rollover",
			spec: "5 * * * *",
			from: time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC),
			want: time.Date(2026, 7, 22, 15, 5, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name: "day rollover",
			spec: "0 3 * * *",
			from: time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC),
			want: time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name: "month rollover to a day-of-month",
			spec: "0 0 1 * *",
			from: time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC),
			want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name: "year rollover on a fixed month",
			spec: "0 0 1 1 *",
			from: time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC),
			want: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name: "unsatisfiable — February 30th",
			spec: "0 0 30 2 *",
			from: time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC),
			ok:   false,
		},
		{
			name: "malformed — wrong field count",
			spec: "* * * *",
			from: time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC),
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := nextAfter(tt.spec, tt.from)
			if ok != tt.ok {
				t.Fatalf("nextAfter(%q, %v) ok = %v, want %v", tt.spec, tt.from.Format(time.RFC3339), ok, tt.ok)
			}
			if tt.ok && !got.Equal(tt.want) {
				t.Errorf("nextAfter(%q, %v) = %v, want %v",
					tt.spec, tt.from.Format(time.RFC3339), got.Format(time.RFC3339), tt.want.Format(time.RFC3339))
			}
		})
	}
}

// TestFireDue_DSTFallBack_DoesNotFireTwice pins the daylight-saving fall-back case: the hour is
// replayed, so the naive next occurrence of a daily schedule is the SAME wall-clock minute an hour
// later in absolute time. A daily agent must still run once that day.
func TestFireDue_DSTFallBack_DoesNotFireTwice(t *testing.T) {
	t.Parallel()

	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata for Europe/Berlin: %v", err)
	}

	fired := make(chan string, 4)
	fire := func(_ context.Context, a Agent) { fired <- a.Name }

	const spec = "30 2 * * *" // 02:30 daily — replayed on the 2026-10-25 fall-back
	agents := Set{"daily": {Name: "daily", When: spec}}
	s := NewScheduler(agents, discardLogger(), fire)

	// The FIRST 02:30 of the fall-back day, built through UTC: 02:30 is ambiguous that day and
	// time.Date resolves it to the second occurrence (CET, +01:00), which would leave nothing to
	// suppress and make this test vacuous. 00:30 UTC is unambiguously the first one (CEST, +02:00).
	at := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(berlin)
	if off := at.Format("-07:00"); off != "+02:00" {
		t.Fatalf("fixture is not the first 02:30 (offset %s) — the test would not exercise the replay", off)
	}
	due := map[string]time.Time{"daily": at}

	s.fireDue(context.Background(), s.agents.All(), due, at)

	select {
	case name := <-fired:
		if name != "daily" {
			t.Errorf("fired %q, want daily", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the firing")
	}

	// Re-armed past the replayed hour: the next run is the NEXT day, not 02:30 CET (+01:00) an hour
	// later on the same day.
	want := time.Date(2026, 10, 26, 2, 30, 0, 0, berlin)
	if got := due["daily"]; !got.Equal(want) {
		t.Errorf("re-armed to %v, want %v (a second run on the same wall-clock minute)",
			got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestPartMatch_InvalidStepOrBounds_False(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		part string
		val  int
		lo   int
		hi   int
		want bool
	}{
		{"wildcard", "*", 5, 0, 59, true},
		{"exact hit", "5", 5, 0, 59, true},
		{"exact miss", "5", 6, 0, 59, false},
		{"range hit", "1-10", 5, 0, 59, true},
		{"range miss high", "1-10", 11, 0, 59, false},
		{"range miss low", "3-10", 2, 0, 59, false},
		{"wildcard step hit", "*/15", 15, 0, 59, true},
		{"wildcard step miss", "*/15", 7, 0, 59, false},
		{"range step hit", "0-30/10", 20, 0, 59, true},
		{"range step miss", "0-30/10", 25, 0, 59, false},
		{"invalid step non-numeric", "*/abc", 5, 0, 59, false},
		{"invalid step zero", "*/0", 5, 0, 59, false},
		{"invalid step negative", "*/-2", 4, 0, 59, false},
		{"invalid range non-numeric", "a-b", 5, 0, 59, false},
		{"range missing upper bound", "5-", 5, 0, 59, false},
		{"range missing lower bound", "-5", 5, 0, 59, false},
		{"invalid literal non-numeric", "abc", 5, 0, 59, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := partMatch(tt.part, tt.val, tt.lo, tt.hi); got != tt.want {
				t.Errorf("partMatch(%q, %d, %d, %d) = %v, want %v", tt.part, tt.val, tt.lo, tt.hi, got, tt.want)
			}
		})
	}
}

// TestScheduler_FireDue_GraceWindowAndReArm drives the decision the wall clock forces after a gap
// (a laptop sleep): a due agent inside catchUpGrace still fires, one outside it is skipped rather
// than run at the wrong time, one not yet due stays silent, and every evaluated agent is re-armed
// forward of now. Firing happens in its own goroutine — a slow run must not stall the loop.
func TestScheduler_FireDue_GraceWindowAndReArm(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release) // unblock the slow fires so their goroutines exit
	fired := make(chan string, 8)
	fire := func(_ context.Context, a Agent) {
		fired <- a.Name
		<-release // simulate a slow run (a whole turn)
	}

	agents := Set{
		"onTime": {Name: "onTime", When: "* * * * *"}, // due exactly now
		"late":   {Name: "late", When: "* * * * *"},   // due inside the grace window
		"missed": {Name: "missed", When: "* * * * *"}, // due long before it — outside the window
		"future": {Name: "future", When: "* * * * *"}, // not due yet
		"manual": {Name: "manual", When: ""},          // never scheduled
	}
	s := NewScheduler(agents, discardLogger(), fire)

	now := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	due := map[string]time.Time{
		"onTime": now,
		"late":   now.Add(-catchUpGrace),               // exactly at the boundary — still runs
		"missed": now.Add(-catchUpGrace - time.Minute), // one minute past it — skipped
		"future": now.Add(time.Minute),
	}

	// If fireDue blocked on a slow fire, this call would never return and the test would time out.
	s.fireDue(context.Background(), s.agents.All(), due, now)
	if len(due) != 4 {
		t.Errorf("remaining scheduled = %d, want 4", len(due))
	}

	got := map[string]bool{}
	for i := range 2 {
		select {
		case name := <-fired:
			got[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for fire %d; got so far: %v", i, got)
		}
	}
	if !got["onTime"] || !got["late"] {
		t.Errorf("fired = %v, want both onTime and late", got)
	}

	// missed (outside the window), future (not yet due) and manual (unscheduled) stay silent.
	select {
	case name := <-fired:
		t.Errorf("unexpected extra fire: %q", name)
	case <-time.After(50 * time.Millisecond):
	}

	// Every agent whose time had come is re-armed strictly forward of now; the not-yet-due one keeps
	// its original time. A missed agent is re-armed too — skipping a firing must not unschedule it.
	for _, name := range []string{"onTime", "late", "missed"} {
		if !due[name].After(now) {
			t.Errorf("due[%q] = %v, want a time after %v", name, due[name].Format(time.RFC3339), now.Format(time.RFC3339))
		}
	}
	if want := now.Add(time.Minute); !due["future"].Equal(want) {
		t.Errorf("due[\"future\"] = %v, want it untouched at %v", due["future"].Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
