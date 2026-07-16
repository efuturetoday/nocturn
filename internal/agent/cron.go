package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed 5-field cron expression (minute hour day-of-month month
// day-of-week), each field a bitset of the values it matches. It decides only WHEN
// an agent fires — never WHAT it may do; authority always comes from the agent's
// envelope (tools + policy + cage), never from the trigger.
type Schedule struct {
	min, hour, dom, month, dow uint64 // bitsets; bit i set = value i matches
	domStar, dowStar           bool   // field was literally "*" (Vixie dom/dow OR rule)
}

// field bounds: [min,max] inclusive.
type fieldSpec struct {
	name     string
	min, max int
}

var cronFields = []fieldSpec{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day-of-month", 1, 31},
	{"month", 1, 12},
	{"day-of-week", 0, 6}, // 0 = Sunday; 7 is accepted as an alias for 0
}

// ParseCron parses a standard 5-field cron expression. Each field supports "*",
// a single value, "a-b" ranges, "a-b/s" or "*/s" steps, and "a,b,..." lists of
// those. Fail-closed: a malformed expression is a hard error (never a schedule
// that fires at the wrong time or always).
func ParseCron(expr string) (Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("cron: want 5 fields (min hour dom month dow), got %d in %q", len(parts), expr)
	}
	var bits [5]uint64
	for i, spec := range cronFields {
		b, err := parseCronField(parts[i], spec)
		if err != nil {
			return Schedule{}, err
		}
		bits[i] = b
	}
	return Schedule{
		min: bits[0], hour: bits[1], dom: bits[2], month: bits[3], dow: bits[4],
		domStar: parts[2] == "*", dowStar: parts[4] == "*",
	}, nil
}

// parseCronField turns one field into a bitset over [spec.min, spec.max].
func parseCronField(field string, spec fieldSpec) (uint64, error) {
	var bits uint64
	for _, term := range strings.Split(field, ",") {
		if term == "" {
			return 0, fmt.Errorf("cron: empty term in %s field %q", spec.name, field)
		}
		// Optional /step suffix.
		step := 1
		base := term
		if slash := strings.IndexByte(term, '/'); slash >= 0 {
			base = term[:slash]
			s, err := strconv.Atoi(term[slash+1:])
			if err != nil || s <= 0 {
				return 0, fmt.Errorf("cron: bad step in %s field %q", spec.name, term)
			}
			step = s
		}
		lo, hi := spec.min, spec.max
		if base != "*" {
			if dash := strings.IndexByte(base, '-'); dash >= 0 {
				var err1, err2 error
				lo, err1 = strconv.Atoi(base[:dash])
				hi, err2 = strconv.Atoi(base[dash+1:])
				if err1 != nil || err2 != nil {
					return 0, fmt.Errorf("cron: bad range in %s field %q", spec.name, term)
				}
			} else {
				v, err := strconv.Atoi(base)
				if err != nil {
					return 0, fmt.Errorf("cron: bad value in %s field %q", spec.name, term)
				}
				lo, hi = v, v
			}
		}
		// day-of-week: accept 7 as an alias for 0 (Sunday).
		if spec.name == "day-of-week" {
			if lo == 7 {
				lo = 0
			}
			if hi == 7 {
				hi = 0
			}
		}
		if lo > hi {
			return 0, fmt.Errorf("cron: range %d-%d out of order in %s field", lo, hi, spec.name)
		}
		if lo < spec.min || hi > spec.max {
			return 0, fmt.Errorf("cron: %s value out of range [%d,%d] in %q", spec.name, spec.min, spec.max, term)
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

// Matches reports whether t (in its own location) falls on this schedule, to the
// minute. Day matching follows Vixie cron: if BOTH day-of-month and day-of-week are
// restricted (neither is "*"), the day matches when EITHER does; otherwise both must.
func (s Schedule) Matches(t time.Time) bool {
	if s.min&(1<<uint(t.Minute())) == 0 ||
		s.hour&(1<<uint(t.Hour())) == 0 ||
		s.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	domHit := s.dom&(1<<uint(t.Day())) != 0
	dowHit := s.dow&(1<<uint(int(t.Weekday()))) != 0
	if !s.domStar && !s.dowStar {
		return domHit || dowHit
	}
	return domHit && dowHit
}

// cronExpr extracts the inner expression from a when field of the form
// cron("<expr>"). Returns ok=false for any other trigger (manual, webhook, …).
func cronExpr(when string) (string, bool) {
	w := strings.TrimSpace(when)
	if !strings.HasPrefix(w, "cron(") || !strings.HasSuffix(w, ")") {
		return "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(w, "cron("), ")")
	inner = strings.TrimSpace(inner)
	inner = strings.Trim(inner, `"'`) // allow cron("…") or cron('…') or cron(…)
	return inner, inner != ""
}
