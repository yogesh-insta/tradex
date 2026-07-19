package calendar

import (
	"testing"
	"time"
)

func TestFilterNormalize(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	raw := []RawEvent{
		{Country: "US", Title: "Non-Farm Payrolls", Impact: "high", Time: now},
		{Country: "DE", Title: "German CPI Flash", Impact: "high", Time: now.Add(time.Hour)},
		{Country: "JP", Title: "BOJ Rate Decision", Impact: "high", Time: now.Add(2 * time.Hour)},
		{Country: "US", Title: "Fed Chair Speaks", Impact: "high", Time: now.Add(3 * time.Hour)}, // speech → drop
		{Country: "US", Title: "Retail Sales", Impact: "high", Time: now.Add(4 * time.Hour)},     // not allowlisted
		{Country: "GB", Title: "BoE Rate Decision", Impact: "high", Time: now.Add(5 * time.Hour)}, // wrong country
		{Country: "US", Title: "CPI", Impact: "medium", Time: now.Add(6 * time.Hour)},            // not high
		{Country: "FR", Title: "ECB Press Conference", Impact: "3", Time: now.Add(7 * time.Hour)},
		{Country: "JP", Title: "Tankan Large Manufacturers", Impact: "high", Time: now.Add(8 * time.Hour)},
	}
	got := FilterNormalize(raw)
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5: %+v", len(got), got)
	}
	wantRegions := map[string]int{"US": 1, "EU": 2, "JP": 2}
	for _, ev := range got {
		if ev.Impact != "high" {
			t.Fatalf("impact %q", ev.Impact)
		}
		wantRegions[ev.Region]--
	}
	for r, n := range wantRegions {
		if n != 0 {
			t.Fatalf("region %s count leftover %d", r, n)
		}
	}
}

func TestCountryToRegion(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"US", RegionUS, true},
		{"jp", RegionJP, true},
		{"DE", RegionEU, true},
		{"IT", RegionEU, true},
		{"GB", "", false},
	}
	for _, tt := range tests {
		got, ok := countryToRegion(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("%q => (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestMinTimeToHighImpact(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	c := NewCache(nil, 90*time.Minute)
	c.SetState(State{
		AsOf: now,
		Events: []Event{
			{Region: "US", Title: "NFP", Impact: "high", Time: now.Add(40 * time.Minute)},
			{Region: "JP", Title: "BOJ", Impact: "high", Time: now.Add(15 * time.Minute)},
			{Region: "EU", Title: "ECB", Impact: "high", Time: now.Add(5 * time.Minute)},
		},
	})
	got := MinTimeToHighImpact(c, []string{"US", "JP"}, now)
	if got != 15*time.Minute {
		t.Fatalf("got %v, want 15m", got)
	}
}
