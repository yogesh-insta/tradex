package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yogesh-insta/tradex/pkg/utils"
)

// Validate fail-fast checks the config for the live trader, per
// docs/specs/13-configuration.md. Returns all problems at once.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Env == "" {
		add("env must be set")
	}
	if c.OANDA.Host == "" {
		add("oanda.host must be set (api-fxpractice.oanda.com or api-fxtrade.oanda.com)")
	}
	if c.OANDA.StreamHost == "" {
		add("oanda.stream_host must be set")
	}
	if c.OANDA.Token == "" {
		add("oanda.token unresolved (set the referenced env var, e.g. OANDA_API_TOKEN)")
	}

	active := 0
	for _, a := range c.Accounts {
		if !a.Active {
			continue
		}
		active++
		if a.Name == "" {
			add("account with empty name")
		}
		if a.OANDAID == "" {
			add("account %q: oanda_account_id unresolved (set the referenced env var)", a.Name)
		}
		if len(a.Instruments) == 0 {
			add("account %q: no instruments", a.Name)
		}
		if a.Strategy == "" {
			add("account %q: no strategy assigned", a.Name)
		}
	}
	if active == 0 {
		add("no active accounts configured")
	}

	errs = append(errs, c.validateShared()...)

	// Control plane (live only).
	if c.ControlPlane.Listen == "" {
		add("control_plane.listen must be set")
	}
	if c.ControlPlane.Auth.Secret == "" {
		add("control_plane.auth.secret unresolved (set CONTROL_HMAC_SECRET)")
	}
	if c.ControlPlane.Auth.MaxSkew <= 0 {
		add("control_plane.auth.max_skew must be > 0")
	}
	if c.ControlPlane.Auth.NonceTTL <= 0 {
		add("control_plane.auth.nonce_ttl must be > 0")
	}

	return errors.Join(errs...)
}

// ValidateBacktest checks only what the offline backtester needs.
func (c *Config) ValidateBacktest() error {
	var errs []error
	errs = append(errs, c.validateShared()...)
	if c.Backtest.InitialEquity <= 0 {
		errs = append(errs, fmt.Errorf("backtest.initial_equity must be > 0"))
	}
	if c.Backtest.SpreadPoints < 0 || c.Backtest.SlippagePoints < 0 {
		errs = append(errs, fmt.Errorf("backtest spread/slippage must be >= 0"))
	}
	return errors.Join(errs...)
}

// ValidateDashboard fail-fast checks the Cloud Run dashboard config
// (docs/specs/14-dashboard.md). Independent of the live trader Validate().
func (c *Config) ValidateDashboard() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	d := c.Dashboard

	if d.ListenAddr == "" {
		add("dashboard.listen_addr must be set")
	}
	mode := strings.ToLower(d.Auth.Mode)
	if mode != "bearer" && mode != "iap" {
		add("dashboard.auth.mode must be \"bearer\" or \"iap\"")
	}
	if mode == "bearer" && !d.Mock && d.Auth.Token == "" {
		add("dashboard.auth.token unresolved (set DASHBOARD_TOKEN) — fail closed")
	}
	if !d.Mock {
		if d.OANDA.Host == "" {
			add("dashboard.oanda.host (or top-level oanda.host) must be set")
		}
		if d.OANDA.Token == "" {
			add("dashboard.oanda.token unresolved — fail closed")
		}
		if len(d.Accounts) == 0 {
			add("dashboard.accounts empty (configure dashboard.accounts or active top-level accounts)")
		}
		for _, a := range d.Accounts {
			if a.Name == "" {
				add("dashboard account with empty name")
			}
			if a.OANDAID == "" {
				add("dashboard account %q: oanda_account_id unresolved", a.Name)
			}
		}
		hasCal := d.CalendarFile != "" || d.GCS.CalendarObject != ""
		if !hasCal {
			add("dashboard calendar source missing (gcs.calendar_object or calendar_file)")
		}
		// OANDA transaction history is the production fallback when the
		// asynchronous BigQuery trade ledger has not been enabled yet.
	}
	if _, err := time.LoadLocation(d.UI.ReportingTZ); err != nil || d.UI.ReportingTZ == "" {
		add("dashboard.ui.reporting_tz %q is not a valid IANA timezone", d.UI.ReportingTZ)
	}
	if d.UI.PLDailyLookbackDays <= 0 {
		add("dashboard.ui.pl_daily_lookback_days must be > 0")
	}
	if d.CacheTTL.Overview <= 0 || d.CacheTTL.Calendar <= 0 || d.CacheTTL.PL <= 0 {
		add("dashboard.cache_ttl overview/calendar/pl must be > 0")
	}
	if d.UI.RefreshInterval <= 0 {
		add("dashboard.ui.refresh_interval must be > 0")
	}
	if d.Health.HeartbeatStale <= 0 || d.Health.TickStale <= 0 {
		add("dashboard.health heartbeat_stale/tick_stale must be > 0")
	}
	return errors.Join(errs...)
}

