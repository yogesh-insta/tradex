package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dashboardYAML = `
env: "test"
project: "tradex-test"
dashboard:
  auth:
    mode: "bearer"
    token: "${DASHBOARD_TOKEN}"
  ui: { refresh_interval: 30s, reporting_tz: "Australia/Sydney" }
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load(writeConfig(t, dashboardYAML), EnvResolver{})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLoadResolvesSecretsAndDefaults(t *testing.T) {
	t.Setenv("DASHBOARD_TOKEN", "dash-secret")
	cfg := load(t)
	if cfg.Dashboard.Auth.Token != "dash-secret" {
		t.Fatalf("token = %q, want the resolved env value", cfg.Dashboard.Auth.Token)
	}
	if cfg.Dashboard.UI.RefreshInterval.D() != 30*time.Second {
		t.Fatalf("refresh_interval = %v", cfg.Dashboard.UI.RefreshInterval.D())
	}
	if cfg.Dashboard.UI.ReportingTZ != "Australia/Sydney" {
		t.Fatalf("reporting_tz = %q", cfg.Dashboard.UI.ReportingTZ)
	}
	// Defaults fill what the YAML omits.
	if cfg.Dashboard.ListenAddr != ":8080" {
		t.Fatalf("listen default = %q", cfg.Dashboard.ListenAddr)
	}
	if cfg.Dashboard.CacheTTL.Overview.D() != 60*time.Second {
		t.Fatalf("cache_ttl default = %v", cfg.Dashboard.CacheTTL.Overview.D())
	}
}

// An unresolved ${VAR} must leave the field empty rather than embed the literal
// reference, so validation reports it missing and the process fails closed.
func TestLoadLeavesUnresolvedRefsEmpty(t *testing.T) {
	os.Unsetenv("DASHBOARD_TOKEN")
	cfg := load(t)
	if cfg.Dashboard.Auth.Token != "" {
		t.Fatalf("token = %q, want empty", cfg.Dashboard.Auth.Token)
	}
	if err := cfg.ValidateDashboard(); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("expected fail-closed token error, got %v", err)
	}
}

func TestValidateDashboard(t *testing.T) {
	t.Setenv("DASHBOARD_TOKEN", "dash-secret")
	cfg := load(t)
	if err := cfg.ValidateDashboard(); err != nil {
		t.Fatalf("valid dashboard rejected: %v", err)
	}

	// Fail closed without a bearer token.
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

func TestValidateDashboardRejectsBadFields(t *testing.T) {
	t.Setenv("DASHBOARD_TOKEN", "dash-secret")
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"unknown auth mode", func(c *Config) { c.Dashboard.Auth.Mode = "basic" }, "auth.mode"},
		{"bad timezone", func(c *Config) { c.Dashboard.UI.ReportingTZ = "Mars/Olympus" }, "reporting_tz"},
		{"empty listen addr", func(c *Config) { c.Dashboard.ListenAddr = "" }, "listen_addr"},
		{"zero refresh", func(c *Config) { c.Dashboard.UI.RefreshInterval = 0 }, "refresh_interval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := load(t)
			tc.mutate(cfg)
			err := cfg.ValidateDashboard()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

// Redacted() feeds a startup audit log, so it must never carry the token.
func TestRedactedOmitsSecrets(t *testing.T) {
	t.Setenv("DASHBOARD_TOKEN", "dash-secret")
	cfg := load(t)
	dump := fmt.Sprint(cfg.Redacted())
	if !strings.Contains(dump, "tradex-test") {
		t.Fatalf("Redacted() lost the project: %s", dump)
	}
	if strings.Contains(dump, "dash-secret") {
		t.Fatalf("Redacted() leaked the bearer token: %s", dump)
	}
}
