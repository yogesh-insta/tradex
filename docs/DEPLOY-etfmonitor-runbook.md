# ASX ETF Monitor — deploy runbook (2026-07-22)

Implemented and committed on `feature/asx-etf-monitor`. Unlike the nserotator
runbook, this one **was** compiled, vetted, tested and run end-to-end against
live Yahoo data locally. What remains is: **verify → deploy → test-fire**.
~15 minutes.

## 0. What was built

- `cmd/etfmonitor` + `internal/etfmonitor/` — monthly advisory job per spec 21
  (Yahoo daily `.AX` data → 3m/6m/12m returns → 200-day trend gate →
  recency-tilted score → top 10 + inverse-vol sizing → exit alerts for held
  funds → Telegram + GCS record).
- Config: `config/config.etfmonitor.cloudrun.yaml` (+ `.dev.yaml`),
  `config/universe-asx-etf.yaml` — 108 funds in five groups
  (68 standard / 8 geared / 5 inverse / 3 fx / 24 excluded cash+bond).
- Seed state: `data/etfmonitor-holdings.json` — **empty**. You fill it in.
- Deploy: `deploy/docker/Dockerfile.etfmonitor`,
  `deploy/scripts/deploy-etfmonitor.sh` (same project, bucket and TELEGRAM_*
  env as nserotator).
- Tests: signal math pinned to `kite/betashares/screen.py` via fixtures
  generated from that module directly; split guard, trend gate, recency tilt,
  weight renormalisation, inverse-vol weighting, exit alerts, report format,
  fetch-failure threshold.

Nothing in `nserotator/` or the OANDA engine was touched — `git status` on the
branch shows only new files.

## 1. Verify (3 min)

```bash
cd ~/mws26/tradex
git checkout feature/asx-etf-monitor
go build ./... && go vet ./... && go test ./...
```

All green as committed.

Local dry run against live data (no Telegram needed — it errors on send, but
the report with the full message text is written first):

```bash
env -u TELEGRAM_BOT_TOKEN -u TELEGRAM_CHAT_ID \
  go run ./cmd/etfmonitor -config config/config.etfmonitor.dev.yaml
python3 -c "import json;print(json.load(open('data/etfmonitor-dev/report-2026-07.json'))['message_text'])"
```

Takes ~60s (85 funds, 8 workers). Expected `exit status 1` with
`telegram: bot token or chat id empty` — the report is still on disk.

## 2. Add your holdings (2 min)

This is the whole point of the job — without it you get a shortlist but no exit
alerts.

```bash
# edit data/etfmonitor-holdings.json before deploying, OR after deploy:
gcloud storage cp gs://tradex-demo-state/etfmonitor/holdings.json /tmp/h.json
# add {"ticker": "...", "qty": N, "avg_price": N.NN} entries + as_of
gcloud storage cp /tmp/h.json gs://tradex-demo-state/etfmonitor/holdings.json
```

The deploy script seeds it only if absent, so it will never clobber your edits.

## 3. Deploy (5 min)

```bash
source .env    # TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID
./deploy/scripts/deploy-etfmonitor.sh
```

Defaults: PROJECT=fxtrade-prod-12345, REGION=us-east1,
BUCKET=tradex-demo-state (override via env). Creates/reuses bucket + Artifact
Registry, seeds `holdings.json` if missing, builds via Cloud Build, deploys
private Cloud Run service `tradex-etfmonitor`, creates Scheduler job
`0 7 1 * *` UTC (monthly, 1st).

## 4. Test-fire once (2 min)

```bash
URL=$(gcloud run services describe tradex-etfmonitor --region=us-east1 --format='value(status.url)')
curl -X POST -H "Authorization: Bearer $(gcloud auth print-identity-token)" "$URL/run?force=1"
```

A REAL Telegram message within ~2 minutes.

## 5. First real run

**1 August, 07:00 UTC** — automatic. Then monthly. Re-read the exit alerts
first; the top 10 is a shortlist, not an instruction.

## Known limitations / honest notes

- **The parameters are not backtested.** 0.5/0.3/0.2 and the 200-day gate are
  reasoned defaults ported from a screener, not optimised values. Spec 21
  §Open questions states this plainly. Treat the output as a shortlist.
- **The trend gate is pro-cyclical** — it keeps you out of bottoms and in after
  moves start. That is the intended trade (drawdown control over entry price).
- **Concentration is unmanaged.** The top 10 can legitimately be eight flavours
  of US tech; inverse-vol weighting does not fix correlation.
- **The ASX products CSV URL is unverified** and may 404. Drift is non-fatal by
  design and falls back to the Betashares page — check the first run's logs to
  see which source actually answered (`source=asx` or `source=betashares`).
- **SEMI is a Global X fund**, not Betashares, and is tagged as such in the
  report. It was in the original screener; it is kept, not silently dropped.
- **holdings.json is the only record of what you own.** The job never reads a
  broker. Keep it honest or the exit alerts are fiction.
- `kite/betashares/screen.py` is the reference implementation but **`kite/` is
  not a git repository.** The checked-in fixtures in
  `internal/etfmonitor/testdata/` are the only versioned record of its
  behaviour.
