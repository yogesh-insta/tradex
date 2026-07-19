package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/oanda"
)

// Deps wires external backends. All are required except StatusURI may be empty.
type Deps struct {
	Accounts AccountReader
	Objects  ObjectFetcher
	Ledger   LedgerQuerier
	Log      *slog.Logger

	CalendarURI string
	StatusURI   string // optional
}

// Service aggregates overview / calendar / PL with TTL caches.
type Service struct {
	cfg  config.DashboardConfig
	deps Deps
	log  *slog.Logger
	now  func() time.Time

	cache *ttlCache
	loc   *time.Location
	euLoc *time.Location
}

// NewService builds the aggregator.
func NewService(cfg config.DashboardConfig, deps Deps) (*Service, error) {
	loc, err := time.LoadLocation(cfg.UI.ReportingTZ)
	if err != nil {
		return nil, fmt.Errorf("reporting_tz: %w", err)
	}
	euLoc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		return nil, err
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		cfg: cfg, deps: deps, log: log, now: time.Now,
		cache: newTTLCache(), loc: loc, euLoc: euLoc,
	}, nil
}

// Overview returns accounts, open trades, and engine health.
func (s *Service) Overview(ctx context.Context) OverviewResponse {
	const key = "overview"
	if v, ok := s.cache.Get(key); ok {
		out := v.(OverviewResponse)
		out.Cached = true
		return out
	}

	now := s.now().UTC()
	out := OverviewResponse{AsOf: now}
	calFresh, calAgeMs, _ := s.calendarFreshness(ctx, now)

	status, statusErr := s.loadStatus(ctx)
	if statusErr != nil {
		out.Errors = append(out.Errors, "status: "+statusErr.Error())
	}

	for _, ac := range s.cfg.Accounts {
		ao := AccountOverview{Name: ac.Name}
		sum, err := s.deps.Accounts.AccountSummary(ctx, ac.OANDAID)
		if err != nil {
			ao.Error = err.Error()
			out.Errors = append(out.Errors, fmt.Sprintf("oanda summary %s: %v", ac.Name, err))
			out.Accounts = append(out.Accounts, ao)
			out.Health = append(out.Health, s.deriveHealth(ac.Name, nil, statusErr != nil && status == nil, calFresh, calAgeMs, now))
			continue
		}
		ao.Currency = sum.Currency
		ao.NAV = float64(sum.NAV)
		ao.Balance = float64(sum.Balance)
		ao.UnrealizedPL = float64(sum.UnrealizedPL)
		ao.RealizedPLToday = float64(sum.ResettablePL)
		ao.MarginUsed = float64(sum.MarginUsed)
		ao.MarginAvailable = float64(sum.MarginAvailable)

		var st *AccountStatus
		if status != nil {
			for i := range status.Accounts {
				if status.Accounts[i].Name == ac.Name {
					st = &status.Accounts[i]
					ao.SystemState = st.State
					break
				}
			}
		}
		out.Accounts = append(out.Accounts, ao)
		out.Health = append(out.Health, s.deriveHealth(ac.Name, st, status == nil, calFresh, calAgeMs, now))

		trades, err := s.deps.Accounts.OpenTrades(ctx, ac.OANDAID)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("oanda trades %s: %v", ac.Name, err))
			continue
		}
		for _, t := range trades.Trades {
			out.Trades = append(out.Trades, toTradeView(ac.Name, t))
		}
	}

	s.cache.Set(key, out, s.cfg.CacheTTL.Overview.D())
	return out
}

func toTradeView(account string, t oanda.RESTTrade) OpenTradeView {
	units := float64(t.CurrentUnits)
	dir := "LONG"
	if units < 0 {
		dir = "SHORT"
		units = -units
	}
	v := OpenTradeView{
		Account: account, TradeID: t.ID, Instrument: t.Instrument,
		Direction: dir, Units: units, Entry: float64(t.Price),
		UnrealizedPL: float64(t.UnrealizedPL), OpenTime: t.OpenTime,
	}
	if t.StopLossOrder != nil {
		v.StopLoss = float64(t.StopLossOrder.Price)
	}
	if t.TakeProfitOrder != nil {
		v.TakeProfit = float64(t.TakeProfitOrder.Price)
	}
	return v
}

