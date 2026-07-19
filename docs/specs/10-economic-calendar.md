# Spec: Calendar (economic events + trading holidays)

## Purpose

Provide the VM with **durable, fail-safe** answers to two questions:
1. "How long until the next high-impact economic event for this region?" (news filter)
2. "Is the market open today / is it a half-day?" (session logic)

Neither is a free-flowing event; both resolve to durable state (see `architecture.md` §5).

This supersedes the earlier interim **file-only / manual Google AI paste** lock. The
target design is an automated fetch pipeline with Telegram human review; the on-VM /
GCS file remains the **durable store** the trader reads after each successful run.

## Two calendars

### A. Economic events (news filter) — polled

- **Poller (Cloud Run, stateless, scheduled):** Cloud Scheduler triggers every
  `poll_interval` (15–30 min). The poller runs the resilient fetch pipeline below,
  posts a Telegram review message, then **writes `calendar-state.json` to GCS**
  (and/or the on-VM path) as durable state. It holds nothing between runs.
- **VM read path:** the VM reads the durable object into a **RAM cache** on its own
  timer (`vm_refresh`), exposing `minsToHighImpact(region)`.
- **EU trader file provider:** existing local-file / GCS read path for the EU trader
  **must keep working** — the poller only changes how the durable object is produced,
  not how the VM consumes it.
- **Fail-safe:** if the object is missing, unparseable, or `as_of` older than
  `staleness_max`, the VM treats **"event imminent"** → risk blocks entries and
  management moves stops to breakeven. Never trade blind.

### B. Trading holidays / half-days — checked-in config

- A **checked-in config file** (per coding guidelines), validated yearly against exchange
  calendars (Eurex/Xetra for DE30/Germany 40, Euronext for FR40). No external dependency,
  no failure mode. Used by the session controller to skip closed days and early closes.

## Inputs

- Finnhub Economic Calendar API (primary), authenticated with `FINNHUB_API_KEY`.
- Gemini Live Search via official Google GenAI Go SDK (fallback), authenticated with
  `GEMINI_API_KEY`, model `gemini-2.5-flash`.
- Telegram Bot API (`TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`) for review delivery.
- Holiday config file (VM).
- Clock: date window from `time.Now().UTC()` for the next **7 days**.

## Outputs

- Durable `calendar-state.json` (GCS and/or on-VM file) with fresh `as_of`.
- Telegram `sendMessage` for ops phone review (source header + JSON body).
- `minsToHighImpact(region) int` and `isTradingDay/earlyClose(date, market)` to risk,
  trade-management, and the session controller.

## Durable state schema

State schema (`calendar-state.json`) — unchanged contract for the VM / dashboard:

```json
{
  "as_of": "2026-07-17T06:00:00Z",
  "events": [
    { "region": "EU", "title": "ECB Rate Decision", "impact": "high",
      "time": "2026-07-17T12:15:00Z" },
    { "region": "US", "title": "FOMC Rate Decision", "impact": "high",
      "time": "2026-07-17T18:00:00Z" },
    { "region": "JP", "title": "BOJ Rate Decision", "impact": "high",
      "time": "2026-07-18T03:00:00Z" }
  ]
}
```

Event fields: `region` ∈ {`EU`,`US`,`JP`}, `impact` exactly `"high"`, `time` UTC
ISO-8601 with `Z`. Schema must match internal Calendar State / Event structs.

## Behavior

### Fetch → Telegram validate → write durable state

Recommended policy (target):

1. Poller compiles a **candidate** calendar JSON (Finnhub primary or Gemini fallback).
2. Poller **POSTs the candidate to Telegram** for ops review on phone.
3. On a successful Telegram send, poller **automatically writes** durable state
   (`calendar-state.json` / GCS) with a fresh `as_of` (`time.Now().UTC()`).
4. Telegram is the **validation channel** (human review). If ops finds the payload wrong,
   they override the durable file (see [`data/README-calendar.md`](../../data/README-calendar.md))
   — the auto-write still lands so the VM is not left on a stale object after a good fetch.

VM / EU file provider continues to read only the durable object; it does not call
Finnhub, Gemini, or Telegram.

### Exact execution path

#### 1. PRIMARY — Finnhub Free Tier

- Query Finnhub economic calendar for the next **7 days** from `time.Now().UTC()`.
- Filter to **High-Impact** for **US, JP, EU** (treat DE / FR / IT country codes as
  **EU proxies** when mapping into `region: "EU"`).
- If the response is valid and yields **≥ 1 matching event** after filtering → parse into
  the standard schema above.
- **Note:** Finnhub free tier may return **403** (or other auth/rate errors). Treat any
  hard failure, parse error, or **0 matching events** as a trigger for fallback — fallback
  is **required**, not optional.

#### 2. FALLBACK — Gemini Live Search

