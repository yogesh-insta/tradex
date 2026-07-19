# Economic calendar (ops)

## Purpose

Keep a **durable high-impact** event file covering **EU equity index** and **US/JP FX**
in one state object. The VM fail-safes if this state is missing, unparseable, or stale —
so freshness matters.

**Primary path:** `cmd/calendarpoller` — Finnhub primary → Gemini Live Search
fallback (`gemini-2.5-flash-lite`) → Telegram review message → write durable
`calendar-state.json` / GCS. Normative behaviour:
[`docs/specs/10-economic-calendar.md`](../docs/specs/10-economic-calendar.md).

```bash
go run ./cmd/calendarpoller -config config/config.dev.yaml
```

This supersedes the earlier interim **manual-only** weekly paste as the main workflow.
Manual Google AI web paste remains an **optional backup** if both Finnhub and Gemini fail.

Traders filter by region at runtime:

| Region | Used by |
| --- | --- |
| `EU` | EU LOVE (equity index) |
| `US`, `JP` | FX USD/JPY |

## File

| Item | Path |
| --- | --- |
| State | [`calendar-state.json`](./calendar-state.json) |

Schema matches the economic calendar spec: `as_of` (UTC ISO-8601) + `events[]` with
`region`, `title`, `impact`, `time`.

### Combined schema notes

- **One root object** — never two JSON roots in the file.
- **Merge all events** into one `events` array (EU + US + JP together).
- **One** `as_of` for the whole object (UTC now when written).
- Every `impact` is exactly `"high"`; every `time` is UTC with a `Z` suffix.
- Validate before relying on a hand edit:

```bash
python -m json.tool data/calendar-state.json
```

### Overwrite rules (critical)

- **Overwrite the whole file** with one JSON object.
- **Never paste two root JSON objects** into the file (invalid JSON; VM fail-safe).
- After each refresh (poller or manual), `as_of` must be **now in UTC** so the object
  is not born stale.

### `as_of` freshness

- Config `staleness_max` is ~**90m**.
- If `as_of` is older than that, the VM treats **"event imminent"** (blocks entries /
  BE management).

## Ops ritual (normal)

1. Poller runs on schedule (or you trigger it).
2. Check **Telegram** for the review message:
   - Header: `Finnhub Primary` or `Gemini Fallback Pipeline`
   - Body: fenced `json` with the candidate calendar
3. Skim on phone: regions `EU`/`US`/`JP` only, high-impact event types, UTC `Z` times.
4. **Default:** poller already auto-wrote durable state after a successful Telegram send
   — no file paste required.
5. **If wrong:** override [`calendar-state.json`](./calendar-state.json) (or the GCS
   object) with a corrected single JSON object, set `as_of` to now UTC, validate with
   `python -m json.tool`, redeploy/sync if needed.

## Optional backup (both pipelines failed)

Use only when Telegram shows failures / no refresh and durable state is going stale.

1. Open Google AI (or equivalent) with **web search** enabled.
2. Paste the combined prompt below; replace `<PASTE TODAY'S DATE…>` with today’s local date.
3. Copy the returned JSON into `data/calendar-state.json` — **replace the entire file**
   with that single object (do not append a second `{...}`).
4. Confirm every `time` is UTC with a `Z` suffix; skim the post-JSON checklist for
   uncertain timings.
5. Set `as_of` to current UTC if the model’s stamp is wrong or old.
6. Validate: `python -m json.tool data/calendar-state.json`
7. Deploy / sync the object so the VM (or GCS path) sees the new file before the next
   session.

## Copy-paste prompt (Google AI / websearch) — EU + US + JP

```text
You are helping me build a trading economic calendar for EU equity-index and USD/JPY news blackouts.

Task: Using current web search, list HIGH-IMPACT macroeconomic events for the next 7 days that can move either euro-area equity indices or USD/JPY.

Include only:

European Union / euro area (region code: EU) — high-impact only:
- ECB rate decision / monetary policy decision and press conference
- Eurozone CPI / HICP (flash or final when marked high impact)
- German CPI (flash or final when marked high impact)
- Major DE / EU GDP releases when marked high impact
- Other euro-area prints only if widely marked high impact for EU equities

United States (region code: US):
- NFP, CPI, PPI, PCE
- FOMC rate decision / press conference
- GDP (advance)
- Unemployment claims only if widely marked high impact

Japan (region code: JP):
- BOJ rate decision / MPM
- Japan CPI
- Tankan
- Major employment / GDP if marked high impact

Exclude: medium/low impact, speeches unless ECB President / Fed Chair / BOJ Governor scheduled as major market events, unrelated countries.

Output rules:
1. Times MUST be in UTC as ISO-8601 with Z suffix (e.g. 2026-07-22T18:00:00Z). Convert carefully from local release times.
2. If time is uncertain, say so in a note AFTER the JSON, but still give your best UTC estimate.
3. impact must be exactly "high" for every included event.
4. Return ONLY ONE JSON object (a single root). Merge EU, US, and JP into one events array. Never output two separate JSON objects.
5. Schema (no markdown fences before/after if possible):

{
  "as_of": "<today UTC now>",
  "events": [
    {
      "region": "EU" | "US" | "JP",
      "title": "short event name",
      "impact": "high",
      "time": "YYYY-MM-DDTHH:MM:SSZ"
    }
  ]
}

Today's date (my local): <PASTE TODAY'S DATE, e.g. 2026-07-19>
My timezone for reference: Australia/Sydney (AEST/AEDT). Still output event times in UTC only.

Also add after the JSON a short checklist:
- source sites you used
- any events with uncertain timing
```
