// Package utils holds small shared helpers: IANA/DST-safe session-window math
// and price rounding to instrument precision.
package utils

import (
	"fmt"
	"time"
)

// ParseClock parses "HH:MM:SS" (or "HH:MM") into hour/minute/second components.
func ParseClock(s string) (h, m, sec int, err error) {
	if s == "" {
		return 0, 0, 0, fmt.Errorf("empty clock string")
	}
	if _, err := time.Parse("15:04:05", s); err == nil {
		fmt.Sscanf(s, "%d:%d:%d", &h, &m, &sec)
		return h, m, sec, nil
	}
	if _, err := time.Parse("15:04", s); err == nil {
		fmt.Sscanf(s, "%d:%d", &h, &m)
		return h, m, 0, nil
	}
	return 0, 0, 0, fmt.Errorf("invalid clock %q (want HH:MM:SS)", s)
}

// AtClock returns the instant on the same wall-clock day as ref (in loc) at the
// given "HH:MM:SS" local time. DST-safe: the wall clock is resolved through the
// IANA zone for that specific date.
func AtClock(ref time.Time, clock string, loc *time.Location) (time.Time, error) {
	h, m, s, err := ParseClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	local := ref.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), h, m, s, 0, loc), nil
}

// FloorTo aligns t down to the nearest boundary of d (UTC-based, which matches
// exchange M5/H1 boundaries: :00/:05 and top of hour).
func FloorTo(t time.Time, d time.Duration) time.Time {
	return t.UTC().Truncate(d)
}

// SameLocalDay reports whether a and b fall on the same calendar day in loc.
func SameLocalDay(a, b time.Time, loc *time.Location) bool {
	al, bl := a.In(loc), b.In(loc)
	return al.Year() == bl.Year() && al.YearDay() == bl.YearDay()
}
