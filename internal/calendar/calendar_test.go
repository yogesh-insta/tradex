package calendar

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEconomicCacheFailSafe(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)

	t.Run("no state ever loaded => imminent", func(t *testing.T) {
		c := NewCache(FileProvider{Path: "/nonexistent"}, 90*time.Minute)
		if got := c.TimeToHighImpact("EU", now); got != 0 {
			t.Fatalf("got %v, want 0 (fail-safe)", got)
		}
		if c.Fresh(now) {
			t.Fatal("must not be fresh")
		}
	})

	t.Run("stale as_of => imminent", func(t *testing.T) {
		c := NewCache(nil, 90*time.Minute)
		c.SetState(State{AsOf: now.Add(-2 * time.Hour)})
		if got := c.TimeToHighImpact("EU", now); got != 0 {
			t.Fatalf("got %v, want 0 (stale)", got)
		}
	})

	t.Run("fresh with upcoming high-impact event", func(t *testing.T) {
		c := NewCache(nil, 90*time.Minute)
		c.SetState(State{
			AsOf: now.Add(-10 * time.Minute),
			Events: []Event{
				{Region: "EU", Title: "ECB Rate Decision", Impact: "high", Time: now.Add(20 * time.Minute)},
				{Region: "EU", Title: "Old CPI", Impact: "high", Time: now.Add(-time.Hour)},  // past: ignored
				{Region: "US", Title: "NFP", Impact: "high", Time: now.Add(5 * time.Minute)}, // other region
				{Region: "EU", Title: "PMI", Impact: "low", Time: now.Add(2 * time.Minute)},  // low impact
			},
		})
		if got := c.TimeToHighImpact("EU", now); got != 20*time.Minute {
			t.Fatalf("got %v, want 20m", got)
		}
	})

	t.Run("fresh with no events => no blackout", func(t *testing.T) {
		c := NewCache(nil, 90*time.Minute)
		c.SetState(State{AsOf: now})
		if got := c.TimeToHighImpact("EU", now); got != NoImminent {
			t.Fatalf("got %v, want NoImminent", got)
		}
	})

	t.Run("file provider round trip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "calendar-state.json")
		body := `{"as_of":"2026-07-17T07:50:00Z","events":[
			{"region":"EU","title":"Eurozone CPI","impact":"high","time":"2026-07-17T09:00:00Z"}]}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		c := NewCache(FileProvider{Path: path}, 90*time.Minute)
		if err := c.Refresh(); err != nil {
			t.Fatal(err)
		}
		if got := c.TimeToHighImpact("EU", now); got != time.Hour {
			t.Fatalf("got %v, want 1h", got)
		}
	})
}

const holidayYAML = `
year: 2026
markets:
  XETR:
    tz: "Europe/Berlin"
    holidays: ["2026-01-01", "2026-04-03"]
    half_days: []
  XPAR:
    tz: "Europe/Paris"
    holidays: ["2026-01-01"]
    half_days:
      - date: "2026-12-24"
        close: "14:05:00"
`

func TestHolidays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holidays.yaml")
	if err := os.WriteFile(path, []byte(holidayYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHolidays(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.Year() != 2026 {
		t.Fatalf("year = %d", h.Year())
	}

	berlin, _ := time.LoadLocation("Europe/Berlin")
	tests := []struct {
		name   string
		when   time.Time
		market string
		want   bool
	}{
		{"New Year holiday", time.Date(2026, 1, 1, 10, 0, 0, 0, berlin), "XETR", false},
		{"Good Friday", time.Date(2026, 4, 3, 10, 0, 0, 0, berlin), "XETR", false},
		{"Saturday", time.Date(2026, 7, 18, 10, 0, 0, 0, berlin), "XETR", false},
		{"Sunday", time.Date(2026, 7, 19, 10, 0, 0, 0, berlin), "XETR", false},
		{"normal Wednesday", time.Date(2026, 7, 15, 10, 0, 0, 0, berlin), "XETR", true},
		{"XPAR trades on XETR-only holiday", time.Date(2026, 4, 3, 10, 0, 0, 0, berlin), "XPAR", true},
		{"unknown market weekday defaults open", time.Date(2026, 7, 15, 10, 0, 0, 0, berlin), "XNYS", true},
		{"unknown market weekend closed", time.Date(2026, 7, 18, 10, 0, 0, 0, berlin), "XNYS", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.IsTradingDay(tt.when, tt.market); got != tt.want {
				t.Fatalf("IsTradingDay = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("half day early close", func(t *testing.T) {
		paris, _ := time.LoadLocation("Europe/Paris")
		when := time.Date(2026, 12, 24, 9, 0, 0, 0, paris)
		close, ok := h.EarlyClose(when, "XPAR")
		if !ok {
			t.Fatal("expected early close on Dec 24")
		}
		want := time.Date(2026, 12, 24, 14, 5, 0, 0, paris)
		if !close.Equal(want) {
			t.Fatalf("close = %v, want %v", close, want)
		}
		if _, ok := h.EarlyClose(time.Date(2026, 12, 23, 9, 0, 0, 0, paris), "XPAR"); ok {
			t.Fatal("Dec 23 is not a half day")
		}
	})
}