func (s *Service) deriveHealth(account string, st *AccountStatus, statusMissing bool, calFresh bool, calAgeMs int64, now time.Time) EngineHealth {
	h := EngineHealth{
		Account: account, CalendarFresh: calFresh, CalendarAsOfAgeMs: calAgeMs,
		State: "UNKNOWN", Stream: "unknown", Level: HealthUnknown,
		StatusSource: "missing",
	}
	if statusMissing {
		h.Notes = append(h.Notes, "status.json missing — health partially derived")
		h.StatusSource = "derived"
		// Safe derivation: calendar freshness only; OANDA reachability implied by overview errors.
		if !calFresh {
			h.Level = HealthAmber
			h.Notes = append(h.Notes, "calendar state stale or missing")
		} else {
			h.Level = HealthAmber // no heartbeat ⇒ cannot claim green
			h.Notes = append(h.Notes, "no trader heartbeat; cannot confirm stream/liveness")
		}
		return h
	}
	if st == nil {
		h.Notes = append(h.Notes, "account absent from status.json")
		h.StatusSource = "status.json"
		h.Level = HealthAmber
		return h
	}
	h.StatusSource = "status.json"
	h.State = st.State
	h.LastTickAgeMs = st.LastTickAgeMs
	ok := st.LastReconcileOK
	h.LastReconcileOK = &ok
	if !st.LastHeartbeatAt.IsZero() {
		h.HeartbeatAgeMs = now.Sub(st.LastHeartbeatAt).Milliseconds()
	}
	if st.StreamUp && !st.MarketDataStale && st.LastTickAgeMs <= s.cfg.Health.TickStale.D().Milliseconds() {
		h.Stream = "up"
	} else {
		h.Stream = "stale"
	}

	hbStale := s.cfg.Health.HeartbeatStale.D()
	tickStale := s.cfg.Health.TickStale.D()
	switch {
	case h.HeartbeatAgeMs > hbStale.Milliseconds(), h.Stream == "stale", !calFresh, st.State == "SYSTEM_LOCKED", st.State == "DISABLED":
		h.Level = HealthRed
	case st.State == "PAUSED", !st.LastReconcileOK:
		h.Level = HealthAmber
	default:
		h.Level = HealthGreen
	}
	_ = tickStale
	return h
}

