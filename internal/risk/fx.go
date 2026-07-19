package risk

import (
	"fmt"
	"time"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// FX-specific rejection reasons (docs/specs/18-fx-risk-profile.md).
const (
	ReasonAlreadyTradedToday = "already_traded_today"
	ReasonSpreadTooWide      = "spread_too_wide"
	ReasonSpreadUnknown      = "spread_unknown"
	ReasonFXSessionClosed    = "fx_session_closed"
	ReasonReopenQuiet        = "reopen_quiet"
	ReasonFridayEntryCut     = "friday_entry_cut"
)

// FXProfile carries FX-only gates. When nil on Engine, Evaluate uses EU path only.
type FXProfile struct {
	MaxSpreadPips       float64
	PipSize             float64 // USD_JPY = 0.01
	RequireSpread       bool
	OneTradePerDay      bool
	ReopenQuiet         time.Duration
	FridayNoEntry       string
	FridayNoEntryTZ     *time.Location
	FridayHardFlatten   string
	FridayHardFlattenTZ *time.Location
	CalendarRegions     []string // news regions (US, JP)
	SessionTZ           *time.Location // Asia/Tokyo for session-day key
}

// FXDeps are optional FX market inputs.
type FXDeps struct {
	// SpreadPips returns the latest spread in pips for the instrument (ok=false if unknown).
	SpreadPips func(instrument string) (pips float64, ok bool)
	// TradedToday reports whether instrument already had an accepted entry this Tokyo session day.
	TradedToday func(instrument string, sessionDay string) bool
	// MarkTraded records an accepted entry for the Tokyo session day (called by trader after fill).
	MarkTraded func(instrument string, sessionDay string)
}

// WithFX attaches an FX profile and deps to the engine (call after NewEngine).
func (e *Engine) WithFX(p FXProfile, d FXDeps) *Engine {
	e.fx = &p
	e.fxDeps = d
	return e
}

// FXEnabled reports whether FX gates are active.
func (e *Engine) FXEnabled() bool { return e.fx != nil }

// SessionDay returns the Tokyo (or configured) session date key for now.
func (e *Engine) SessionDay(now time.Time) string {
	loc := time.UTC
	if e.fx != nil && e.fx.SessionTZ != nil {
		loc = e.fx.SessionTZ
	}
	return now.In(loc).Format("2006-01-02")
}

// EvaluateFXGates runs FX-only entry gates (spread, one-trade/day, weekend, Friday).
// Called from Evaluate when fx profile is set. News multi-region is handled via
// TimeToNews wrapper in the trader (region "FX").
func (e *Engine) evaluateFXGates(sig types.Signal, now time.Time) error {
	if e.fx == nil {
		return nil
	}
	p := e.fx

	if err := e.checkFXSessionHours(now); err != nil {
		return err
	}
	if err := e.checkFridayNoEntry(now); err != nil {
		return err
	}

	if p.OneTradePerDay && e.fxDeps.TradedToday != nil {
		day := e.SessionDay(now)
		if e.fxDeps.TradedToday(sig.Instrument, day) {
			return &Rejection{Reason: ReasonAlreadyTradedToday, Detail: day}
		}
	}

	if p.MaxSpreadPips > 0 {
		if e.fxDeps.SpreadPips == nil {
			if p.RequireSpread {
				return &Rejection{Reason: ReasonSpreadUnknown}
			}
		} else {
			pips, ok := e.fxDeps.SpreadPips(sig.Instrument)
			if !ok {
				if p.RequireSpread {
					return &Rejection{Reason: ReasonSpreadUnknown}
				}
			} else if pips > p.MaxSpreadPips {
				return &Rejection{Reason: ReasonSpreadTooWide,
					Detail: fmt.Sprintf("%.2f > %.2f", pips, p.MaxSpreadPips)}
			}
		}
	}
	return nil
}

func (e *Engine) checkFridayNoEntry(now time.Time) error {
	p := e.fx
	if p.FridayNoEntry == "" || p.FridayNoEntryTZ == nil {
		return nil
	}
	local := now.In(p.FridayNoEntryTZ)
	if local.Weekday() != time.Friday {
		return nil
	}
	cut, err := utils.AtClock(now, p.FridayNoEntry, p.FridayNoEntryTZ)
	if err != nil {
		return nil
	}
	if !now.Before(cut) {
		return &Rejection{Reason: ReasonFridayEntryCut, Detail: p.FridayNoEntry}
	}
	return nil
}

// checkFXSessionHours blocks Sat/Sun (NY), Friday after hard flatten, and
// Sunday reopen quiet window.
func (e *Engine) checkFXSessionHours(now time.Time) error {
	p := e.fx
	loc := p.FridayHardFlattenTZ
	if loc == nil {
		loc, _ = time.LoadLocation("America/New_York")
	}
	ny := now.In(loc)

	switch ny.Weekday() {
	case time.Saturday:
		return &Rejection{Reason: ReasonFXSessionClosed, Detail: "saturday"}
	case time.Sunday:
		// Typical FX reopen ~17:00 America/New_York Sunday.
		reopen, err := utils.AtClock(now, "17:00:00", loc)
		if err != nil {
			return &Rejection{Reason: ReasonFXSessionClosed, Detail: "sunday"}
		}
		if now.Before(reopen) {
			return &Rejection{Reason: ReasonFXSessionClosed, Detail: "sunday_before_reopen"}
		}
		if p.ReopenQuiet > 0 && now.Before(reopen.Add(p.ReopenQuiet)) {
			return &Rejection{Reason: ReasonReopenQuiet,
				Detail: fmt.Sprintf("%v after reopen", p.ReopenQuiet)}
		}
	case time.Friday:
		if p.FridayHardFlatten != "" {
			flat, err := utils.AtClock(now, p.FridayHardFlatten, loc)
			if err == nil && !now.Before(flat) {
				return &Rejection{Reason: ReasonFXSessionClosed, Detail: "friday_hard_flatten"}
			}
		}
	}
	return nil
}

// FXProfileFromConfig builds an FXProfile from config (nil if FX risk disabled).
func FXProfileFromConfig(fx config.FXRiskConfig, sessionTZ *time.Location) (*FXProfile, error) {
	if !fx.Enabled() {
		return nil, nil
	}
	nyName := fx.FridayNoEntryTZ
	if nyName == "" {
		nyName = "America/New_York"
	}
	ny, err := time.LoadLocation(nyName)
	if err != nil {
		return nil, err
	}
	flatTZName := fx.FridayHardFlattenTZ
	if flatTZName == "" {
		flatTZName = nyName
	}
	flatTZ, err := time.LoadLocation(flatTZName)
	if err != nil {
		return nil, err
	}
	one := true
	if fx.OneTradePerDay != nil {
		one = *fx.OneTradePerDay
	}
	reqSpread := true
	if fx.RequireSpread != nil {
		reqSpread = *fx.RequireSpread
	}
	pip := fx.PipSize
	if pip <= 0 {
		pip = 0.01
	}
	regions := fx.CalendarRegions
	if len(regions) == 0 {
		regions = []string{"US", "JP"}
	}
	quiet := time.Duration(fx.ReopenQuietMinutes) * time.Minute
	if fx.ReopenQuietMinutes == 0 {
		quiet = 30 * time.Minute
	}
	return &FXProfile{
		MaxSpreadPips:       fx.MaxSpreadPips,
		PipSize:             pip,
		RequireSpread:       reqSpread,
		OneTradePerDay:      one,
		ReopenQuiet:         quiet,
		FridayNoEntry:       fx.FridayNoEntry,
		FridayNoEntryTZ:     ny,
		FridayHardFlatten:   fx.FridayHardFlatten,
		FridayHardFlattenTZ: flatTZ,
		CalendarRegions:     regions,
		SessionTZ:           sessionTZ,
	}, nil
}

// MarkAccepted records a filled/accepted FX entry for one-trade/day tracking.
func (e *Engine) MarkAccepted(sig types.Signal, now time.Time) {
	if e.fx == nil || !e.fx.OneTradePerDay || e.fxDeps.MarkTraded == nil {
		return
	}
	e.fxDeps.MarkTraded(sig.Instrument, e.SessionDay(now))
}
