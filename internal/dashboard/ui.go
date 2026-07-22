package dashboard

// uiHTML is a dense, ops-oriented single page. Auto-refreshes via /api/*.
const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover"/>
<title>Tradex Ops</title>
<style>
  :root {
    --bg: #0f1419; --panel: #1a2332; --panel2: #152033; --border: #2d3a4d;
    --text: #e7ecf3; --muted: #8b9bb4; --accent: #3d9cf0;
    --green: #3ecf8e; --amber: #e6b84d; --red: #ef6b6b;
    --mono: "IBM Plex Mono", "SF Mono", ui-monospace, monospace;
    --sans: "IBM Plex Sans", "Segoe UI", system-ui, sans-serif;
    --pad: 14px;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 12px var(--pad) 48px;
    background:
      radial-gradient(900px 420px at 8% -8%, #1a2a40 0%, transparent 55%),
      radial-gradient(700px 380px at 100% 0%, #182438 0%, var(--bg) 50%);
    color: var(--text); font: 14px/1.45 var(--sans);
    -webkit-text-size-adjust: 100%;
  }
  header {
    display: flex; flex-wrap: wrap; align-items: center; gap: 10px 16px;
    margin-bottom: 14px; border-bottom: 1px solid var(--border); padding-bottom: 12px;
  }
  h1 { margin: 0; font-size: 1.35rem; letter-spacing: 0.02em; line-height: 1.2; }
  h1 span { color: var(--accent); font-weight: 600; }
  .meta { color: var(--muted); font-family: var(--mono); font-size: 11px; line-height: 1.4; }
  .header-meta { flex: 1 1 12rem; min-width: 0; word-break: break-word; }
  .health-dot {
    width: 10px; height: 10px; border-radius: 50%; background: var(--muted);
    box-shadow: 0 0 0 3px #243044; flex: 0 0 auto;
  }
  .health-dot.green { background: var(--green); box-shadow: 0 0 0 3px #163828; }
  .health-dot.amber { background: var(--amber); box-shadow: 0 0 0 3px #3a3014; }
  .health-dot.red { background: var(--red); box-shadow: 0 0 0 3px #3a1818; }
  .banner {
    background: #3a2a14; border: 1px solid var(--amber); color: #f3d9a0;
    padding: 10px 12px; margin-bottom: 14px; border-radius: 6px; display: none;
    font-size: 13px; line-height: 1.4;
  }
  .banner.show { display: block; }
  .grid { display: grid; gap: 12px; grid-template-columns: 1fr; }
  @media (min-width: 960px) {
    body { padding: 16px 20px 40px; }
    .grid { grid-template-columns: 1.15fr 1fr; gap: 14px; }
    .full { grid-column: 1 / -1; }
  }
  section {
    background: linear-gradient(180deg, var(--panel) 0%, var(--panel2) 100%);
    border: 1px solid var(--border); border-radius: 8px; padding: 12px 12px 10px;
    min-width: 0;
  }
  section h2 {
    margin: 0 0 10px; font-size: 12px; text-transform: uppercase;
    letter-spacing: 0.08em; color: var(--muted); font-weight: 600;
  }
  .scroll { overflow-x: auto; -webkit-overflow-scrolling: touch; margin: 0 -4px; padding: 0 4px; }
  table { width: 100%; border-collapse: collapse; font-family: var(--mono); font-size: 12px; }
  th, td { text-align: left; padding: 7px 8px; border-bottom: 1px solid var(--border); white-space: nowrap; }
  th { color: var(--muted); font-weight: 500; }
  .num { text-align: right; font-variant-numeric: tabular-nums; }
  .pos { color: var(--green); }
  .neg { color: var(--red); }
  .pill {
    display: inline-block; min-width: 56px; text-align: center;
    padding: 2px 8px; border-radius: 999px; font-size: 11px; font-family: var(--mono);
  }
  .pill.green { background: #163828; color: var(--green); }
  .pill.amber { background: #3a3014; color: var(--amber); }
  .pill.red { background: #3a1818; color: var(--red); }
  .pill.unknown { background: #243044; color: var(--muted); }
  .empty { color: var(--muted); font-style: italic; padding: 8px 0; }
  .account-grid {
    display: grid; gap: 10px;
    grid-template-columns: 1fr;
    margin-bottom: 12px;
  }
  @media (min-width: 640px) {
    .account-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  .card {
    background: rgba(15, 20, 25, 0.45); border: 1px solid var(--border);
    border-radius: 8px; padding: 10px 12px;
  }
  .card .title {
    display: flex; align-items: center; justify-content: space-between; gap: 8px;
    margin-bottom: 8px; font-family: var(--mono); font-size: 13px; font-weight: 600;
  }
  .kv {
    display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 12px;
  }
  .kv div { font-family: var(--mono); font-size: 12px; min-width: 0; }
  .kv b {
    display: block; color: var(--muted); font-weight: 500; font-size: 10px;
    text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 2px;
  }
  .cards { display: grid; gap: 8px; }
  .row-card .top {
    display: flex; justify-content: space-between; align-items: baseline; gap: 8px;
    margin-bottom: 6px; font-family: var(--mono); font-size: 12px;
  }
  .row-card .top .inst { font-weight: 600; }
  .row-card .top .acct { color: var(--muted); }
  .row-card .stats {
    display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 10px;
    font-family: var(--mono); font-size: 11px;
  }
  .row-card .stats span { color: var(--muted); }
  .desktop-only { display: none; }
  .mobile-only { display: block; }
  @media (min-width: 760px) {
    .desktop-only { display: block; }
    .mobile-only { display: none; }
  }
  .bars { display: flex; align-items: flex-end; gap: 3px; height: 48px; margin: 8px 0 4px; }
  .bars i {
    flex: 1; min-width: 4px; background: var(--accent); opacity: 0.85;
    border-radius: 2px 2px 0 0;
  }
  .bars i.neg { background: var(--red); }
  .err { color: var(--red); font-size: 12px; margin-top: 6px; }
  .footnote { color: var(--muted); font-family: var(--mono); font-size: 11px; margin-top: 6px; }
</style>
</head>
<body>
<header>
  <div class="health-dot" id="health-dot" title="overall health"></div>
  <h1><span>Tradex</span> Ops</h1>
  <div class="meta header-meta" id="meta">loading…</div>
</header>
<div class="banner" id="banner"></div>
<div class="grid">
  <section class="full" id="sec-accounts">
    <h2>1 · Open trades &amp; account</h2>
    <div id="account-summaries"></div>
    <div id="trades"></div>
  </section>
  <section id="sec-health">
    <h2>2 · Engine / bot health</h2>
    <div id="health"></div>
  </section>
  <section id="sec-cal">
    <h2>3 · Economic calendar</h2>
    <div id="calendar"></div>
  </section>
  <section class="full" id="sec-pl">
    <h2>4 · P&amp;L summary</h2>
    <div id="pl"></div>
  </section>
  <section class="full" id="sec-etf">
    <h2>5 · ASX ETF monitor</h2>
    <div id="etf"></div>
  </section>
  <section class="full" id="sec-nse">
    <h2>6 · NSE momentum rotator</h2>
    <div id="nse"></div>
  </section>
</div>
<script>
const token = new URLSearchParams(location.search).get("token") || "";
const authHeaders = () => {
  const h = {"Accept":"application/json"};
  if (token) h["Authorization"] = "Bearer " + token;
  return h;
};
async function api(path) {
  const r = await fetch(path, {headers: authHeaders(), credentials: "same-origin"});
  if (r.status === 401 || r.status === 403) throw new Error("auth " + r.status);
  if (!r.ok) throw new Error(path + " " + r.status);
  return r.json();
}
function money(n) {
  if (n == null || Number.isNaN(n)) return "—";
  const s = Number(n).toFixed(2);
  return (n > 0 ? "+" : "") + s;
}
function cls(n) { return n > 0 ? "pos" : n < 0 ? "neg" : ""; }
// etfmonitor stores trailing returns as {Value, OK} objects (Go Ret type).
function etfRet(r) {
  if (!r) return "—";
  const ok = r.OK ?? r.ok;
  const val = r.Value ?? r.value;
  if (!ok || val == null) return "n/a";
  const pct = Math.round(val * 100);
  return (pct > 0 ? "+" : "") + pct + "%";
}
function etfPct(v) {
  if (v == null || v === "") return "—";
  return Math.round(Number(v) * 100) + "%";
}
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));
}
function pill(level, text) {
  return '<span class="pill ' + esc(level || "unknown") + '">' + esc(text) + '</span>';
}
function worstLevel(levels) {
  const rank = { red: 3, amber: 2, unknown: 1, green: 0 };
  let worst = "unknown", score = -1;
  for (const l of levels) {
    const s = rank[l] ?? 1;
    if (s > score) { score = s; worst = l; }
  }
  return worst;
}
function setDot(level) {
  const el = document.getElementById("health-dot");
  el.className = "health-dot " + (level || "unknown");
}
async function refresh() {
  const cfg = await api("/api/ui-config");
  const accounts = cfg.accounts || [];
  const promises = [
    api("/api/overview"),
    api("/api/calendar"),
    ...accounts.map(account => api("/api/pl/daily?account=" + encodeURIComponent(account))),
    api("/api/etf").catch(() => ({error: "not available"})),
    api("/api/nse").catch(() => ({error: "not available"})),
  ];
  const [ov, cal, ...rest] = await Promise.all(promises);
  const etf = rest[rest.length - 2];
  const nse = rest[rest.length - 1];
  const dailyByAccount = rest.slice(0, -2);
  document.getElementById("meta").textContent =
    "tz=" + cfg.reporting_tz +
    " · refresh=" + (cfg.refresh_interval_ms/1000) + "s" +
    (cfg.mock ? " · MOCK" : "") +
    " · as_of " + (ov.as_of || "");

  const banners = [];
  if (cal.warning) banners.push(cal.warning);
  if (ov.errors && ov.errors.length) banners.push(ov.errors.join(" · "));
  const b = document.getElementById("banner");
  if (banners.length) { b.textContent = banners.join(" | "); b.classList.add("show"); }
  else { b.classList.remove("show"); b.textContent = ""; }

  setDot(worstLevel((ov.health || []).map(e => e.level)));

  // Accounts
  let sumHtml = '<div class="account-grid">';
  for (const a of (ov.accounts || [])) {
    sumHtml += '<div class="card">' +
      '<div class="title"><span>' + esc(a.name) + '</span><span>' + esc(a.system_state || "—") + '</span></div>' +
      '<div class="kv">' +
      '<div><b>NAV</b>' + money(a.nav) + '</div>' +
      '<div><b>Unrealized</b><span class="' + cls(a.unrealized_pl) + '">' + money(a.unrealized_pl) + '</span></div>' +
      '<div><b>Realized today*</b><span class="' + cls(a.realized_pl_today) + '">' + money(a.realized_pl_today) + '</span></div>' +
      '<div><b>Margin used / avail</b>' + money(a.margin_used) + ' / ' + money(a.margin_available) + '</div>' +
      '</div>' +
      (a.error ? '<div class="err">' + esc(a.error) + '</div>' : '') +
      '</div>';
  }
  sumHtml += '</div><div class="footnote">* realized today ≈ OANDA resettablePL when available</div>';
  document.getElementById("account-summaries").innerHTML = sumHtml || '<div class="empty">No accounts</div>';

  const trades = ov.trades || [];
  if (!trades.length) {
    document.getElementById("trades").innerHTML = '<div class="empty">No open trades</div>';
  } else {
    let mobile = '<div class="cards mobile-only">';
    for (const x of trades) {
      mobile += '<div class="card row-card">' +
        '<div class="top"><span class="inst">' + esc(x.instrument) + ' · ' + esc(x.direction) +
        '</span><span class="' + cls(x.unrealized_pl) + '">' + money(x.unrealized_pl) + '</span></div>' +
        '<div class="top"><span class="acct">' + esc(x.account) + '</span><span class="acct">' + esc(x.open_time) + '</span></div>' +
        '<div class="stats">' +
        '<div><span>Units</span> ' + esc(x.units) + '</div>' +
        '<div><span>Entry</span> ' + esc(x.entry) + '</div>' +
        '<div><span>SL</span> ' + (x.stop_loss || "—") + '</div>' +
        '<div><span>TP</span> ' + (x.take_profit || "—") + '</div>' +
        '</div></div>';
    }
    mobile += '</div>';
    let t = '<div class="scroll desktop-only"><table><thead><tr><th>Account</th><th>Instrument</th><th>Dir</th><th class="num">Units</th><th class="num">Entry</th><th class="num">SL</th><th class="num">TP</th><th class="num">uPL</th><th>Open (UTC)</th></tr></thead><tbody>';
    for (const x of trades) {
      t += '<tr><td>' + esc(x.account) + '</td><td>' + esc(x.instrument) + '</td><td>' + esc(x.direction) +
        '</td><td class="num">' + esc(x.units) + '</td><td class="num">' + esc(x.entry) +
        '</td><td class="num">' + (x.stop_loss || "—") + '</td><td class="num">' + (x.take_profit || "—") +
        '</td><td class="num ' + cls(x.unrealized_pl) + '">' + money(x.unrealized_pl) +
        '</td><td>' + esc(x.open_time) + '</td></tr>';
    }
    t += '</tbody></table></div>';
    document.getElementById("trades").innerHTML = mobile + t;
  }

  // Health
  let hm = '<div class="cards mobile-only">';
  let h = '<div class="scroll desktop-only"><table><thead><tr><th>Account</th><th>State</th><th>Stream</th><th>Heartbeat</th><th>Reconcile</th><th>Calendar</th><th>Level</th><th>Source</th></tr></thead><tbody>';
  for (const e of (ov.health || [])) {
    const hb = e.heartbeat_age_ms != null ? (e.heartbeat_age_ms/1000).toFixed(0) + "s" : "—";
    const calA = e.calendar_as_of_age_ms != null ? (e.calendar_as_of_age_ms/1000).toFixed(0) + "s" : "—";
    const rec = e.last_reconcile_ok == null ? "—" : (e.last_reconcile_ok ? "ok" : "fail");
    const calTxt = (e.calendar_fresh ? "fresh " : "stale ") + calA;
    hm += '<div class="card row-card">' +
      '<div class="top"><span class="inst">' + esc(e.account) + '</span>' + pill(e.level, e.level) + '</div>' +
      '<div class="stats">' +
      '<div><span>State</span> ' + esc(e.state) + '</div>' +
      '<div><span>Stream</span> ' + esc(e.stream) + '</div>' +
      '<div><span>Heartbeat</span> ' + hb + '</div>' +
      '<div><span>Reconcile</span> ' + rec + '</div>' +
      '<div><span>Calendar</span> ' + calTxt + '</div>' +
      '<div><span>Source</span> ' + esc(e.status_source) + '</div>' +
      '</div>' +
      ((e.notes && e.notes.length) ? '<div class="footnote">' + esc(e.notes.join(" · ")) + '</div>' : '') +
      '</div>';
    h += '<tr><td>' + esc(e.account) + '</td><td>' + esc(e.state) + '</td><td>' + esc(e.stream) +
      '</td><td>' + hb + '</td><td>' + rec + '</td><td>' + calTxt +
      '</td><td>' + pill(e.level, e.level) + '</td><td>' + esc(e.status_source) + '</td></tr>';
    if (e.notes && e.notes.length) {
      h += '<tr><td colspan="8" class="meta">' + esc(e.notes.join(" · ")) + '</td></tr>';
    }
  }
  hm += '</div>';
  h += '</tbody></table></div>';
  document.getElementById("health").innerHTML = hm + h;

  // Calendar
  let c = "";
  if (cal.warning) c += '<div class="banner show">' + esc(cal.warning) + '</div>';
  c += '<div class="footnote">as_of ' + esc(cal.as_of || "—") + (cal.fresh ? " · fresh" : " · stale/missing") + '</div>';
  const evs = cal.events || [];
  if (!evs.length) c += '<div class="empty">No upcoming high-impact events</div>';
  else {
    let cm = '<div class="cards mobile-only">';
    for (const e of evs) {
      cm += '<div class="card row-card">' +
        '<div class="top"><span class="inst">' + esc(e.title) + '</span><span class="acct">' + esc(e.impact) + '</span></div>' +
        '<div class="stats">' +
        '<div><span>Region</span> ' + esc(e.region) + '</div>' +
        '<div><span>UTC</span> ' + esc(e.time_utc) + '</div>' +
        '<div><span>Berlin</span> ' + esc(e.time_berlin) + '</div>' +
        '</div></div>';
    }
    cm += '</div>';
    c += cm + '<div class="scroll desktop-only"><table><thead><tr><th>Region</th><th>Title</th><th>Impact</th><th>UTC</th><th>Europe/Berlin</th></tr></thead><tbody>';
    for (const e of evs) {
      c += '<tr><td>' + esc(e.region) + '</td><td>' + esc(e.title) + '</td><td>' + esc(e.impact) +
        '</td><td>' + esc(e.time_utc) + '</td><td>' + esc(e.time_berlin) + '</td></tr>';
    }
    c += '</tbody></table></div>';
  }
  document.getElementById("calendar").innerHTML = c;

  // P&L
  let p = '<div class="footnote">Day buckets: DATE(close_time) in ' + esc(cfg.reporting_tz) + '</div>';
  if (!dailyByAccount.length) {
    p += '<div class="empty">No account configured for P&amp;L</div>';
  } else {
    for (const daily of dailyByAccount) {
      p += '<div class="card" style="margin-top:10px">' +
        '<div class="title"><span>' + esc(daily.account) + '</span></div>' +
        '<div class="kv">' +
        '<div><b>7-day total</b><span class="' + cls(daily.total_7d) + '">' + money(daily.total_7d) + '</span></div>' +
        '<div><b>All-time</b><span class="' + cls(daily.total_all) + '">' + money(daily.total_all) + '</span></div>' +
        '<div><b>Close fills</b>' + esc(daily.trade_count_all) + '</div>' +
        '</div></div>';
      if (daily.errors && daily.errors.length) {
        p += '<div class="err">' + esc(daily.errors.join(" · ")) + '</div>';
        continue;
      }
      const days = (daily.days || []).slice().reverse(); // oldest → newest for bars
      if (days.length) {
        const max = Math.max(...days.map(d => Math.abs(d.realized_pl)), 1);
        p += '<div class="bars">';
        for (const d of days) {
          const hgt = Math.max(2, Math.round(40 * Math.abs(d.realized_pl) / max));
          p += '<i class="' + (d.realized_pl < 0 ? "neg" : "") + '" style="height:' + hgt + 'px" title="' +
            esc(d.day) + ': ' + money(d.realized_pl) + '"></i>';
        }
        p += '</div>';
        p += '<div class="scroll"><table><thead><tr><th>Day</th><th class="num">Realized</th><th class="num">Close fills</th><th class="num">W</th><th class="num">L</th></tr></thead><tbody>';
        for (const d of (daily.days || [])) {
          p += '<tr><td>' + esc(d.day) + '</td><td class="num ' + cls(d.realized_pl) + '">' + money(d.realized_pl) +
            '</td><td class="num">' + esc(d.trade_count) + '</td><td class="num">' + esc(d.wins||0) +
            '</td><td class="num">' + esc(d.losses||0) + '</td></tr>';
        }
        p += '</tbody></table></div>';
      } else {
        p += '<div class="empty">No closed trades in lookback window</div>';
      }
    }
  }
  document.getElementById("pl").innerHTML = p;

  // ETF Monitor
  let etfHtml = '';
  if (etf && etf.error) {
    etfHtml = '<div class="empty">' + esc(etf.error) + '</div>';
  } else if (etf) {
    let html = '<div class="etf-container">';
    if (etf.exit_alerts && etf.exit_alerts.length) {
      html += '<div class="etf-section">';
      html += '<h3>Exit Alerts</h3>';
      html += '<div class="scroll"><table><thead><tr><th>Ticker</th><th>Name</th><th class="num">Qty</th><th>Reason</th></tr></thead><tbody>';
      for (const a of etf.exit_alerts) {
        html += '<tr class="alert"><td><strong>' + esc(a.ticker) + '</strong></td><td>' + esc(a.name) +
          '</td><td class="num">' + (a.qty || '—') + '</td><td>' + esc(a.reason) + '</td></tr>';
      }
      html += '</tbody></table></div></div>';
    }
    if (etf.top && etf.top.length) {
      html += '<div class="etf-section">';
      html += '<h3>Top 10 Standard Momentum</h3>';
      html += '<div class="scroll"><table><thead><tr><th>Rank</th><th>Ticker</th><th>Name</th><th class="num">Score</th><th class="num">3m</th><th class="num">6m</th><th class="num">12m</th><th class="num">Vol</th></tr></thead><tbody>';
      for (let i = 0; i < etf.top.length; i++) {
        const t = etf.top[i];
        html += '<tr><td>' + (i+1) + '</td><td><strong>' + esc(t.ticker) + '</strong></td><td>' + esc(t.name) +
          '</td><td class="num ' + cls(t.score) + '">' + (t.score || '—').toFixed(2) +
          '</td><td class="num">' + etfRet(t.ret_3m) + '</td><td class="num">' + etfRet(t.ret_6m) + '</td>' +
          '<td class="num">' + etfRet(t.ret_12m) + '</td><td class="num">' + etfPct(t.vol) + '</td></tr>';
      }
      html += '</tbody></table></div></div>';
    }
    if (etf.geared_fx && etf.geared_fx.length) {
      html += '<div class="etf-section">';
      html += '<h3>Geared &amp; FX</h3>';
      html += '<div class="scroll"><table><thead><tr><th>Ticker</th><th>Name</th><th class="num">Score</th><th class="num">Vol</th></tr></thead><tbody>';
      for (const t of etf.geared_fx) {
        html += '<tr><td><strong>' + esc(t.ticker) + '</strong></td><td>' + esc(t.name) +
          '</td><td class="num">' + (t.score || '—').toFixed(2) + '</td><td class="num">' + etfPct(t.vol) + '</td></tr>';
      }
      html += '</tbody></table></div></div>';
    }
    if (etf.warnings && etf.warnings.length) {
      html += '<div class="etf-section warnings">';
      for (const w of etf.warnings) {
        html += '<div class="empty">⚠ ' + esc(w) + '</div>';
      }
      html += '</div>';
    }
    html += '<div class="meta">as of ' + esc(etf.run_at || etf.month || '—') + ' · <a href="https://www.betashares.com.au/fund/" target="_blank" style="color:var(--accent)">Betashares</a></div>';
    html += '</div>';
    etfHtml = html;
  }
  document.getElementById("etf").innerHTML = etfHtml || '<div class="empty">Loading…</div>';

  // NSE Rotator
  let nseHtml = '';
  if (nse && nse.error) {
    nseHtml = '<div class="empty">' + esc(nse.error) + '</div>';
  } else if (nse) {
    let html = '<div class="nse-container">';
    if (nse.top && nse.top.length) {
      html += '<div class="nse-section">';
      html += '<h3>Top ' + nse.top.length + ' NSE Stocks (6m momentum)</h3>';
      html += '<div class="scroll"><table><thead><tr><th>Rank</th><th>Symbol</th><th>Name</th><th class="num">Score</th><th class="num">Return</th><th class="num">Vol</th><th class="num">Sharpe</th></tr></thead><tbody>';
      for (let i = 0; i < nse.top.length; i++) {
        const t = nse.top[i];
        html += '<tr><td>' + (i+1) + '</td><td><strong>' + esc(t.symbol) + '</strong></td><td>' + esc(t.name || '') +
          '</td><td class="num">' + (t.score || '—').toFixed(2) +
          '</td><td class="num ' + cls(t.return_pct) + '">' + (t.return_pct || '—') + '%</td>' +
          '<td class="num">' + (t.volatility || '—') + '%</td><td class="num">' + (t.sharpe || '—').toFixed(2) + '</td></tr>';
      }
      html += '</tbody></table></div></div>';
    }
    if (nse.warnings && nse.warnings.length) {
      html += '<div class="nse-section warnings">';
      for (const w of nse.warnings) {
        html += '<div class="empty">⚠ ' + esc(w) + '</div>';
      }
      html += '</div>';
    }
    html += '<div class="meta">as of ' + esc(nse.run_at || nse.month || '—') + '</div>';
    html += '</div>';
    nseHtml = html;
  }
  document.getElementById("nse").innerHTML = nseHtml || '<div class="empty">Loading…</div>';
  return cfg.refresh_interval_ms || 20000;
}
async function loop() {
  try {
    const ms = await refresh();
    setTimeout(loop, ms);
  } catch (e) {
    document.getElementById("banner").textContent = String(e);
    document.getElementById("banner").classList.add("show");
    setTimeout(loop, 5000);
  }
}
loop();
</script>
</body>
</html>
`
