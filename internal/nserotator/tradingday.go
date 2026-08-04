package nserotator

import (
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
)

// IsLastTradingDayOfMonth delegates to the shared calendar implementation,
// which the ASX lane uses too. Kept as a wrapper so this package's call sites
// and tradingday_test.go stay unchanged.
func IsLastTradingDayOfMonth(now time.Time, hol *calendar.Holidays, market string, loc *time.Location) bool {
	return hol.IsLastTradingDayOfMonth(now, market, loc)
}
