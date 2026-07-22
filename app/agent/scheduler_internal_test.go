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

func TestCronMatches_Wildcards_Ranges_Steps_Lists(t *testing.T) {
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
			if got := cronMatches(tt.spec, tt.when); got != tt.want {
				t.Errorf("cronMatches(%q, %v) = %v, want %v", tt.spec, tt.when.Format(time.RFC3339), got, tt.want)
			}
		})
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

// TestScheduler_Tick_FiresMatchingAgentsInGoroutine verifies that tick fires exactly the agents
// whose schedule matches (skipping When==""), that firing happens in its own goroutine (a slow fire
// does not stall the loop), and that non-matching schedules are not fired.
func TestScheduler_Tick_FiresMatchingAgentsInGoroutine(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release) // unblock the slow fires so their goroutines exit
	fired := make(chan string, 8)
	fire := func(_ context.Context, a Agent) {
		fired <- a.Name
		<-release // simulate a slow run (a whole turn)
	}

	agents := Set{
		"match1":  {Name: "match1", When: "* * * * *"},
		"manual":  {Name: "manual", When: ""},           // manual only -> skipped
		"match2":  {Name: "match2", When: "* * * * *"},
		"nomatch": {Name: "nomatch", When: "30 4 * * *"}, // won't match 12:00
	}
	s := NewScheduler(agents, discardLogger(), fire)

	tickTime := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	// If tick blocked on the slow fire, this call would never return and the test would time out.
	s.tick(context.Background(), tickTime)

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-fired:
			got[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for fire %d; got so far: %v", i, got)
		}
	}
	if !got["match1"] || !got["match2"] {
		t.Errorf("fired = %v, want both match1 and match2", got)
	}

	// No further agent should fire (manual + nomatch stay silent).
	select {
	case name := <-fired:
		t.Errorf("unexpected extra fire: %q", name)
	case <-time.After(50 * time.Millisecond):
	}
}
