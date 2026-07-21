package nserotator

import (
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
)

// IsLastTradingDayOfMonth reports whether now (interpreted in the market's
// exchange-local zone via the holidays file) is the final trading day of its
// calendar month: today is a trading day and no later day in the month is.
func IsLastTradingDayOfMonth(now time.Time, hol *calendar.Holidays, market string, loc *time.Location) bool {
	local := now.In(loc)
	if !hol.IsTradingDay(local, market) {
		return false
	}
	year, month, day := local.Date()
	daysInMonth := time.Date(year, month+1, 0, 12, 0, 0, 0, loc).Day()
	for d := day + 1; d <= daysInMonth; d++ {
		t := time.Date(year, month, d, 12, 0, 0, 0, loc)
		if hol.IsTradingDay(t, market) {
			return false
		}
	}
	return true
}