func (c *Config) validateShared() []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	// EU session.
	loc, err := time.LoadLocation(c.EUSession.TZ)
	if err != nil || c.EUSession.TZ == "" {
		add("eu_session.tz %q is not a valid IANA timezone", c.EUSession.TZ)
		loc = time.UTC
	}
	_ = loc
	rs, errS := clockSeconds(c.EUSession.RangeStart)
	re, errE := clockSeconds(c.EUSession.RangeEnd)
	if errS != nil {
		add("eu_session.range_start: %v", errS)
	}
	if errE != nil {
		add("eu_session.range_end: %v", errE)
	}
	if errS == nil && errE == nil && re <= rs {
		add("eu_session.range_end (%s) must be after range_start (%s)", c.EUSession.RangeEnd, c.EUSession.RangeStart)
	}
	if _, err := clockSeconds(c.EUSession.TradeWindowStart); err != nil {
		add("eu_session.trade_window_start: %v", err)
	}
	if c.EUSession.ATRPeriodDays <= 0 {
		add("eu_session.atr_period_days must be > 0")
	}
	if c.EUSession.VolMACandles <= 0 {
		add("eu_session.vol_ma_candles must be > 0")
	}

	// Strategy.
	s := c.Strategies.EULove
	if s.VolumeSpikeMult <= 0 {
		add("strategies.eu_love.volume_spike_mult must be > 0")
	}
	if s.SLATRMult <= 0 {
		add("strategies.eu_love.sl_atr_mult must be > 0")
	}
	if s.TPATRMult <= 0 {
		add("strategies.eu_love.tp_atr_mult must be > 0")
	}
	if s.BreakevenAtR <= 0 {
		add("strategies.eu_love.breakeven_at_r must be > 0")
	}
	if _, err := clockSeconds(s.EntryWindowEnd); err != nil {
		add("strategies.eu_love.entry_window_end: %v", err)
	}

	// Risk (global + per-account merged views).
	check := func(scope string, r RiskConfig) {
		if r.RiskPerTrade <= 0 || r.RiskPerTrade >= 1 {
			add("%s: risk_per_trade must be in (0,1)", scope)
		}
		if r.DailyLossLimit <= 0 {
			add("%s: daily_loss_limit must be > 0", scope)
		}
		if r.ConsecutiveLossHalt <= 0 {
			add("%s: consecutive_loss_halt must be > 0", scope)
		}
		if r.MaxConcurrent <= 0 {
			add("%s: max_concurrent must be > 0", scope)
		}
		if r.MaxMarginFrac <= 0 || r.MaxMarginFrac > 1 {
			add("%s: max_margin_frac must be in (0,1]", scope)
		}
		if r.MaxLeverage <= 0 {
			add("%s: max_leverage must be > 0", scope)
		}
	}
	check("risk", c.Risk)
	for _, a := range c.Accounts {
		if a.Active && a.Risk != nil {
			check("accounts."+a.Name+".risk", c.Risk.Merged(a.Risk))
		}
	}

	// Candle timeframes.
	for inst, tfs := range c.Candles.Timeframes {
		for _, tf := range tfs {
			if tf != "M5" && tf != "H1" {
				add("candles.timeframes.%s: unsupported timeframe %q (M5/H1)", inst, tf)
			}
		}
	}
	if c.Candles.WindowMax <= 0 {
		add("candles.window_max must be > 0")
	}

	// Mgmt cutoffs.
	if c.Mgmt.EUFridayCutoff != "" {
		if _, err := clockSeconds(c.Mgmt.EUFridayCutoff); err != nil {
			add("mgmt.eu_friday_cutoff: %v", err)
		}
	}
	if c.Mgmt.EUDailyCutoff != "" {
		if _, err := clockSeconds(c.Mgmt.EUDailyCutoff); err != nil {
			add("mgmt.eu_daily_cutoff: %v", err)
		}
	}
	if c.Mgmt.FXFridayCutoff != "" {
		if _, err := clockSeconds(c.Mgmt.FXFridayCutoff); err != nil {
			add("mgmt.fx_friday_cutoff: %v", err)
		}
	}
	if c.Mgmt.FXSoftCutoff != "" {
		if _, err := clockSeconds(c.Mgmt.FXSoftCutoff); err != nil {
			add("mgmt.fx_soft_cutoff: %v", err)
		}
	}

	errs = append(errs, c.validateFX()...)
	return errs
}

