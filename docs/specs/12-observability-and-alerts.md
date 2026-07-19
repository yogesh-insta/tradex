# Spec: Observability (alerts, liveness, reconciliation)

## Purpose

Make the system's health legible and page a human before a silent failure costs money.
Covers liveness, market-data staleness, execution failures, RAM↔OANDA reconciliation,
and drawdown/limit monitoring. **Calendar review** messages go to Telegram today
(`cmd/calendarpoller`). **Trade / session / breaker Telegram alerts** (Pub/Sub →
Cloud Run notifier) are **deferred** — designed here, not yet wired. Metrics target
**Cloud Monitoring** (also deferred behind `metrics.Registry`).

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
| **VM liveness** | heartbeat missing > `liveness_timeout` | high | page (external uptime check) |
| **Publisher saturation** | telemetry buffer drop-oldest firing | warn | notify; Pub/Sub health |

## Behavior

1. **Heartbeat:** the VM emits a liveness metric every `heartbeat_interval`; an external
   Cloud Monitoring uptime/alert policy pages if it stops (covers total VM death).
2. **Reconciliation:** every `reconcile_interval`, compare in-RAM open trades to
   `executor.OpenTrades()`. On drift, **OANDA is truth**: adopt its view, re-attach
   `ManagementPolicy` where possible (from ledger), alert with the diff.
3. **Metrics (Cloud Monitoring):** stream up/last-tick age, reconnect count, order
   success/fail counts + latency, open positions, daily realized P&L, current state,
   calendar freshness (`as_of` age), publisher buffer depth.
4. **Telegram notifier (Cloud Run) — deferred:** will consume alert events from Pub/Sub
   and format concise messages (state changes, breaker, execution errors, daily P&L
   summary at session end). Until then, operators use the control-plane `STATUS`
   webhook, logs, and the read-only dashboard (`14-dashboard.md`).
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

- Killing the VM triggers a liveness page within `liveness_timeout` (when Monitoring is
  wired).
- Manually closing a trade at the OANDA console makes reconciliation detect the drift
  and adopt OANDA's view — without the loop erroring.
- Approaching −$120 realized loss warns (when notifier is wired); −$150 trips the
  breaker and locks regardless.
- Calendar poller review messages reach Telegram after a successful compile.
- Trade/session Telegram alerts remain deferred until the Pub/Sub notifier lands.

## Out of scope

- Taking trading action (risk/control-plane own that). Tick-level analytics (deferred).
- Operator pull UI / per-day P&L dashboard — see `14-dashboard.md` (Cloud Run, read-only).
- Implementing the trade/session Telegram notifier in this revision (explicitly deferred).
