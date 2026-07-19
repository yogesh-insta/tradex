package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
env: "test"
project: "tradex-test"
oanda:
  host: "api-fxpractice.oanda.com"
  stream_host: "stream-fxpractice.oanda.com"
  token: "${TEST_OANDA_TOKEN}"
accounts:
  - name: "eu-indices"
    oanda_account_id: "${TEST_OANDA_ACCOUNT}"
    instruments: ["DE30_EUR", "FR40_EUR"]
    strategy: "eu_love"
    active: true
eu_session:
  tz: "Europe/Berlin"
  range_start: "08:00:00"
  range_end: "09:00:00"
  trade_window_start: "09:05:00"
  atr_period_days: 14
  vol_ma_candles: 12
  instruments: ["DE30_EUR", "FR40_EUR"]
strategies:
  eu_love:
    volume_spike_mult: 1.0
    sl_atr_mult: 0.5
    tp_atr_mult: 1.5
    breakeven_at_r: 1.0
    entry_window_end: "11:00:00"
risk:
  risk_per_trade: 0.01
  daily_loss_limit: 150
  consecutive_loss_halt: 3
  max_concurrent: 1
  max_margin_frac: 0.10
  max_leverage: 5.0
  news_block_before: 30m
  correlation_groups: [["DE30_EUR","FR40_EUR"]]
mgmt:
  eu_friday_cutoff: "17:30:00"
control_plane:
  listen: ":8443"
  auth:
    secret: "${TEST_HMAC}"
    max_skew: 60s
    nonce_ttl: 300s
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("TEST_OANDA_TOKEN", "tok-123")
	t.Setenv("TEST_OANDA_ACCOUNT", "101-004-1234567-001")
	t.Setenv("TEST_HMAC", "hmac-secret-value")
}

func TestLoadResolvesSecretsAndDefaults(t *testing.T) {
	setSecrets(t)
	cfg, err := Load(writeConfig(t, validYAML), EnvResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OANDA.Token != "tok-123" {
		t.Fatalf("token = %q", cfg.OANDA.Token)
	}
	if cfg.Accounts[0].OANDAID != "101-004-1234567-001" {
		t.Fatalf("account id = %q", cfg.Accounts[0].OANDAID)
	}
	// Defaults applied.
	if cfg.Stream.HeartbeatTimeout.D() != 15*time.Second {
		t.Fatalf("heartbeat default = %v", cfg.Stream.HeartbeatTimeout.D())
	}
	if cfg.Candles.WindowMax != 50 {
		t.Fatalf("window_max default = %d", cfg.Candles.WindowMax)
	}
	// Instruments derived from active accounts.
	if got := cfg.Stream.Instruments; len(got) != 2 || got[0] != "DE30_EUR" {
		t.Fatalf("derived instruments = %v", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Redacted startup log must not leak secret values.
	redacted := fmt.Sprint(cfg.Redacted())
	for _, secret := range []string{"tok-123", "hmac-secret-value", "101-004-1234567-001"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("secret %q leaked in redacted config: %s", secret, redacted)
		}
	}
}

func TestValidateFailsFast(t *testing.T) {
	setSecrets(t)
	tests := []struct {
		name     string
		mutate   func(string) string
		wantHint string
	}{
		{
			name:     "missing token env",
			mutate:   func(y string) string { return strings.ReplaceAll(y, "TEST_OANDA_TOKEN", "UNSET_TOKEN_VAR") },
			wantHint: "oanda.token",
		},
		{
			name:     "range end before start",
			mutate:   func(y string) string { return strings.ReplaceAll(y, `range_end: "09:00:00"`, `range_end: "07:00:00"`) },
			wantHint: "range_end",
		},
		{
			name:     "bad timezone",
			mutate:   func(y string) string { return strings.ReplaceAll(y, "Europe/Berlin", "Mars/OlympusMons") },
			wantHint: "tz",
		},
		{
			name:     "zero ATR multiplier",
			mutate:   func(y string) string { return strings.ReplaceAll(y, "sl_atr_mult: 0.5", "sl_atr_mult: 0") },
			wantHint: "sl_atr_mult",
		},
		{
			name:     "daily loss limit missing",
			mutate:   func(y string) string { return strings.ReplaceAll(y, "daily_loss_limit: 150", "daily_loss_limit: 0") },
			wantHint: "daily_loss_limit",
		},
		{
			name:     "no active accounts",
			mutate:   func(y string) string { return strings.ReplaceAll(y, "active: true", "active: false") },
			wantHint: "no active accounts",
		},
		{
			name:     "missing HMAC secret",
			mutate:   func(y string) string { return strings.ReplaceAll(y, "TEST_HMAC", "UNSET_HMAC_VAR") },
			wantHint: "control_plane.auth.secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.mutate(validYAML)), EnvResolver{})
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.Validate()
			if err == nil {
				t.Fatal("expected validation failure")
			}
			if !strings.Contains(err.Error(), tt.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tt.wantHint)
			}
		})
	}
}

