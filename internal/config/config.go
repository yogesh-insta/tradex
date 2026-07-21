// Package config loads the per-environment YAML config, applies env-var
// overrides/secret resolution, and fail-fast validates per docs/specs/13.
//
// Secrets are referenced as ${ENV_VAR} in the YAML and resolved at load time
// through a SecretResolver (env vars in v1; Secret Manager later behind the
// same interface). Secret values are never logged.
package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration for YAML strings like "15s", "30m".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(dur)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// SecretResolver resolves a secret reference name to its value. The v1
// implementation reads environment variables; a Secret Manager implementation
// can be swapped in later without touching consumers.
type SecretResolver interface {
	Resolve(ref string) (string, error)
}

// EnvResolver resolves ${NAME} references from process environment variables.
type EnvResolver struct{}

// Resolve returns the environment variable value, or an error if unset.
func (EnvResolver) Resolve(ref string) (string, error) {
	v, ok := os.LookupEnv(ref)
	if !ok {
		return "", fmt.Errorf("environment variable %s not set", ref)
	}
	return v, nil
}

// Config is the full per-environment configuration tree.
type Config struct {
	Env     string `yaml:"env"`
	Project string `yaml:"project"`

	OANDA         OANDAConfig         `yaml:"oanda"`
	Accounts      []AccountConfig     `yaml:"accounts"`
	Stream        StreamConfig        `yaml:"stream"`
	Candles       CandlesConfig       `yaml:"candles"`
	EUSession     EUSessionConfig     `yaml:"eu_session"`
	FXSession     FXSessionConfig     `yaml:"fx_session"`
	Strategies    StrategiesConfig    `yaml:"strategies"`
	Risk          RiskConfig          `yaml:"risk"`
	FXRisk        FXRiskConfig        `yaml:"fx_risk"`
	Executor      ExecutorConfig      `yaml:"executor"`
	Mgmt          MgmtConfig          `yaml:"mgmt"`
	ControlPlane  ControlPlaneConfig  `yaml:"control_plane"`
	Calendar      CalendarConfig      `yaml:"calendar"`
	Persistence   PersistenceConfig   `yaml:"persistence"`
	Observability ObservabilityConfig `yaml:"observability"`
	Backtest      BacktestConfig      `yaml:"backtest"`
	Dashboard     DashboardConfig     `yaml:"dashboard"`
}

// DashboardConfig — 14-dashboard.md (Cloud Run read-only ops UI; not used by trader).
type DashboardConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	// Mock enables explicit fixture mode for local demos (no OANDA/GCS/BQ).
	Mock bool `yaml:"mock"`

	Auth     DashboardAuthConfig      `yaml:"auth"`
	OANDA    DashboardOANDAConfig     `yaml:"oanda"`    // optional; falls back to top-level oanda
	Accounts []DashboardAccountConfig `yaml:"accounts"` // optional; falls back to top-level accounts

	GCS      DashboardGCSConfig      `yaml:"gcs"`
	BigQuery DashboardBigQueryConfig `yaml:"bigquery"`

	// Local file overrides (dev/mock). When set, used instead of GCS/BQ.
	CalendarFile string `yaml:"calendar_file"`
	StatusFile   string `yaml:"status_file"`
	LedgerFile   string `yaml:"ledger_file"` // JSON closed-trade rows for local PL

	CacheTTL DashboardCacheTTLConfig `yaml:"cache_ttl"`
	UI       DashboardUIConfig       `yaml:"ui"`
	Health   DashboardHealthConfig   `yaml:"health"`
}

// DashboardAuthConfig selects bearer token or IAP identity gate.
type DashboardAuthConfig struct {
	Mode  string `yaml:"mode"`  // "bearer" | "iap"
	Token string `yaml:"token"` // ${DASHBOARD_TOKEN}; required for bearer
	// IAPAllowEmails is an optional allowlist when mode=iap (empty = any
	// authenticated IAP identity is accepted).
	IAPAllowEmails []string `yaml:"iap_allow_emails"`
}

// DashboardOANDAConfig is the read-only OANDA REST client used by the dashboard.
type DashboardOANDAConfig struct {
	Host  string `yaml:"host"`
	Token string `yaml:"token"`
}

// DashboardAccountConfig is one account shown on the dashboard.
type DashboardAccountConfig struct {
	Name    string `yaml:"name"`
	OANDAID string `yaml:"oanda_account_id"`
}

