package nserotator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
)

func testHolidays(t *testing.T, yaml string) *calendar.Holidays {
	t.Helper()
	p := filepath.Join(t.TempDir(), "holidays.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := calendar.LoadHolidays(p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

const nseFixture = `
year: 2026
markets:
  XNSE:
    tz: "Asia/Kolkata"
    holidays:
      - "2026-07-31"
    half_days: []
`

func TestIsLastTradingDayOfMonth(t *testing.T) {
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	hol := testHolidays(t, nseFixture)

	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 18, 0, 0, 0, ist)
	}

	// July 2026: 31st is a Friday but a holiday in the fixture → the 30th
	// (Thursday) becomes the last trading day.
	if IsLastTradingDayOfMonth(at(2026, 7, 31), hol, "XNSE", ist) {
		t.Error("holiday must not be a trading day")
	}
	if !IsLastTradingDayOfMonth(at(2026, 7, 30), hol, "XNSE", ist) {
		t.Error("Jul 30 should be last trading day when Jul 31 is a holiday")
	}
	if IsLastTradingDayOfMonth(at(2026, 7, 29), hol, "XNSE", ist) {
		t.Error("Jul 29 is not last — Jul 30 still trades")
	}

	// August 2026: 31st is a Monday, no holiday → it is the last trading day.
	if !IsLastTradingDayOfMonth(at(2026, 8, 31), hol, "XNSE", ist) {
		t.Error("Mon Aug 31 should be the last trading day")
	}
	// Aug 28 is the last Friday; 29/30 are weekend, but 31 trades → not last.
	if IsLastTradingDayOfMonth(at(2026, 8, 28), hol, "XNSE", ist) {
		t.Error("Fri Aug 28 is not last — Mon Aug 31 trades")
	}
	// Weekend days are never trading days.
	if IsLastTradingDayOfMonth(at(2026, 8, 30), hol, "XNSE", ist) {
		t.Error("Sunday must not be a trading day")
	}
}
