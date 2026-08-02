# NSE Rotator — morning runbook (2026-07-21)

Everything is implemented and committed on `feature/nse-momentum-rotator`.
I could not compile or deploy from my sandbox (no Go toolchain, no gcloud
auth), so the morning path is: **verify → deploy → test-fire**. ~15 minutes.

> **Superseded 2026-08.** The strategy is now top 10 with 6m/12m exit
> hysteresis (`exit_rank_n: 30`) and the **regime filter turned off**, so the
> "CASH regime" behaviour described below no longer happens. This runbook is
> kept as a point-in-time record of the first deploy; for current behaviour
> read `docs/specs/20-nse-momentum-rotator.md` § THE STRATEGY IN FULL.

## 0. What was built overnight

- `cmd/nserotator` + `internal/nserotator/` — monthly advisory job per spec 20
  (Yahoo daily data → 6m momentum on Nifty 200 → top 8 equal-weight →
  regime filter → diff vs your portfolio → Telegram + GCS record).
- Config: `config/config.nserotator.cloudrun.yaml` (+ `.dev.yaml`),
  `config/universe-nse200.yaml`, `config/holidays-nse.yaml` (2026, lunar
  dates marked ~ESTIMATE — validate against the official NSE list).
- Seed state: `data/nserotator-portfolio.json` — an example portfolio. The live book stays in Cloud Storage, not in git.
- Deploy: `deploy/docker/Dockerfile.nserotator`,
  `deploy/scripts/deploy-nserotator.sh` (same pattern as calendarpoller;
  same project, same bucket, reuses your TELEGRAM_* env).
- Tests: signal math pinned to the Python backtest (EMA vs pandas
  `adjust=False`, momentum vs `pct_change(6)` default), diff/rotation cases,
  last-trading-day gate with holiday fixtures, INR formatting.

Verified end-to-end in Python emulation against the real downloaded data:
regime currently reads **CASH** (Nifty 24,238 < EMA200 24,396 — matches your
Kite screenshot exactly).

## 1. Verify (5 min)

```bash
cd ~/mws26/tradex
git checkout feature/nse-momentum-rotator
go build ./... && go test ./internal/nserotator/...
go vet ./cmd/nserotator/... ./internal/nserotator/...
```

If anything fails to compile, tell Claude the error — the code was written
without a compiler available.

Optional local dry run (no Telegram needed — it will error on send but you
can inspect `data/nserotator/recommendation-*.json` which contains the full
message text; copy `data/nserotator-portfolio.json` to
`data/nserotator/portfolio.json` first):

```bash
mkdir -p data/nserotator
cp data/nserotator-portfolio.json data/nserotator/portfolio.json
go run ./cmd/nserotator --config config/config.nserotator.dev.yaml --force
```

## 2. Deploy (5 min)

```bash
source .env    # for TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID
./deploy/scripts/deploy-nserotator.sh
```

Defaults: PROJECT=fxtrade-prod-12345, REGION=us-east1,
BUCKET=tradex-demo-state (override via env). The script:
creates/reuses bucket + Artifact Registry, seeds
`gs://tradex-demo-state/nserotator/portfolio.json` (only if missing),
builds via Cloud Build, deploys Cloud Run service `tradex-nserotator`
(private), creates Scheduler job weekdays 12:30 UTC = 18:00 IST.

## 3. Test-fire once (2 min)

```bash
URL=$(gcloud run services describe tradex-nserotator --region=us-east1 --format='value(status.url)')
curl -X POST -H "Authorization: Bearer $(gcloud auth print-identity-token)" "$URL/run?force=1"
```

You should get a REAL Telegram message within ~2 minutes. Expected content
right now: regime CASH, SELL all 14 holdings, no buys (that's the preview
result — market is below its 200-day EMA). **It's advisory. You decide what
to actually execute, and tax on the big winners is your call.**

## 4. First real run

Friday **July 31, 18:00 IST** — automatic. Execute (or skip) orders Monday
Aug 3 at open, then update the portfolio file:

```bash
gcloud storage cp gs://tradex-demo-state/nserotator/portfolio.json /tmp/p.json
# edit /tmp/p.json: holdings you actually have now + as_of
gcloud storage cp /tmp/p.json gs://tradex-demo-state/nserotator/portfolio.json
```

## 5. Optional hardening (later)

- Cloud Monitoring alert on Cloud Run 5xx (covers Telegram-down case).
- Validate `config/holidays-nse.yaml` lunar dates against the official NSE
  trading-holiday page.
- Merge to main once you're happy with a paper month.

## Known limitations / honest notes

- Compile is unverified (sandbox had no Go). Tests exist; run them first.
- Expectation setting lives in spec 20: backtest CAGR is survivorship-
  inflated; realistic is mid-teens with −20…−30% drawdowns. First months in
  CASH regime will feel like "it does nothing" — that's the drawdown
  protection working.
- portfolio.json is the single source of truth about what you hold. The
  system never reads your broker account. Keep it honest or the diffs drift.