// DashboardGCSConfig points at durable state objects.
type DashboardGCSConfig struct {
	CalendarObject string `yaml:"calendar_object"` // gs://… or local path
	StatusObject   string `yaml:"status_object"`   // optional
}

// DashboardBigQueryConfig selects the trade_ledger table for realized PL.
type DashboardBigQueryConfig struct {
	Project string `yaml:"project"`
	Dataset string `yaml:"dataset"`
	Table   string `yaml:"table"`
}

// DashboardCacheTTLConfig bounds in-process cache freshness.
type DashboardCacheTTLConfig struct {
	Overview Duration `yaml:"overview"`
	Calendar Duration `yaml:"calendar"`
	PL       Duration `yaml:"pl"`
}

// DashboardUIConfig drives the browser page.
type DashboardUIConfig struct {
	RefreshInterval     Duration `yaml:"refresh_interval"`
	PLDailyLookbackDays int      `yaml:"pl_daily_lookback_days"`
	ReportingTZ         string   `yaml:"reporting_tz"` // day bucket for per-day PL
}

// DashboardHealthConfig sets green/amber/red thresholds for the health panel.
type DashboardHealthConfig struct {
	HeartbeatStale Duration `yaml:"heartbeat_stale"`
	TickStale      Duration `yaml:"tick_stale"`
	CalendarStale  Duration `yaml:"calendar_stale"` // as_of age; default 90m
}

// OANDAConfig selects the broker host + credentials. Live vs paper is chosen
// by which host/credentials are loaded — never a boolean.
type OANDAConfig struct {
	Host       string `yaml:"host"`        // api-fxpractice.oanda.com | api-fxtrade.oanda.com
	StreamHost string `yaml:"stream_host"` // stream-fxpractice.oanda.com | stream-fxtrade.oanda.com
	Token      string `yaml:"token"`       // ${OANDA_API_TOKEN}
}

// AccountConfig maps one OANDA sub-account to an instrument group and a
// strategy, with optional per-account risk overrides.
type AccountConfig struct {
	Name        string      `yaml:"name"`
	OANDAID     string      `yaml:"oanda_account_id"` // ${OANDA_ACCOUNT_ID_EU}
	Instruments []string    `yaml:"instruments"`
	Strategy    string      `yaml:"strategy"` // registry key, e.g. "eu_love"
	Active      bool        `yaml:"active"`
	Risk        *RiskConfig `yaml:"risk"` // nil = use global risk config
}

// StreamConfig — 02-market-data-stream.md.
type StreamConfig struct {
	Instruments      []string `yaml:"instruments"` // optional; derived from accounts if empty
	HeartbeatTimeout Duration `yaml:"heartbeat_timeout"`
	StaleHalt        Duration `yaml:"stale_halt"`
	BackoffBase      Duration `yaml:"backoff_base"`
	BackoffMax       Duration `yaml:"backoff_max"`
	TickChannelBuf   int      `yaml:"tick_channel_buffer"`
}

// CandlesConfig — 03-candle-builder.md.
type CandlesConfig struct {
	Price              string              `yaml:"price"` // "M" = mid
	Timeframes         map[string][]string `yaml:"timeframes"`
	WindowMax          int                 `yaml:"window_max"`
	RESTConfirmDelay   Duration            `yaml:"rest_confirm_delay"`
	RESTConfirmTimeout Duration            `yaml:"rest_confirm_timeout"`
}

// EUSessionConfig — 04-eu-session-controller.md.
type EUSessionConfig struct {
	TZ               string   `yaml:"tz"`
	RangeStart       string   `yaml:"range_start"`
	RangeEnd         string   `yaml:"range_end"`
	TradeWindowStart string   `yaml:"trade_window_start"`
	ATRPeriodDays    int      `yaml:"atr_period_days"`
	VolMACandles     int      `yaml:"vol_ma_candles"`
	Instruments      []string `yaml:"instruments"`
}

// StrategiesConfig holds per-strategy tunables, keyed by registry name.
type StrategiesConfig struct {
	EULove EULoveConfig `yaml:"eu_love"`
	FXTRLD FXTRLDConfig `yaml:"fx_trld"`
}

// EULoveConfig — 05-strategy-eu-love.md.
type EULoveConfig struct {
	VolumeSpikeMult float64 `yaml:"volume_spike_mult"`
	SLATRMult       float64 `yaml:"sl_atr_mult"`
	TPATRMult       float64 `yaml:"tp_atr_mult"`
	BreakevenAtR    float64 `yaml:"breakeven_at_r"`
	EntryWindowEnd  string  `yaml:"entry_window_end"` // Europe/Berlin
}