func (c *Config) validateFX() []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	fxActive := false
	for _, a := range c.Accounts {
		if a.Active && a.Strategy == "fx_trld" {
			fxActive = true
			break
		}
	}
	if !fxActive && !c.FXSession.Enabled() {
		return nil
	}

	fx := c.FXSession
	if fx.TZ == "" {
		add("fx_session.tz must be set when FX lane is enabled")
	} else if _, err := time.LoadLocation(fx.TZ); err != nil {
		add("fx_session.tz %q is not a valid IANA timezone", fx.TZ)
	}
	rs, errS := clockSeconds(fx.RangeStart)
	re, errE := clockSeconds(fx.RangeEnd)
	if errS != nil {
		add("fx_session.range_start: %v", errS)
	}
	if errE != nil {
		add("fx_session.range_end: %v", errE)
	}
	if errS == nil && errE == nil && re <= rs {
		add("fx_session.range_end must be after range_start")
	}
	for _, clk := range []struct{ name, v string }{
		{"fx_session.trade_window_start", fx.TradeWindowStart},
		{"fx_session.soft_cutoff", fx.SoftCutoff},
		{"fx_session.prep_time", fx.PrepTime},
	} {
		if clk.v == "" {
			continue
		}
		if _, err := clockSeconds(clk.v); err != nil {
			add("%s: %v", clk.name, err)
		}
	}
	if fx.ATRPeriodDays <= 0 {
		add("fx_session.atr_period_days must be > 0")
	}
	if fx.VolMACandles <= 0 {
		add("fx_session.vol_ma_candles must be > 0")
	}

	s := c.Strategies.FXTRLD
	if s.VolumeSpikeMult <= 0 {
		add("strategies.fx_trld.volume_spike_mult must be > 0")
	}
	if s.SLATRMult <= 0 || s.TPATRMult <= 0 || s.BreakevenAtR <= 0 {
		add("strategies.fx_trld multipliers must be > 0")
	}
	if s.MinATRFrac <= 0 || s.MaxATRFrac <= 0 || s.MaxATRFrac < s.MinATRFrac {
		add("strategies.fx_trld min/max_atr_frac invalid")
	}
	if s.MaxSpreadPips <= 0 {
		add("strategies.fx_trld.max_spread_pips must be > 0")
	}
	for _, clk := range []struct{ name, v string }{
		{"strategies.fx_trld.trade_window_start", s.TradeWindowStart},
		{"strategies.fx_trld.entry_window_end", s.EntryWindowEnd},
	} {
		if _, err := clockSeconds(clk.v); err != nil {
			add("%s: %v", clk.name, err)
		}
	}

	fr := c.FXRisk
	if fr.NewsBlockBefore <= 0 {
		add("fx_risk.news_block_before must be > 0")
	}
	if fr.MaxSpreadPips <= 0 {
		add("fx_risk.max_spread_pips must be > 0")
	}
	if fr.SoftCutoffFlattenR <= 0 {
		add("fx_risk.soft_cutoff_flatten_r must be > 0 (else soft-cutoff flatten is silently off)")
	}
	if fr.FridayNoEntry != "" {
		if _, err := clockSeconds(fr.FridayNoEntry); err != nil {
			add("fx_risk.friday_no_entry: %v", err)
		}
	}
	if fr.FridayHardFlatten != "" {
		if _, err := clockSeconds(fr.FridayHardFlatten); err != nil {
			add("fx_risk.friday_hard_flatten: %v", err)
		}
	}
	return errs
}

func clockSeconds(s string) (int, error) {
	h, m, sec, err := utils.ParseClock(s)
	if err != nil {
		return 0, err
	}
	return h*3600 + m*60 + sec, nil
}

// Redacted returns a human-readable summary of the effective config with all
// secret material removed, suitable for startup audit logging.
func (c *Config) Redacted() map[string]any {
	accounts := make([]map[string]any, 0, len(c.Accounts))
	for _, a := range c.Accounts {
		accounts = append(accounts, map[string]any{
			"name":        a.Name,
			"account_id":  redactID(a.OANDAID),
			"instruments": a.Instruments,
			"strategy":    a.Strategy,
			"active":      a.Active,
		})
	}
	return map[string]any{
		"env":         c.Env,
		"project":     c.Project,
		"oanda_host":  c.OANDA.Host,
		"stream_host": c.OANDA.StreamHost,
		"accounts":    accounts,
		"instruments": c.ActiveInstruments(),
		"dashboard": map[string]any{
			"listen":       c.Dashboard.ListenAddr,
			"mock":         c.Dashboard.Mock,
			"auth_mode":    c.Dashboard.Auth.Mode,
			"reporting_tz": c.Dashboard.UI.ReportingTZ,
			"accounts":     len(c.Dashboard.Accounts),
		},
	}
}

func redactID(id string) string {
	if len(id) <= 4 {
		return strings.Repeat("*", len(id))
	}
	return strings.Repeat("*", len(id)-4) + id[len(id)-4:]
}
