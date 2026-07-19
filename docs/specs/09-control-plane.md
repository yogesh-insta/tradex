# Spec: Control Plane (command webhook)

## Purpose

Turn the outbound-only alert pipeline into a two-way admin console: a small, secure
inbound HTTP API on the VM that drives a **Master Control Routine** state machine and
executes operational commands (`FLATTEN`, `PAUSE`, `RESUME`, `RE_ARM`).

## Inputs

- HTTPS `POST` to the VM on **:8443**, `application/json`, **HMAC-signed** (see Auth).
- Internal triggers: risk breaker (daily loss / consecutive loss) locks only the breached
  account. Operator commands remain process-wide.

## Outputs

- State transitions (in RAM, authoritative for the process).
- Executor calls (`Close`, `CancelOrder`) on flatten / re-arm paths.
- JSON response with current `SystemState` (and `STATUS` detail).
- **Deferred:** Telegram confirmation of each accepted command (trade/session Telegram
  notifier is deferred — see `12-observability-and-alerts.md`).

## State machine

```text
        PAUSE                         RESUME
ACTIVE ───────► PAUSED ───────────────────► ACTIVE
  │                                             ▲
  └───────────────────► DISABLED  ─────────────┘  RE_ARM
       FLATTEN from any state

Risk breaker: account ACTIVE → account LOCKED → account ACTIVE (signed RE_ARM).
```

- `ACTIVE` — normal trading.
- `PAUSED` — **process-wide** (v1): risk rejects new entries on all accounts;
  **open positions untouched** (brackets + management still protect them).
- `SYSTEM_LOCKED` — legacy/manual process-wide hard lock; **no new entries**. Risk
  breakers instead use account locks, so an EU breaker does not stop FX and vice versa.
  Managed positions keep broker stops. **No automatic midnight re-arm.**
- `DISABLED` — post-`FLATTEN`: everything blocked; requires operator action to resume.

## Commands

| Command | Effect |
| --- | --- |
| `FLATTEN` | Block all strategy channels → `DELETE` all resting orders → market-`Close` all open trades → state `DISABLED`. Emergency stop. |
| `PAUSE` | Process-wide pause → `PAUSED`. Positions untouched. (Per-market pause is future.) |
| `RESUME` | Wake process → `ACTIVE` (if not locked/disabled). |
| `RE_ARM` | Clear all breaker-locked accounts, snapshot each affected account's new baseline equity; also exits legacy `SYSTEM_LOCKED`/`DISABLED` → `ACTIVE`. |
| `STATUS` | Return process state plus per-account open trades, daily P&L, baseline equity, `locked`, and `lock_reason` (read-only). |

## Auth (HMAC over TLS)

- Payload: `{ "command": "...", "args": {...}, "nonce": "...", "ts": <unix> }`.
- Header `X-Signature: hex(HMAC_SHA256(secret, rawBody))`; secret in Secret Manager.
- Reject if: bad signature, `ts` skew > `auth.max_skew` (60s), or `nonce` seen before
  (replay cache, TTL = `auth.nonce_ttl`). All rejects logged + alerted.
- TLS terminates on the VM (self-managed cert or fronted); bind to the admin interface;
  firewall :8443 to known admin source ranges.

## Config keys

```yaml
control_plane:
  listen: ":8443"
  auth:
    secret_ref: "projects/…/secrets/control-hmac"
    max_skew: 60s
    nonce_ttl: 300s
  allow_cidrs: ["<admin-ip-range>"]
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Invalid signature / replay | 401, log + alert, no state change |
| `FLATTEN` partial (some closes fail) | Retry failed closes; stay `DISABLED`; alert until flat |
| `RE_ARM` while no account/global lock exists | No-op with explicit response (idempotent) |
| Webhook unreachable | Breaker still works internally; operator falls back to OANDA console |
| Process restart | State rebuilt: if a daily/consecutive breaker is breached from ledger/account → lock that account only |

## Acceptance criteria

- A correctly-signed `FLATTEN` cancels all resting orders and closes all trades, ending
  in `DISABLED`; an unsigned/replayed request is rejected with no effect.
- Hitting −$150 realized loss locks the breached account; its entries remain rejected until
  a signed `RE_ARM`, which snapshots a fresh baseline equity without blocking other accounts.
- `PAUSE` stops new EU entries while an open trade continues to be managed to its exit.
- State survives restart consistently with account/ledger reality.

## Out of scope

- Sizing/risk math (risk). Order mechanics (executor). Alert formatting (observability).
