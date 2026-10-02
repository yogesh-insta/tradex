# Tradex

**Stack:** Go, Cloud Run, Cloud Scheduler, Gemini, Telegram

**Skills:** Serverless services, portfolio analytics, scheduled jobs


Cloud utility and analytics suite in Go, running on Google Cloud Run.

## Architecture & Retained Services

The repository is configured for serverless execution on **GCP Cloud Run** with automated **Cloud Scheduler** cron triggers:

1. **Ops Dashboard (`cmd/dashboard`)**: Read-only operations UI and REST API for portfolio and analytics metrics.
2. **NSE Sector & Stock Rotator (`cmd/nserotator`)**: Monthly/daily rotational momentum advisory service for NSE stocks.
3. **ASX ETF Monitor (`cmd/etfmonitor`)**: Monthly momentum advisory report service for ASX ETFs.
4. **Economic Calendar Poller (`cmd/calendarpoller`)**: Ingests economic calendar announcements (Finnhub → Gemini AI → Telegram alerts → durable `calendar-state.json`).

*Note: Infrastructure deployment scripts (`deploy/scripts/deploy.sh`, `deploy/systemd/`, `deploy/docker/Dockerfile`) are preserved in the repository so a dedicated VM instance can be re-provisioned if needed.*

## Repository Layout

```
cmd/calendarpoller/    Finnhub → Gemini → Telegram → durable calendar-state.json
cmd/dashboard/         read-only Cloud Run ops UI (spec 14)
cmd/etfmonitor/        ASX ETF momentum report service (spec 21)
cmd/nserotator/        NSE rotational momentum advisory service (spec 20)
internal/calendar/     economic-calendar cache + poller pipeline + trading-holiday file
internal/config/       YAML loader + ${ENV} secret resolution + fail-fast validation
internal/dashboard/    ops dashboard APIs, auth, caches, embedded UI
internal/etfmonitor/   ASX ETF monitor calculation & report generator
internal/logging/      slog JSON logging
internal/metrics/      counters/gauges metrics abstraction
internal/nserotator/   NSE rotator strategy & advisory generator
internal/status/       status and health tracking
pkg/types/             shared data contracts
pkg/utils/             IANA/DST-safe clock helpers, price rounding
config/                dashboard*.yaml + holidays.yaml
data/                  calendar-state.json + ops README
deploy/                Dockerfile(s), scripts, systemd unit, deploy scripts
.github/workflows/     ci.yml (vet+lint+test+build)
```

## Quickstart

Requirements: Go 1.25+.

```bash
# 1. Build + test
go build ./... && go test ./...

# 2. Run Dashboard locally (Mock mode)
export DASHBOARD_TOKEN=dev
go run ./cmd/dashboard --config config/config.dashboard.mock.yaml
# Open http://localhost:8080/?token=dev

# 3. Deploy Cloud Run services
./deploy/scripts/deploy-dashboard.sh
./deploy/scripts/deploy-calendarpoller.sh
./deploy/scripts/deploy-nserotator.sh
./deploy/scripts/deploy-etfmonitor.sh
```
