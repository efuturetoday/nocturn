package agent

import (
	"testing"
	"time"
)

func TestParseCron_Errors(t *testing.T) {
	for _, expr := range []string{
		"",            // empty
		"* * * *",     // 4 fields
		"* * * * * *", // 6 fields
		"60 * * * *",  // minute out of range
		"* 24 * * *",  // hour out of range
		"* * 0 * *",   // dom below 1
		"* * * 13 *",  // month out of range
		"* * * * 8",   // dow above 7
		"a * * * *",   // bad token
		"5-1 * * * *", // reversed range
		"*/0 * * * *", // zero step
		"1,, * * * *", // empty list term
	} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) accepted, want error", expr)
		}
	}
}

func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestSchedule_Matches(t *testing.T) {
	cases := []struct {
		expr string
		t    time.Time
		want bool
	}{
		{"0 7 * * *", at(2026, 7, 15, 7, 0), true},  // 07:00 any day
		{"0 7 * * *", at(2026, 7, 15, 7, 1), false}, // wrong minute
		{"0 7 * * *", at(2026, 7, 15, 8, 0), false}, // wrong hour
		{"*/15 * * * *", at(2026, 7, 15, 9, 30), true},
		{"*/15 * * * *", at(2026, 7, 15, 9, 31), false},
		{"0,30 * * * *", at(2026, 7, 15, 9, 30), true},
		{"0 9-17 * * *", at(2026, 7, 15, 17, 0), true},
		{"0 9-17 * * *", at(2026, 7, 15, 18, 0), false},
		// Vixie OR: "13th OR Friday". 2026-01-13 is a Tuesday (the 13th) → matches on dom.
		{"0 0 13 * 5", at(2026, 1, 13, 0, 0), true},
		// 2026-01-02 is a Friday, day 2 → matches on dow (not the 13th).
		{"0 0 13 * 5", at(2026, 1, 2, 0, 0), true},
		// 2026-01-05 is a Monday, day 5 → neither → no match.
		{"0 0 13 * 5", at(2026, 1, 5, 0, 0), false},
		// Both "*": AND semantics = every day.
		{"0 0 * * *", at(2026, 1, 5, 0, 0), true},
		// dow restricted only: weekdays. 2026-01-02 Friday matches; 2026-01-03 Saturday not.
		{"0 8 * * 1-5", at(2026, 1, 2, 8, 0), true},
		{"0 8 * * 1-5", at(2026, 1, 3, 8, 0), false},
	}
	for _, c := range cases {
		s, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("ParseCron(%q): %v", c.expr, err)
		}
		if got := s.Matches(c.t); got != c.want {
			t.Errorf("%q.Matches(%s) = %v, want %v", c.expr, c.t.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestCronExpr(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		`cron("0 7 * * *")`:   {"0 7 * * *", true},
		`cron('*/5 * * * *')`: {"*/5 * * * *", true},
		`cron(0 0 * * *)`:     {"0 0 * * *", true},
		"manual":              {"", false},
		"webhook":             {"", false},
		"cron()":              {"", false},
		"":                    {"", false},
	}
	for in, want := range cases {
		got, ok := cronExpr(in)
		if got != want.want || ok != want.ok {
			t.Errorf("cronExpr(%q) = (%q, %v), want (%q, %v)", in, got, ok, want.want, want.ok)
		}
	}
}
