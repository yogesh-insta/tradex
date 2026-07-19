# Spec: Executor (single OANDA writer)

## Purpose

The **only** component that writes to OANDA. Places bracketed entries, modifies stops,
closes trades, and cancels resting orders — all idempotently, through a broker-abstracted
interface. Owns the instrument-metadata layer used for sizing/precision.

## Interface

```go
type OrderExecutor interface {
    Open(req OrderRequest) (OpenTrade, error)          // atomic entry + bracket
    ModifyStop(tradeID string, price float64) error    // breakeven / news flatten
    Close(tradeID string) error                         // market close
    CancelOrder(clientOrderID string) error             // resting-order cancel (US future)
    OpenTrades() ([]OpenTrade, error)                    // reconcile source of truth
    Instrument(sym string) (InstrumentMeta, error)       // metadata lookup
}

type InstrumentMeta struct {
    Symbol        string
    DisplayName   string
    PricePrecision int      // decimal places for price
    PipLocation    int      // OANDA pipLocation
    MinUnits       int64
    MarginRate     float64
    PointValue     float64  // account-currency value per point per unit
}
```

## Inputs

- `OrderRequest` from risk (entry). Trade-management calls `ModifyStop`/`Close`.
- OANDA REST: orders, trades, positions, account instruments.
- Config: account id/host, retry policy, timeouts.

## Outputs

- `OpenTrade` on successful entry; errors on failure. Emits **trade events** (opened /
  modified / closed / rejected) to the async publisher (`11-trade-ledger-persistence.md`).

## Behavior

1. **Instrument metadata (boot):** `GET /v3/accounts/{id}/instruments`; cache
   `InstrumentMeta`. Verify the configured symbols exist (resolve `DE30_EUR`/`FR40_EUR` to
   OANDA's actual codes; **fail boot** if a required instrument is missing).
2. **Open (EU = MARKET + bracket):** single `POST /v3/accounts/{id}/orders`:
   - `type: "MARKET"`, `instrument`, signed `units`, `timeInForce: "FOK"`,
     `positionFill: "DEFAULT"`.
   - `stopLossOnFill.price`, `takeProfitOnFill.price` (formatted to `PricePrecision`,
     respecting min-distance rules).
   - `clientExtensions.id = req.ClientOrderID` (idempotency).
   - On fill, read the created trade → return `OpenTrade` with `RiskDistance` captured
     from the initial SL.
3. **Idempotency:** before sending, if a trade/order with the same `ClientOrderID`
   already exists (from reconcile), treat as done (no duplicate). Retries reuse the same
   id so OANDA/we dedupe.
4. **ModifyStop:** `PUT /v3/accounts/{id}/trades/{tradeID}/orders` replacing the stop;
   no-op if the stop is already at the target price (level-triggered — see trade-mgmt).
5. **Close:** `PUT /v3/accounts/{id}/trades/{tradeID}/close` (full close).
6. **CancelOrder / resting orders:** `LIMIT` entry + `DELETE` order — **implemented but
   unused in v1** (US path). Keep behind the interface.
7. **Retries:** idempotent calls retry with backoff on 5xx/network; **never** blind-retry
   a non-idempotent open without the client id. Bounded attempts, then error + alert.
8. **Precision/validation:** format all prices/units per `InstrumentMeta`; pre-validate
   min-distance and min-units to avoid predictable OANDA rejects.

## Config keys

```yaml
executor:
  account_id: "${OANDA_ACCOUNT_ID}"
  host: "api-fxpractice.oanda.com"   # paper for v1
  time_in_force: "FOK"
  request_timeout: 5s
  max_retries: 3
  retry_backoff_base: 250ms
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Order rejected (margin/precision/min-dist) | Surface reason to risk/observability; no retry unless transient |
| Network/5xx on open | Retry with same `ClientOrderID`; if unsure, reconcile via `OpenTrades()` |
| Partial fill | Read actual filled units into `OpenTrade`; risk/mgmt use real size |
| Missing instrument at boot | Fail startup (don't trade blind) |
| Modify on a vanished trade | Treat as closed (reconcile drops it); no error escalation |

## Acceptance criteria

- An `Open` produces exactly one OANDA trade with linked SL/TP; replaying the same
  `OrderRequest` (same id) creates **no** second trade.
- Prices sent are rounded to `PricePrecision` and pass OANDA min-distance validation.
- `OpenTrades()` returns the live set used by risk and trade-management for reconciliation.
- Switching `host` between `fxpractice`/`fxtrade` requires **no code change**.

## Out of scope

- Deciding to trade (strategy/risk). Timer-driven exit logic (trade-management).