// FXSessionConfig — 16-fx-session-controller.md (Asia/Tokyo clocks).
type FXSessionConfig struct {
	TZ               string   `yaml:"tz"`
	RangeStart       string   `yaml:"range_start"`
	RangeEnd         string   `yaml:"range_end"`
	TradeWindowStart string   `yaml:"trade_window_start"`
	SoftCutoff       string   `yaml:"soft_cutoff"`
	PrepTime         string   `yaml:"prep_time"`
	ATRPeriodDays    int      `yaml:"atr_period_days"`
	VolMACandles     int      `yaml:"vol_ma_candles"`
	Instruments      []string `yaml:"instruments"`
}

// Enabled reports whether an FX session lane is configured.
func (c FXSessionConfig) Enabled() bool { return len(c.Instruments) > 0 && c.TZ != "" }

// FXTRLDConfig — 17-strategy-fx-trld.md.
type FXTRLDConfig struct {
	VolumeSpikeMult  float64 `yaml:"volume_spike_mult"`
	SLATRMult        float64 `yaml:"sl_atr_mult"`
	TPATRMult        float64 `yaml:"tp_atr_mult"`
	BreakevenAtR     float64 `yaml:"breakeven_at_r"`
	MinATRFrac       float64 `yaml:"min_atr_frac"`
	MaxATRFrac       float64 `yaml:"max_atr_frac"`
	MaxSpreadPips    float64 `yaml:"max_spread_pips"`
	TradeWindowStart string  `yaml:"trade_window_start"`
	EntryWindowEnd   string  `yaml:"entry_window_end"`
	TrailAfterR      float64 `yaml:"trail_after_r"` // 0 = off
}

// FXRiskConfig — 18-fx-risk-profile.md (FX-only gates on top of RiskConfig).
type FXRiskConfig struct {
	AccountID           string   `yaml:"account_id"` // optional; accounts[].oanda_account_id is authoritative
	RiskPerTrade        float64  `yaml:"risk_per_trade"`
	DailyLossLimit      float64  `yaml:"daily_loss_limit"`
	ConsecutiveLossHalt int      `yaml:"consecutive_loss_halt"`
	MaxConcurrent       int      `yaml:"max_concurrent"`
	MaxMarginFrac       float64  `yaml:"max_margin_frac"`
	MaxLeverage         float64  `yaml:"max_leverage"`
	NewsBlockBefore     Duration `yaml:"news_block_before"`
	MaxSpreadPips       float64  `yaml:"max_spread_pips"`
	OneTradePerDay      *bool    `yaml:"one_trade_per_day"` // nil = true when FX enabled
	ReopenQuietMinutes  int      `yaml:"reopen_quiet_minutes"`
	FridayNoEntry       string   `yaml:"friday_no_entry"`     // America/New_York
	FridayHardFlatten   string   `yaml:"friday_hard_flatten"` // America/New_York
	SoftCutoffFlattenR  float64  `yaml:"soft_cutoff_flatten_r"`
	CalendarRegions     []string `yaml:"calendar_regions"`
	RequireSpread       *bool    `yaml:"require_spread"` // nil = true
	FridayNoEntryTZ     string   `yaml:"friday_no_entry_tz"`
	FridayHardFlattenTZ string   `yaml:"friday_hard_flatten_tz"`
	PipSize             float64  `yaml:"pip_size"` // USD_JPY default 0.01
}

// Enabled reports whether FX risk profile keys are present.
func (c FXRiskConfig) Enabled() bool {
	return c.MaxSpreadPips > 0 || c.NewsBlockBefore > 0 || len(c.CalendarRegions) > 0
}

// AsRiskConfig maps the shared numeric gates onto RiskConfig for portfolio merge.
func (c FXRiskConfig) AsRiskConfig() RiskConfig {
	return RiskConfig{
		RiskPerTrade:        c.RiskPerTrade,
		DailyLossLimit:      c.DailyLossLimit,
		ConsecutiveLossHalt: c.ConsecutiveLossHalt,
		MaxConcurrent:       c.MaxConcurrent,
		MaxMarginFrac:       c.MaxMarginFrac,
		MaxLeverage:         c.MaxLeverage,
		NewsBlockBefore:     c.NewsBlockBefore,
	}
}

