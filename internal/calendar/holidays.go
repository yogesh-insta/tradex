package calendar

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yogesh-insta/tradex/pkg/utils"
)

// HalfDay marks an early close for one date.
type HalfDay struct {
	Date  string `yaml:"date"`  // YYYY-MM-DD, exchange-local date
	Close string `yaml:"close"` // HH:MM:SS exchange-local wall clock
}

// MarketCalendar lists full holidays and half-days for one exchange (MIC code).
type MarketCalendar struct {
	TZ       string    `yaml:"tz"` // IANA zone of the exchange
	Holidays []string  `yaml:"holidays"`
	HalfDays []HalfDay `yaml:"half_days"`
}

type holidayFile struct {
	Year    int                       `yaml:"year"`
	Markets map[string]MarketCalendar `yaml:"markets"`
}

// Holidays answers isTradingDay / earlyClose from the checked-in config file.
type Holidays struct {
	year    int
	markets map[string]marketDays
}

type marketDays struct {
	loc      *time.Location
	holidays map[string]bool
	half     map[string]string // date -> close clock
}

// LoadHolidays parses config/holidays.yaml.
func LoadHolidays(path string) (*Holidays, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("holidays: %w", err)
	}
	var f holidayFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("holidays parse: %w", err)
	}
	h := &Holidays{year: f.Year, markets: map[string]marketDays{}}
	for mic, cal := range f.Markets {
		loc, err := time.LoadLocation(cal.TZ)
		if err != nil {
			return nil, fmt.Errorf("holidays: market %s tz %q: %w", mic, cal.TZ, err)
		}
		md := marketDays{loc: loc, holidays: map[string]bool{}, half: map[string]string{}}
		for _, d := range cal.Holidays {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return nil, fmt.Errorf("holidays: market %s bad date %q", mic, d)
			}
			md.holidays[d] = true
		}
		for _, hd := range cal.HalfDays {
			if _, err := time.Parse("2006-01-02", hd.Date); err != nil {
				return nil, fmt.Errorf("holidays: market %s bad half-day date %q", mic, hd.Date)
			}
			if _, _, _, err := utils.ParseClock(hd.Close); err != nil {
				return nil, fmt.Errorf("holidays: market %s half-day %s: %w", mic, hd.Date, err)
			}
			md.half[hd.Date] = hd.Close
		}
		h.markets[mic] = md
	}
	return h, nil
}

// Year returns the calendar year the file covers (alert if out of date).
func (h *Holidays) Year() int { return h.year }

// IsTradingDay reports whether the market is open on the given instant's
// exchange-local date. Weekends are always closed; unknown markets are treated
// as open (no data = no restriction beyond weekends).
func (h *Holidays) IsTradingDay(t time.Time, market string) bool {
	md, ok := h.markets[market]
	loc := time.UTC
	if ok {
		loc = md.loc
	}
	local := t.In(loc)
	if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	if !ok {
		return true
	}
	return !md.holidays[local.Format("2006-01-02")]
}

// EarlyClose returns the early-close instant for the given date/market if it
// is a half-day.
func (h *Holidays) EarlyClose(t time.Time, market string) (time.Time, bool) {
	md, ok := h.markets[market]
	if !ok {
		return time.Time{}, false
	}
	local := t.In(md.loc)
	clock, ok := md.half[local.Format("2006-01-02")]
	if !ok {
		return time.Time{}, false
	}
	at, err := utils.AtClock(local, clock, md.loc)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}
