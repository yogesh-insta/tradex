# Spec: Observability (alerts, liveness, reconciliation)

## Purpose

Make the system's health legible and page a human before a silent failure costs money.
Covers liveness, market-data staleness, execution failures, RAM↔OANDA reconciliation,
and drawdown/limit monitoring. Calendar review and Tier 1 paper-trading alerts
go to Telegram today. Metrics target **Cloud Monitoring** remains deferred behind
`metrics.Registry`.

## Signals & alerts

| Signal | Condition | Severity | Action |
| --- | --- | --- | --- |
| **Stream staleness** | no ticks > `stale_halt` during EU session | high | page; risk blocks entries (`02-market-data-stream.md`) |
| **Stream reconnect churn** | > N reconnects / 5 min | warn | notify; investigate network |
| **Execution failure** | order open/modify/close error after retries | high | page; include OANDA reason |
| **Reconciliation drift** | RAM open-trades ≠ `OpenTrades()` | high | page; trust OANDA; auto-resync |
| **Daily drawdown** | realized loss approaching −$150 (e.g. −$120) | warn | notify (breaker at −$150) |
| **Breaker tripped** | state → `SYSTEM_LOCKED` | high | notify: reason + that `RE_ARM` is required |
| **Calendar fail-safe** | calendar stale/missing → blocking entries | warn | notify; check poller |
| **Calendar pipeline fail** | Finnhub + Gemini both fail, or Telegram review send fails | warn | notify; keep last good state; see `10-economic-calendar.md` |
| **VM liveness** | `status.json` heartbeat missing/stale > `liveness_timeout` | high | Telegram (VM systemd timer) |
| **Publisher saturation** | telemetry buffer drop-oldest firing | warn | notify; Pub/Sub health |

## Behavior

1. **Heartbeat:** the trader writes the canonical `status.json` locally and to
   `observability.status_object` every `heartbeat_interval`. The VM's
   `tradex-heartbeat-check.timer` fetches the GCS object every minute and sends
   a Telegram alert if `as_of` is stale, missing, or unreadable. Alerts repeat
   no more than once per 15 minutes while unhealthy.
2. **Reconciliation:** every `reconcile_interval`, compare in-RAM open trades to
   `executor.OpenTrades()`. On drift, **OANDA is truth**: adopt its view, re-attach
   `ManagementPolicy` where possible (from ledger), alert with the diff.
3. **Metrics (Cloud Monitoring):** stream up/last-tick age, reconnect count, order
   success/fail counts + latency, open positions, daily realized P&L, current state,
   calendar freshness (`as_of` age), publisher buffer depth.
4. **Telegram lifecycle notifier:** the trader decorates its asynchronous event
   publisher with a bounded Telegram queue. It sends boot ACTIVE, trade opened,
   trade closed, process-state, and account breaker-lock messages. Network calls
   are never made on the synchronous order path. A systemd `OnFailure=` unit
   also sends a process-failure message if `tradex.service` enters failed state.
5. **Calendar review (poller) — implemented:** after each successful Finnhub/Gemini
   compile, `cmd/calendarpoller` POSTs a review message to Telegram
   (`https://api.telegram.org/bot<TELEGRAM_BOT_TOKEN>/sendMessage`) with source header
   (`Finnhub Primary` / `Gemini Fallback Pipeline`) and a fenced `json` body — ops
   validation channel, not a trading alert. Normative detail:
   [`10-economic-calendar.md`](./10-economic-calendar.md).
6. **Analytics/KPI (scheduled SQL) — deferred:** Sharpe, profit factor, max drawdown,
   win rate, equity curve over `trade_ledger` — reporting only, off the hot path.

## Config keys

```yaml
observability:
  heartbeat_interval: 30s
  liveness_timeout: 120s
  reconcile_interval: 20s
  status_file: "data/status.json"
  status_object: "gs://tradex-demo-state/status.json"
  reconnect_churn_window: 5m
  reconnect_churn_max: 5
  drawdown_warn: 120        # USD, warn before -150 breaker
  telegram:
    chat_id_ref: "projects/…/secrets/telegram-chat"
    token_ref: "projects/…/secrets/telegram-token"
  alert_topic: "alerts"
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Telegram down | Metrics/alerts still in Cloud Monitoring; retry sends; don't block trading |
| Reconcile can't reach OANDA | Alert; keep last view; risk already blocks on data health |
| False-positive drift on race | Debounce one cycle before alerting/resyncing |
| Alert storm | Rate-limit/coalesce per signal type |

## Acceptance criteria

- Stopping the trader causes the GCS heartbeat to become stale and triggers
  Telegram within `liveness_timeout` plus the one-minute timer cadence.
- Manually closing a trade at the OANDA console makes reconciliation detect the drift
  and adopt OANDA's view — without the loop erroring.
- Approaching −$120 realized loss warns (when notifier is wired); −$150 trips the
  breaker and locks regardless.
- Calendar poller review messages reach Telegram after a successful compile.
- Opening/closing a paper trade, booting ACTIVE, and breaker locks send Telegram
  notifications without blocking order execution.

## Out of scope

- Taking trading action (risk/control-plane own that). Tick-level analytics (deferred).
- Operator pull UI / per-day P&L dashboard — see `14-dashboard.md` (Cloud Run, read-only).
- Implementing the trade/session Telegram notifier in this revision (explicitly deferred).