// RiskConfig — 06-risk-management.md. Also used for per-account overrides.
type RiskConfig struct {
	RiskPerTrade        float64    `yaml:"risk_per_trade"`
	DailyLossLimit      float64    `yaml:"daily_loss_limit"`
	ConsecutiveLossHalt int        `yaml:"consecutive_loss_halt"`
	MaxConcurrent       int        `yaml:"max_concurrent"`
	MaxMarginFrac       float64    `yaml:"max_margin_frac"`
	MaxLeverage         float64    `yaml:"max_leverage"`
	NewsBlockBefore     Duration   `yaml:"news_block_before"`
	CorrelationGroups   [][]string `yaml:"correlation_groups"`
}

// Merged returns a copy of the global risk config with non-zero override
// fields from o applied.
func (r RiskConfig) Merged(o *RiskConfig) RiskConfig {
	if o == nil {
		return r
	}
	m := r
	if o.RiskPerTrade > 0 {
		m.RiskPerTrade = o.RiskPerTrade
	}
	if o.DailyLossLimit > 0 {
		m.DailyLossLimit = o.DailyLossLimit
	}
	if o.ConsecutiveLossHalt > 0 {
		m.ConsecutiveLossHalt = o.ConsecutiveLossHalt
	}
	if o.MaxConcurrent > 0 {
		m.MaxConcurrent = o.MaxConcurrent
	}
	if o.MaxMarginFrac > 0 {
		m.MaxMarginFrac = o.MaxMarginFrac
	}
	if o.MaxLeverage > 0 {
		m.MaxLeverage = o.MaxLeverage
	}
	if o.NewsBlockBefore > 0 {
		m.NewsBlockBefore = o.NewsBlockBefore
	}
	if len(o.CorrelationGroups) > 0 {
		m.CorrelationGroups = o.CorrelationGroups
	}
	return m
}

// ExecutorConfig — 07-order-executor.md (host/account moved to oanda/accounts).
type ExecutorConfig struct {
	TimeInForce      string   `yaml:"time_in_force"`
	RequestTimeout   Duration `yaml:"request_timeout"`
	MaxRetries       int      `yaml:"max_retries"`
	RetryBackoffBase Duration `yaml:"retry_backoff_base"`
}

// MgmtConfig — 08-trade-management-loop.md (+ FX amendments from 18).
type MgmtConfig struct {
	TickInterval      Duration `yaml:"tick_interval"`
	ReconcileInterval Duration `yaml:"reconcile_interval"`
	NewsBlockBefore   Duration `yaml:"news_block_before"`
	EUFridayCutoff    string   `yaml:"eu_friday_cutoff"` // Europe/Berlin
	EUDailyCutoff     string   `yaml:"eu_daily_cutoff"`  // empty = none (v1)
	FXFridayCutoffTZ  string   `yaml:"fx_friday_cutoff_tz"`
	FXFridayCutoff    string   `yaml:"fx_friday_cutoff"`
	FXSoftCutoffTZ    string   `yaml:"fx_soft_cutoff_tz"`
	FXSoftCutoff      string   `yaml:"fx_soft_cutoff"`
}

// ControlPlaneConfig — 09-control-plane.md.
type ControlPlaneConfig struct {
	Listen     string     `yaml:"listen"`
	Auth       AuthConfig `yaml:"auth"`
	AllowCIDRs []string   `yaml:"allow_cidrs"`
	TLSCert    string     `yaml:"tls_cert"` // path; empty = plain HTTP (front with TLS)
	TLSKey     string     `yaml:"tls_key"`
}

// AuthConfig holds HMAC webhook auth parameters.
type AuthConfig struct {
	Secret   string   `yaml:"secret"` // ${CONTROL_HMAC_SECRET}
	MaxSkew  Duration `yaml:"max_skew"`
	NonceTTL Duration `yaml:"nonce_ttl"`
}

// CalendarConfig — 10-economic-calendar.md.
type CalendarConfig struct {
	Economic EconomicCalendarConfig `yaml:"economic"`
	Holidays HolidaysConfig         `yaml:"holidays"`
}

