package calendar

import (
	"strings"
	"time"
)

// Allowed regions in durable calendar-state.json.
const (
	RegionEU = "EU"
	RegionUS = "US"
	RegionJP = "JP"
)

// countryToRegion maps Finnhub (and Gemini) country codes onto durable regions.
// DE/FR/IT are EU proxies per docs/specs/10-economic-calendar.md.
func countryToRegion(country string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(country)) {
	case "US", "USA", "UNITED STATES":
		return RegionUS, true
	case "JP", "JPN", "JAPAN":
		return RegionJP, true
	case "EU", "EMU", "EZ", "EUROZONE", "EURO AREA", "EA":
		return RegionEU, true
	case "DE", "DEU", "GERMANY", "FR", "FRA", "FRANCE", "IT", "ITA", "ITALY":
		return RegionEU, true
	default:
		return "", false
	}
}

// isHighImpact reports whether the provider impact label is high.
func isHighImpact(impact string) bool {
	switch strings.ToLower(strings.TrimSpace(impact)) {
	case "high", "3", "red":
		return true
	default:
		return false
	}
}

// eventTypeAllowed keeps rate decisions / press conferences, CPI/PPI/PCE, NFP,
// GDP (Advance), and Tankan — and drops speeches / other noise.
func eventTypeAllowed(title string) bool {
	t := strings.ToLower(title)
	if t == "" {
		return false
	}
	// Speeches unless already a rate/press event (checked via keywords below).
	if strings.Contains(t, "speech") || strings.Contains(t, "speaks") {
		if !strings.Contains(t, "press conference") &&
			!strings.Contains(t, "rate decision") &&
			!strings.Contains(t, "fomc") &&
			!strings.Contains(t, "ecb") &&
			!strings.Contains(t, "boj") {
			return false
		}
	}
	allow := []string{
		"rate decision", "interest rate", "monetary policy", "policy rate",
		"press conference", "fomc", "federal funds",
		"ecb", "boj", "bank of japan", "mpm",
		"cpi", "hicp", "ppi", "pce",
		"nonfarm", "non-farm", "non farm", "nfp", "payroll",
		"gdp", "gross domestic",
		"tankan",
	}
	for _, k := range allow {
		if strings.Contains(t, k) {
			// GDP: prefer Advance / preliminary / flash; still allow plain "GDP" when high-impact.
			return true
		}
	}
	return false
}

// RawEvent is a provider-agnostic calendar row before region/impact filtering.
type RawEvent struct {
	Country string
	Title   string
	Impact  string
	Time    time.Time // must be UTC
}

// FilterNormalize keeps high-impact US/JP/EU events with allowed types and
// normalizes them into durable Event values (impact always "high", UTC times).
func FilterNormalize(raw []RawEvent) []Event {
	out := make([]Event, 0, len(raw))
	seen := map[string]bool{}
	for _, r := range raw {
		if r.Time.IsZero() || !isHighImpact(r.Impact) || !eventTypeAllowed(r.Title) {
			continue
		}
		region, ok := countryToRegion(r.Country)
		if !ok {
			continue
		}
		title := strings.TrimSpace(r.Title)
		if title == "" {
			continue
		}
		ev := Event{
			Region: region,
			Title:  title,
			Impact: "high",
			Time:   r.Time.UTC(),
		}
		key := ev.Region + "|" + ev.Title + "|" + ev.Time.Format(time.RFC3339)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ev)
	}
	return out
}

// MinTimeToHighImpact returns the soonest high-impact event across regions
// (fail-safe semantics are left to Cache.TimeToHighImpact per region).
func MinTimeToHighImpact(c *Cache, regions []string, now time.Time) time.Duration {
	if len(regions) == 0 {
		return c.TimeToHighImpact("", now)
	}
	min := NoImminent
	for _, r := range regions {
		d := c.TimeToHighImpact(r, now)
		if d < min {
			min = d
		}
	}
	return min
}
