package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ValidateDashboard fail-fast checks the Cloud Run dashboard config
// (docs/specs/14-dashboard.md). Bad config must stop the process at startup
// rather than surface as a broken page.
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
	if _, err := time.LoadLocation(d.UI.ReportingTZ); err != nil || d.UI.ReportingTZ == "" {
		add("dashboard.ui.reporting_tz %q is not a valid IANA timezone", d.UI.ReportingTZ)
	}
	if d.CacheTTL.Overview <= 0 {
		add("dashboard.cache_ttl.overview must be > 0")
	}
	if d.UI.RefreshInterval <= 0 {
		add("dashboard.ui.refresh_interval must be > 0")
	}
	return errors.Join(errs...)
}

// Redacted returns a human-readable summary of the effective config with all
// secret material removed, suitable for startup audit logging.
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"env":     c.Env,
		"project": c.Project,
		"dashboard": map[string]any{
			"listen":       c.Dashboard.ListenAddr,
			"mock":         c.Dashboard.Mock,
			"auth_mode":    c.Dashboard.Auth.Mode,
			"reporting_tz": c.Dashboard.UI.ReportingTZ,
		},
	}
}