// EconomicCalendarConfig configures the economic-events read path and poller.
type EconomicCalendarConfig struct {
	Provider              string   `yaml:"provider"`          // VM read: "file" | "gcs"; poller primary is always Finnhub
	FallbackProvider      string   `yaml:"fallback_provider"` // "gemini"
	GeminiModel           string   `yaml:"gemini_model"`
	PollInterval          Duration `yaml:"poll_interval"`
	LookaheadDays         int      `yaml:"lookahead_days"`
	StateFile             string   `yaml:"state_file"` // alias for local_file (VM read)
	LocalFile             string   `yaml:"local_file"` // durable write path for poller
	GCSObject             string   `yaml:"gcs_object"`
	VMRefresh             Duration `yaml:"vm_refresh"`
	StalenessMax          Duration `yaml:"staleness_max"`
	HighImpactOnly        bool     `yaml:"high_impact_only"`
	Regions               []string `yaml:"regions"`
	TelegramReview        *bool    `yaml:"telegram_review"`
	AutoWriteOnTelegramOK *bool    `yaml:"auto_write_on_telegram_ok"`
}

// HolidaysConfig points at the checked-in trading-holiday file.
type HolidaysConfig struct {
	File    string   `yaml:"file"`
	Markets []string `yaml:"markets"`
}

// PersistenceConfig — 11-trade-ledger-persistence.md (publisher is a stdout
// no-op in v1 behind an interface).
type PersistenceConfig struct {
	Topics       map[string]string `yaml:"topics"`
	BufferSize   int               `yaml:"buffer_size"`
	FlushTimeout Duration          `yaml:"flush_timeout"`
}

// ObservabilityConfig — 12-observability-and-alerts.md.
type ObservabilityConfig struct {
	HeartbeatInterval Duration `yaml:"heartbeat_interval"`
	LivenessTimeout   Duration `yaml:"liveness_timeout"`
	ReconcileInterval Duration `yaml:"reconcile_interval"`
	DrawdownWarn      float64  `yaml:"drawdown_warn"`
	AlertTopic        string   `yaml:"alert_topic"`
	// StatusFile and StatusObject are trader heartbeat targets. StatusObject is
	// a gs:// URI, while StatusFile is an optional local diagnostic copy.
	StatusFile   string         `yaml:"status_file"`
	StatusObject string         `yaml:"status_object"`
	Telegram     TelegramConfig `yaml:"telegram"`
}

// TelegramConfig holds outbound Telegram bot credentials (env-resolved).
type TelegramConfig struct {
	BotToken string `yaml:"bot_token"` // ${TELEGRAM_BOT_TOKEN}
	ChatID   string `yaml:"chat_id"`   // ${TELEGRAM_CHAT_ID}
}

// BacktestConfig configures the offline simulation engine.
type BacktestConfig struct {
	InitialEquity  float64 `yaml:"initial_equity"`
	SpreadPoints   float64 `yaml:"spread_points"`   // simulated half... full spread in price points
	SlippagePoints float64 `yaml:"slippage_points"` // adverse slippage per fill, price points
	DataDir        string  `yaml:"data_dir"`        // cache dir for downloaded candles
	OutputDir      string  `yaml:"output_dir"`      // equity-curve CSV output
}

