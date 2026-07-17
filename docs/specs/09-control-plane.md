# Spec: Control Plane (command webhook)

## Purpose

Turn the outbound-only alert pipeline into a two-way admin console: a small, secure
inbound HTTP API on the VM that drives a **Master Control Routine** state machine and
executes operational commands (`FLATTEN`, `PAUSE`, `RESUME`, `RE_ARM`).

## Inputs

- HTTPS `POST` to the VM on **:8443**, `application/json`, **HMAC-signed** (see Auth).
- Internal triggers: risk breaker (daily loss / consecutive loss) forces `SYSTEM_LOCKED`.

## Outputs

- State transitions (in RAM, authoritative for the process).
- Executor calls (`Close`, `CancelOrder`), controller pause/resume flags.
- Telegram confirmation of each accepted command (outbound).

## State machine

```text
        PAUSE                         RESUME
ACTIVE ───────► PAUSED ───────────────────► ACTIVE
  │  ▲                                         ▲
  │  │ RE_ARM (signed)                         │
  │  └─────────────── SYSTEM_LOCKED ◄──────────┘  (breaker: daily/consecutive loss)
  │        FLATTEN                              (RE_ARM is the ONLY exit)
  └───────────────────► DISABLED  ◄── FLATTEN from any state
```

- `ACTIVE` — normal trading.
- `PAUSED` — per-market controllers asleep; **open positions untouched** (brackets +
  management still protect them). New entries suppressed.
- `SYSTEM_LOCKED` — hard lock after a breaker; **no new entries**; managed positions keep
  broker stops. **No automatic midnight re-arm.** Exit only via signed `RE_ARM`.
- `DISABLED` — post-`FLATTEN`: everything blocked; requires operator action to resume.

## Commands

| Command | Effect |
| --- | --- |
| `FLATTEN` | Block all strategy channels → `DELETE` all resting orders → market-`Close` all open trades → state `DISABLED`. Emergency stop. |
| `PAUSE {market?}` | Sleep the named market controller(s) (default all EU). Positions untouched. → `PAUSED`. |
| `RESUME {market?}` | Wake controller(s). → `ACTIVE` (if not locked/disabled). |
| `RE_ARM` | Only exit from `SYSTEM_LOCKED`: reset daily-loss tracker, **snapshot new baseline equity**, → `ACTIVE`. |
| `STATUS` | Return current state, open trades, daily P&L, baseline equity (read-only). |

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
| `RE_ARM` while not locked | No-op with explicit response (idempotent) |
| Webhook unreachable | Breaker still works internally; operator falls back to OANDA console |
| Process restart | State rebuilt: if daily loss already breached from ledger/account → boot into `SYSTEM_LOCKED` |

## Acceptance criteria

- A correctly-signed `FLATTEN` cancels all resting orders and closes all trades, ending
  in `DISABLED`; an unsigned/replayed request is rejected with no effect.
- Hitting −$150 realized loss transitions to `SYSTEM_LOCKED`; no entry is accepted until a
  signed `RE_ARM`, which snapshots a fresh baseline equity.
- `PAUSE` stops new EU entries while an open trade continues to be managed to its exit.
- State survives restart consistently with account/ledger reality.

## Out of scope

- Sizing/risk math (risk). Order mechanics (executor). Alert formatting (observability).