- Invoke when Finnhub fails, errors, or returns 0 matching events.
- Use the **official Google GenAI Go SDK**, model **`gemini-2.5-flash`**.
- Enable **Live Google Search Grounding** so the model consults real-time public calendars.
- Enforce a **strict JSON schema** matching the internal Calendar State / Event structs
  (same durable schema as above). Reject / retry on schema mismatch.

#### 3. FILTERING (both paths)

Apply before Telegram / durable write:

| Rule | Value |
| --- | --- |
| Regions | `US`, `JP`, `EU` only (DE/FR/IT → `EU`) |
| Impact | `"high"` only |
| Event types | Rate Decisions / Press Conferences (FOMC / ECB / BOJ); CPI / PPI / PCE; NFP; GDP (Advance); Tankan |
| Timestamps | UTC ISO-8601 with `Z` |

Drop medium/low impact, speeches (unless already classified high-impact rate/press
events), and unrelated countries.

#### 4. TELEGRAM

After compiling final calendar JSON (Finnhub or Gemini):

- `POST https://api.telegram.org/bot<TELEGRAM_BOT_TOKEN>/sendMessage`
  (Bot API host is `api.telegram.org` — not `telegram.org`).
- Message body:
  1. Header stating source: **`Finnhub Primary`** or **`Gemini Fallback Pipeline`**.
  2. The calendar JSON inside a Markdown fenced `json` code block for phone review.
- Chat target: `TELEGRAM_CHAT_ID`.
- On send success → auto-write durable state with fresh `as_of` (recommended policy above).
- On send failure → do **not** silently treat review as done; alert via normal
  observability path; keep last good durable object (VM fail-safe if it goes stale).

### Secrets / env (no hardcoding)

| Env / secret | Purpose |
| --- | --- |
| `FINNHUB_API_KEY` | Finnhub primary |
| `GEMINI_API_KEY` | Gemini fallback |
| `TELEGRAM_BOT_TOKEN` | Bot API auth |
| `TELEGRAM_CHAT_ID` | Review chat |

Resolve from Secret Manager (or env in local/dev). Never commit keys.

## Config keys

```yaml
calendar:
  economic:
    provider: "finnhub"           # primary; gemini is automatic fallback
    fallback_provider: "gemini"
    gemini_model: "gemini-2.5-flash"
    poll_interval: 20m
    lookback_days: 0
    lookahead_days: 7             # window: now UTC → +7d
    gcs_object: "gs://tradex-<env>-state/calendar-state.json"
    local_file: "data/calendar-state.json"   # durable on-VM / ops override path
    vm_refresh: 5m
    staleness_max: 90m            # older as_of => fail-safe "event imminent"
    high_impact_only: true
    regions: ["EU", "US", "JP"]
    telegram_review: true         # POST candidate after compile
    auto_write_on_telegram_ok: true
  holidays:
    file: "config/holidays.yaml"
    markets: ["XETR", "XPAR"]     # Eurex/Xetra, Euronext Paris
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Finnhub 403 / down / rate-limited / 0 matches | Invoke Gemini fallback |
| Gemini fail / schema reject | Keep last good durable object; alert; if stale → VM fail-safe |
| Both pipelines fail | Keep last good object; ops optional manual backup ([`data/README-calendar.md`](../../data/README-calendar.md)); if stale → fail-safe |
| Telegram send fails | Alert; do not assume review delivered; keep last good durable object unless a separate write policy is explicitly configured |
| GCS / local object missing/corrupt | VM treats as "event imminent" (block entries) + alert |
| Poller cold-start delay | Irrelevant — VM reads durable state, not the poller |
| Timezone in provider payload | Normalize all event times to UTC `Z` on write |
| Holiday file out of date | Yearly validation task; alert if current year missing |

## Acceptance criteria

- With a high-impact EU event 20 min out, `minsToHighImpact("EU") <= 30` and risk blocks
  entries / mgmt moves stops to breakeven.
- Finnhub returning 403 or 0 high-impact US/JP/EU events causes Gemini fallback to run.
- After a successful compile, Telegram receives a message with the correct source header
  and a fenced `json` code block; durable state is written with a fresh `as_of`.
- Deleting the GCS / local object makes the VM fail-safe (blocks entries) without crashing.
- EU trader file/GCS provider path continues to work unchanged for consumers.
- On a Eurex holiday, the session controller does not activate for DE30.
- No API tokens appear in config files, logs, or Telegram message bodies beyond the
  calendar JSON itself.

## Out of scope

- Acting on the signal (risk/trade-management own that). Building the ML feature set.
- Changing holiday-calendar ownership (still checked-in YAML).

## Related / Manual ops

Primary path is the automated poller (Finnhub → Gemini → Telegram → durable write).
Ops reviews Telegram; overrides the durable file only when the candidate is wrong.
The weekly Google AI web-paste ritual in [`data/README-calendar.md`](../../data/README-calendar.md)
is an **optional backup** if both pipelines fail.