var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads the YAML file, resolves ${ENV} secret references via resolver,
// applies defaults, and returns the parsed config. Validation is separate
// (Validate / ValidateBacktest) so the backtester can skip live-only checks.
func Load(path string, resolver SecretResolver) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var resolveErr error
	expanded := envRefRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		name := envRefRe.FindStringSubmatch(m)[1]
		v, err := resolver.Resolve(name)
		if err != nil {
			// Leave unresolved refs empty; validation reports what's required.
			return ""
		}
		_ = resolveErr
		return v
	})
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Stream.HeartbeatTimeout == 0 {
		c.Stream.HeartbeatTimeout = Duration(15 * time.Second)
	}
	if c.Stream.StaleHalt == 0 {
		c.Stream.StaleHalt = Duration(60 * time.Second)
	}
	if c.Stream.BackoffBase == 0 {
		c.Stream.BackoffBase = Duration(time.Second)
	}
	if c.Stream.BackoffMax == 0 {
		c.Stream.BackoffMax = Duration(60 * time.Second)
	}
	if c.Stream.TickChannelBuf == 0 {
		c.Stream.TickChannelBuf = 4096
	}
	if len(c.Stream.Instruments) == 0 {
		c.Stream.Instruments = c.ActiveInstruments()
	}
	if c.Candles.Price == "" {
		c.Candles.Price = "M"
	}
	if c.Candles.WindowMax == 0 {
		c.Candles.WindowMax = 50
	}
	if c.Candles.RESTConfirmDelay == 0 {
		c.Candles.RESTConfirmDelay = Duration(2 * time.Second)
	}
	if c.Candles.RESTConfirmTimeout == 0 {
		c.Candles.RESTConfirmTimeout = Duration(5 * time.Second)
	}
	if c.Executor.TimeInForce == "" {
		c.Executor.TimeInForce = "FOK"
	}
	if c.Executor.RequestTimeout == 0 {
		c.Executor.RequestTimeout = Duration(5 * time.Second)
	}
	if c.Executor.MaxRetries == 0 {
		c.Executor.MaxRetries = 3
	}
	if c.Executor.RetryBackoffBase == 0 {
		c.Executor.RetryBackoffBase = Duration(250 * time.Millisecond)
	}
	if c.Mgmt.TickInterval == 0 {
		c.Mgmt.TickInterval = Duration(2 * time.Second)
	}
	if c.Mgmt.ReconcileInterval == 0 {
		c.Mgmt.ReconcileInterval = Duration(20 * time.Second)
	}
	if c.Persistence.BufferSize == 0 {
		c.Persistence.BufferSize = 8192
	}
	if c.Persistence.FlushTimeout == 0 {
		c.Persistence.FlushTimeout = Duration(5 * time.Second)
	}
	if c.Backtest.InitialEquity == 0 {
		c.Backtest.InitialEquity = 5000
	}
	if c.Backtest.DataDir == "" {
		c.Backtest.DataDir = "data/candles"
	}
	if c.Backtest.OutputDir == "" {
		c.Backtest.OutputDir = "out"
	}
	c.Dashboard.applyDefaults(c)
}

func (d *DashboardConfig) applyDefaults(root *Config) {
	if d.ListenAddr == "" {
		d.ListenAddr = ":8080"
	}
	if d.Auth.Mode == "" {
		d.Auth.Mode = "bearer"
	}
	if d.OANDA.Host == "" {
		d.OANDA.Host = root.OANDA.Host
	}
	if d.OANDA.Token == "" {
		d.OANDA.Token = root.OANDA.Token
	}
	if len(d.Accounts) == 0 {
		for _, a := range root.Accounts {
			if !a.Active {
				continue
			}
			d.Accounts = append(d.Accounts, DashboardAccountConfig{
				Name: a.Name, OANDAID: a.OANDAID,
			})
		}
	}
	if d.CacheTTL.Overview == 0 {
		d.CacheTTL.Overview = Duration(60 * time.Second)
	}
	if d.CacheTTL.Calendar == 0 {
		d.CacheTTL.Calendar = Duration(60 * time.Second)
	}
	if d.CacheTTL.PL == 0 {
		d.CacheTTL.PL = Duration(180 * time.Second)
	}
	if d.UI.RefreshInterval == 0 {
		d.UI.RefreshInterval = Duration(20 * time.Second)
	}
	if d.UI.PLDailyLookbackDays == 0 {
		d.UI.PLDailyLookbackDays = 30
	}
	if d.UI.ReportingTZ == "" {
		d.UI.ReportingTZ = "UTC"
	}
	if d.Health.HeartbeatStale == 0 {
		d.Health.HeartbeatStale = Duration(120 * time.Second)
	}
	if d.Health.TickStale == 0 {
		d.Health.TickStale = Duration(60 * time.Second)
	}
	if d.Health.CalendarStale == 0 {
		d.Health.CalendarStale = Duration(90 * time.Minute)
	}
	if d.BigQuery.Dataset == "" {
		d.BigQuery.Dataset = "tradex"
	}
	if d.BigQuery.Table == "" {
		d.BigQuery.Table = "trade_ledger"
	}
	if d.BigQuery.Project == "" {
		d.BigQuery.Project = root.Project
	}
}

// ActiveInstruments returns the union of instruments across active accounts.
func (c *Config) ActiveInstruments() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range c.Accounts {
		if !a.Active {
			continue
		}
		for _, in := range a.Instruments {
			if !seen[in] {
				seen[in] = true
				out = append(out, in)
			}
		}
	}
	return out
}

// AccountFor returns the active account config that trades instrument.
func (c *Config) AccountFor(instrument string) (*AccountConfig, bool) {
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if !a.Active {
			continue
		}
		for _, in := range a.Instruments {
			if in == instrument {
				return a, true
			}
		}
	}
	return nil, false
}
