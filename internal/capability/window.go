package capability

import "time"

// Window is an allowed daily time range in minutes-of-day [0, 1440). It supports
// wrap-around: StartMin > EndMin means an overnight range (e.g. 22:00–06:00). A
// Rule with no Window (nil pointer) is not time-constrained — "not set" is an
// explicit nil, never an overloaded zero value.
type Window struct {
	StartMin int
	EndMin   int
}

// Daily builds a Window from clock hours and minutes, e.g. Daily(8,0,22,0) for
// 08:00–22:00.
func Daily(startHour, startMin, endHour, endMin int) *Window {
	return &Window{StartMin: startHour*60 + startMin, EndMin: endHour*60 + endMin}
}

// contains reports whether t's time-of-day falls within the window.
func (w Window) contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if w.StartMin <= w.EndMin {
		return m >= w.StartMin && m < w.EndMin
	}
	// wraps midnight
	return m >= w.StartMin || m < w.EndMin
}