func (s *Service) loadStatus(ctx context.Context) (*StatusDoc, error) {
	uri := s.deps.StatusURI
	if uri == "" {
		return nil, fmt.Errorf("not configured")
	}
	raw, err := s.deps.Objects.Fetch(ctx, uri)
	if err != nil {
		return nil, err
	}
	var doc StatusDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func (s *Service) calendarFreshness(ctx context.Context, now time.Time) (fresh bool, ageMs int64, state calendar.State) {
	raw, err := s.deps.Objects.Fetch(ctx, s.deps.CalendarURI)
	if err != nil {
		return false, 0, state
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return false, 0, state
	}
	if !state.AsOf.IsZero() {
		ageMs = now.Sub(state.AsOf).Milliseconds()
	}
	fresh = !state.AsOf.IsZero() && now.Sub(state.AsOf) <= s.cfg.Health.CalendarStale.D()
	return fresh, ageMs, state
}

// Calendar returns upcoming high-impact events from calendar-state.json.
func (s *Service) Calendar(ctx context.Context) CalendarResponse {
	const key = "calendar"
	if v, ok := s.cache.Get(key); ok {
		out := v.(CalendarResponse)
		out.Cached = true
		return out
	}

	now := s.now().UTC()
	out := CalendarResponse{}
	raw, err := s.deps.Objects.Fetch(ctx, s.deps.CalendarURI)
	if err != nil {
		out.Missing = true
		out.Stale = true
		out.Warning = "calendar-state missing or unreadable — treat as unknown / imminent (fail-safe)"
		out.Errors = append(out.Errors, err.Error())
		s.cache.Set(key, out, s.cfg.CacheTTL.Calendar.D())
		return out
	}
	var state calendar.State
	if err := json.Unmarshal(raw, &state); err != nil {
		out.Missing = true
		out.Stale = true
		out.Warning = "calendar-state unparseable — fail-safe"
		out.Errors = append(out.Errors, err.Error())
		s.cache.Set(key, out, s.cfg.CacheTTL.Calendar.D())
		return out
	}
	out.AsOf = state.AsOf
	out.Fresh = !state.AsOf.IsZero() && now.Sub(state.AsOf) <= s.cfg.Health.CalendarStale.D()
	out.Stale = !out.Fresh
	if out.Stale {
		out.Warning = "calendar-state stale — treat as unknown / imminent (fail-safe); events shown may be outdated"
	}
	for _, ev := range state.Events {
		if ev.Impact != "high" {
			continue
		}
		if ev.Time.Before(now) {
			continue
		}
		out.Events = append(out.Events, CalendarEvent{
			Region: ev.Region, Title: ev.Title, Impact: ev.Impact,
			TimeUTC: ev.Time, TimeBerlin: ev.Time.In(s.euLoc).Format("2006-01-02 15:04 MST"),
		})
	}
	s.cache.Set(key, out, s.cfg.CacheTTL.Calendar.D())
	return out
}

// PL returns a window rollup for account.
func (s *Service) PL(ctx context.Context, account, window string) PLResponse {
	key := "pl:" + account + ":" + window
	if v, ok := s.cache.Get(key); ok {
		out := v.(PLResponse)
		out.Cached = true
		return out
	}
	out := PLResponse{
		Account: account, Window: window, ReportingTZ: s.cfg.UI.ReportingTZ,
	}
	if account == "" {
		out.Errors = append(out.Errors, "account required")
		return out
	}
	now := s.now()
	from, to, err := WindowBounds(window, now, s.loc, s.cfg.UI.PLDailyLookbackDays)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
		return out
	}
	if strings.EqualFold(window, "all") {
		total, count, err := s.deps.Ledger.AllTimePL(ctx, account)
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
			return out
		}
		out.TotalPL = total
		out.TradeCount = count
		s.cache.Set(key, out, s.cfg.CacheTTL.PL.D())
		return out
	}
	days, err := s.deps.Ledger.DailyPL(ctx, account, from, to, s.loc)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
		return out
	}
	out.Days = days
	for _, d := range days {
		out.TotalPL += d.RealizedPL
		out.TradeCount += d.TradeCount
	}
	s.cache.Set(key, out, s.cfg.CacheTTL.PL.D())
	return out
}

// DailyPLSeries returns per-day rows plus 7d and all-time totals.
func (s *Service) DailyPLSeries(ctx context.Context, account string, from, to time.Time) DailyPLResponse {
	key := fmt.Sprintf("pldaily:%s:%s:%s", account, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if v, ok := s.cache.Get(key); ok {
		out := v.(DailyPLResponse)
		out.Cached = true
		return out
	}
	out := DailyPLResponse{
		Account: account, ReportingTZ: s.cfg.UI.ReportingTZ,
		From: from.In(s.loc).Format("2006-01-02"),
		To:   to.In(s.loc).Format("2006-01-02"),
	}
	if account == "" {
		out.Errors = append(out.Errors, "account required")
		return out
	}
	days, err := s.deps.Ledger.DailyPL(ctx, account, from, to, s.loc)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
		return out
	}
	out.Days = days
	out.Total7d = SumLastNDays(days, 7)
	total, count, err := s.deps.Ledger.AllTimePL(ctx, account)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
	} else {
		out.TotalAll = total
		out.TradeCountAll = count
	}
	s.cache.Set(key, out, s.cfg.CacheTTL.PL.D())
	return out
}

// UIConfig exposes refresh interval etc. to the embedded page.
func (s *Service) UIConfig() map[string]any {
	accounts := make([]string, 0, len(s.cfg.Accounts))
	for _, a := range s.cfg.Accounts {
		accounts = append(accounts, a.Name)
	}
	return map[string]any{
		"refresh_interval_ms":    s.cfg.UI.RefreshInterval.D().Milliseconds(),
		"pl_daily_lookback_days": s.cfg.UI.PLDailyLookbackDays,
		"reporting_tz":           s.cfg.UI.ReportingTZ,
		"accounts":               accounts,
		"mock":                   s.cfg.Mock,
	}
}