func TestRiskConfigMerge(t *testing.T) {
	global := RiskConfig{
		RiskPerTrade: 0.01, DailyLossLimit: 150, ConsecutiveLossHalt: 3,
		MaxConcurrent: 1, MaxMarginFrac: 0.10, MaxLeverage: 5,
	}
	merged := global.Merged(&RiskConfig{DailyLossLimit: 100, RiskPerTrade: 0.005})
	if merged.DailyLossLimit != 100 || merged.RiskPerTrade != 0.005 {
		t.Fatalf("overrides not applied: %+v", merged)
	}
	if merged.ConsecutiveLossHalt != 3 || merged.MaxLeverage != 5 {
		t.Fatalf("globals not preserved: %+v", merged)
	}
	same := global.Merged(nil)
	if same.DailyLossLimit != global.DailyLossLimit || same.RiskPerTrade != global.RiskPerTrade {
		t.Fatal("nil override must return the global config")
	}
}

func TestAccountFor(t *testing.T) {
	setSecrets(t)
	cfg, err := Load(writeConfig(t, validYAML), EnvResolver{})
	if err != nil {
		t.Fatal(err)
	}
	ac, ok := cfg.AccountFor("FR40_EUR")
	if !ok || ac.Name != "eu-indices" {
		t.Fatalf("AccountFor = %v, %v", ac, ok)
	}
	if _, ok := cfg.AccountFor("XAU_USD"); ok {
		t.Fatal("unexpected account for unassigned instrument")
	}
}

func TestValidateDashboard(t *testing.T) {
	setSecrets(t)
	t.Setenv("DASHBOARD_TOKEN", "dash-secret")
	body := validYAML + `
dashboard:
  auth:
    mode: "bearer"
    token: "${DASHBOARD_TOKEN}"
  calendar_file: "data/calendar-state.json"
  ledger_file: "data/trade-ledger.json"
`
	cfg, err := Load(writeConfig(t, body), EnvResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateDashboard(); err != nil {
		t.Fatalf("valid dashboard rejected: %v", err)
	}
	if cfg.Dashboard.ListenAddr != ":8080" {
		t.Fatalf("listen default = %q", cfg.Dashboard.ListenAddr)
	}
	if len(cfg.Dashboard.Accounts) != 1 || cfg.Dashboard.Accounts[0].Name != "eu-indices" {
		t.Fatalf("accounts not inherited: %+v", cfg.Dashboard.Accounts)
	}

	// Fail closed without bearer token.
	cfg.Dashboard.Auth.Token = ""
	cfg.Dashboard.Mock = false
	if err := cfg.ValidateDashboard(); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("expected token failure, got %v", err)
	}

	// Mock mode skips external secret requirements.
	cfg.Dashboard.Mock = true
	if err := cfg.ValidateDashboard(); err != nil {
		t.Fatalf("mock dashboard rejected: %v", err)
	}
}
