# Spec: Calendar (economic events + trading holidays)

## Purpose

Provide the VM with **durable, fail-safe** answers to two questions:
1. "How long until the next high-impact economic event for this region?" (news filter)
2. "Is the market open today / is it a half-day?" (session logic)

Neither is a free-flowing event; both resolve to durable state (see `architecture.md` §5).

## Two calendars

### A. Economic events (news filter) — polled

- **Poller (Cloud Run, stateless, scheduled):** Cloud Scheduler triggers every
  `poll_interval` (15–30 min). The poller fetches the provider, normalizes to a compact
  schema, and **writes `calendar-state.json` to GCS** (durable). It holds nothing between
  runs.
- **Provider interface (swappable):** default **Finnhub** free tier; alternates FMP,
  Forex Factory XML, Trading Economics (paid). Behind `EconomicCalendarProvider`.
- **VM read path:** the VM reads the GCS object into a **RAM cache** on its own timer
  (`vm_refresh`), exposing `minsToHighImpact(region)`.
- **Fail-safe:** if the object is missing, unparseable, or `as_of` older than
  `staleness_max`, the VM treats **"event imminent"** → risk blocks entries and management
  moves stops to breakeven. Never trade blind.

State schema (`calendar-state.json`):

```json
{
  "as_of": "2026-07-17T06:00:00Z",
  "events": [
    { "region": "EU", "title": "ECB Rate Decision", "impact": "high",
      "time": "2026-07-17T12:15:00Z" },
    { "region": "EU", "title": "Eurozone CPI", "impact": "high",
      "time": "2026-07-17T09:00:00Z" }
  ]
}
```

### B. Trading holidays / half-days — checked-in config

- A **checked-in config file** (per coding guidelines), validated yearly against exchange
  calendars (Eurex/Xetra for DE40, Euronext for FR40). No external dependency, no failure
  mode. Used by the session controller to skip closed days and early closes.

## Inputs / Outputs

- In: provider API (poller), GCS object (VM), holiday config file (VM).
- Out: `minsToHighImpact(region) int` and `isTradingDay/earlyClose(date, market)` to risk,
  trade-management, and the session controller.

## Config keys

```yaml
calendar:
  economic:
    provider: "finnhub"
    poll_interval: 20m
    gcs_object: "gs://tradex-<env>-state/calendar-state.json"
    vm_refresh: 5m
    staleness_max: 90m        # older as_of => fail-safe "event imminent"
    high_impact_only: true
    regions: ["EU"]
  holidays:
    file: "config/holidays.yaml"
    markets: ["XETR", "XPAR"]  # Eurex/Xetra, Euronext Paris
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Provider down / rate-limited | Poller keeps last good GCS object; if it goes stale → VM fail-safe |
| GCS object missing/corrupt | VM treats as "event imminent" (block entries) + alert |
| Poller cold-start delay | Irrelevant — VM reads durable state, not the poller |
| Timezone in provider payload | Normalize all event times to UTC on write |
| Holiday file out of date | Yearly validation task; alert if current year missing |

## Acceptance criteria

- With a high-impact EU event 20 min out, `minsToHighImpact("EU") <= 30` and risk blocks
  entries / mgmt moves stops to breakeven.
- Deleting the GCS object makes the VM fail-safe (blocks entries) without crashing.
- On a Eurex holiday, the session controller does not activate for DE40.

## Out of scope

- Acting on the signal (risk/trade-management own that). Building the ML feature set.
