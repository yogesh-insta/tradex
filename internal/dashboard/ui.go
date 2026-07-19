package dashboard

// uiHTML is a dense, ops-oriented single page. Auto-refreshes via /api/*.
const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Tradex Ops</title>
<style>
  :root {
    --bg: #0f1419; --panel: #1a2332; --border: #2d3a4d;
    --text: #e7ecf3; --muted: #8b9bb4; --accent: #3d9cf0;
    --green: #3ecf8e; --amber: #e6b84d; --red: #ef6b6b;
    --mono: "IBM Plex Mono", "SF Mono", ui-monospace, monospace;
    --sans: "IBM Plex Sans", "Segoe UI", system-ui, sans-serif;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 16px 20px 40px;
    background: radial-gradient(1200px 600px at 10% -10%, #1a2a40 0%, var(--bg) 55%);
    color: var(--text); font: 14px/1.45 var(--sans);
  }
  header {
    display: flex; flex-wrap: wrap; align-items: baseline; gap: 12px 24px;
    margin-bottom: 18px; border-bottom: 1px solid var(--border); padding-bottom: 12px;
  }
  h1 { margin: 0; font-size: 22px; letter-spacing: 0.02em; }
  h1 span { color: var(--accent); font-weight: 600; }
  .meta { color: var(--muted); font-family: var(--mono); font-size: 12px; }
  .banner {
    background: #3a2a14; border: 1px solid var(--amber); color: #f3d9a0;
    padding: 8px 12px; margin-bottom: 14px; border-radius: 4px; display: none;
  }
  .banner.show { display: block; }
  .grid {
    display: grid; gap: 14px;
    grid-template-columns: 1fr;
  }
  @media (min-width: 1100px) {
    .grid { grid-template-columns: 1.2fr 1fr; }
    .full { grid-column: 1 / -1; }
  }
  section {
    background: var(--panel); border: 1px solid var(--border);
    border-radius: 6px; padding: 12px 14px;
  }
  section h2 {
    margin: 0 0 10px; font-size: 13px; text-transform: uppercase;
    letter-spacing: 0.08em; color: var(--muted); font-weight: 600;
  }
  table { width: 100%; border-collapse: collapse; font-family: var(--mono); font-size: 12px; }
  th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--border); white-space: nowrap; }
  th { color: var(--muted); font-weight: 500; }
  .num { text-align: right; }
  .pos { color: var(--green); }
  .neg { color: var(--red); }
  .pill {
    display: inline-block; min-width: 64px; text-align: center;
    padding: 2px 8px; border-radius: 3px; font-size: 11px; font-family: var(--mono);
  }
  .pill.green { background: #163828; color: var(--green); }
  .pill.amber { background: #3a3014; color: var(--amber); }
  .pill.red { background: #3a1818; color: var(--red); }
  .pill.unknown { background: #243044; color: var(--muted); }
  .empty { color: var(--muted); font-style: italic; padding: 8px 0; }
  .summary { display: flex; flex-wrap: wrap; gap: 14px 22px; margin-bottom: 10px; }
  .summary div { font-family: var(--mono); font-size: 12px; }
  .summary b { display: block; color: var(--muted); font-weight: 500; font-size: 11px; margin-bottom: 2px; }
  .bars { display: flex; align-items: flex-end; gap: 3px; height: 48px; margin: 8px 0 4px; }
  .bars i {
    flex: 1; min-width: 4px; background: var(--accent); opacity: 0.85;
    border-radius: 2px 2px 0 0;
  }
  .bars i.neg { background: var(--red); }
  .err { color: var(--red); font-size: 12px; margin-top: 6px; }
</style>
</head>
<body>
<header>
  <h1><span>Tradex</span> Ops</h1>
  <div class="meta" id="meta">loading…</div>
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
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));
}
function pill(level, text) {
  return '<span class="pill ' + esc(level || "unknown") + '">' + esc(text) + '</span>';
}
async function refresh() {
  const cfg = await api("/api/ui-config");
  const account = (cfg.accounts && cfg.accounts[0]) || "";
  const [ov, cal, daily] = await Promise.all([
    api("/api/overview"),
    api("/api/calendar"),
    account ? api("/api/pl/daily?account=" + encodeURIComponent(account)) : Promise.resolve(null),
  ]);
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

  // Accounts
  let sumHtml = "";
  for (const a of (ov.accounts || [])) {
    sumHtml += '<div class="summary">' +
      '<div><b>Account</b>' + esc(a.name) + '</div>' +
      '<div><b>NAV</b>' + money(a.nav) + '</div>' +
      '<div><b>Unrealized</b><span class="' + cls(a.unrealized_pl) + '">' + money(a.unrealized_pl) + '</span></div>' +
      '<div><b>Realized today*</b><span class="' + cls(a.realized_pl_today) + '">' + money(a.realized_pl_today) + '</span></div>' +
      '<div><b>Margin used / avail</b>' + money(a.margin_used) + ' / ' + money(a.margin_available) + '</div>' +
      '<div><b>State</b>' + esc(a.system_state || "—") + '</div>' +
      (a.error ? '<div class="err">' + esc(a.error) + '</div>' : '') +
      '</div>';
  }
  sumHtml += '<div class="meta">* realized today ≈ OANDA resettablePL when available</div>';
  document.getElementById("account-summaries").innerHTML = sumHtml || '<div class="empty">No accounts</div>';

  const trades = ov.trades || [];
  if (!trades.length) {
    document.getElementById("trades").innerHTML = '<div class="empty">No open trades</div>';
  } else {
    let t = '<table><thead><tr><th>Account</th><th>Instrument</th><th>Dir</th><th class="num">Units</th><th class="num">Entry</th><th class="num">SL</th><th class="num">TP</th><th class="num">uPL</th><th>Open (UTC)</th></tr></thead><tbody>';
    for (const x of trades) {
      t += '<tr><td>' + esc(x.account) + '</td><td>' + esc(x.instrument) + '</td><td>' + esc(x.direction) +
        '</td><td class="num">' + esc(x.units) + '</td><td class="num">' + esc(x.entry) +
        '</td><td class="num">' + (x.stop_loss || "—") + '</td><td class="num">' + (x.take_profit || "—") +
        '</td><td class="num ' + cls(x.unrealized_pl) + '">' + money(x.unrealized_pl) +
        '</td><td>' + esc(x.open_time) + '</td></tr>';
    }
    t += '</tbody></table>';
    document.getElementById("trades").innerHTML = t;
  }

  // Health
  let h = '<table><thead><tr><th>Account</th><th>State</th><th>Stream</th><th>Heartbeat</th><th>Reconcile</th><th>Calendar</th><th>Level</th><th>Source</th></tr></thead><tbody>';
  for (const e of (ov.health || [])) {
    const hb = e.heartbeat_age_ms != null ? (e.heartbeat_age_ms/1000).toFixed(0) + "s" : "—";
    const calA = e.calendar_as_of_age_ms != null ? (e.calendar_as_of_age_ms/1000).toFixed(0) + "s" : "—";
    const rec = e.last_reconcile_ok == null ? "—" : (e.last_reconcile_ok ? "ok" : "fail");
    h += '<tr><td>' + esc(e.account) + '</td><td>' + esc(e.state) + '</td><td>' + esc(e.stream) +
      '</td><td>' + hb + '</td><td>' + rec + '</td><td>' + (e.calendar_fresh ? "fresh " : "stale ") + calA +
      '</td><td>' + pill(e.level, e.level) + '</td><td>' + esc(e.status_source) + '</td></tr>';
    if (e.notes && e.notes.length) {
      h += '<tr><td colspan="8" class="meta">' + esc(e.notes.join(" · ")) + '</td></tr>';
    }
  }
  h += '</tbody></table>';
  document.getElementById("health").innerHTML = h;

  // Calendar
  let c = "";
  if (cal.warning) c += '<div class="banner show">' + esc(cal.warning) + '</div>';
  c += '<div class="meta">as_of ' + esc(cal.as_of || "—") + (cal.fresh ? " · fresh" : " · stale/missing") + '</div>';
  const evs = cal.events || [];
  if (!evs.length) c += '<div class="empty">No upcoming high-impact events</div>';
  else {
    c += '<table><thead><tr><th>Region</th><th>Title</th><th>Impact</th><th>UTC</th><th>Europe/Berlin</th></tr></thead><tbody>';
    for (const e of evs) {
      c += '<tr><td>' + esc(e.region) + '</td><td>' + esc(e.title) + '</td><td>' + esc(e.impact) +
        '</td><td>' + esc(e.time_utc) + '</td><td>' + esc(e.time_berlin) + '</td></tr>';
    }
    c += '</tbody></table>';
  }
  document.getElementById("calendar").innerHTML = c;

  // P&L
  let p = '<div class="meta">Day buckets: DATE(close_time) in ' + esc(cfg.reporting_tz) + '</div>';
  if (!daily) {
    p += '<div class="empty">No account configured for P&amp;L</div>';
  } else if (daily.errors && daily.errors.length) {
    p += '<div class="err">' + esc(daily.errors.join(" · ")) + '</div>';
  } else {
    p += '<div class="summary">' +
      '<div><b>Account</b>' + esc(daily.account) + '</div>' +
      '<div><b>7-day total</b><span class="' + cls(daily.total_7d) + '">' + money(daily.total_7d) + '</span></div>' +
      '<div><b>All-time</b><span class="' + cls(daily.total_all) + '">' + money(daily.total_all) + '</span></div>' +
      '<div><b>All-time trades</b>' + esc(daily.trade_count_all) + '</div>' +
      '</div>';
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
      p += '<table><thead><tr><th>Day</th><th class="num">Realized</th><th class="num">Trades</th><th class="num">W</th><th class="num">L</th></tr></thead><tbody>';
      for (const d of (daily.days || [])) {
        p += '<tr><td>' + esc(d.day) + '</td><td class="num ' + cls(d.realized_pl) + '">' + money(d.realized_pl) +
          '</td><td class="num">' + esc(d.trade_count) + '</td><td class="num">' + esc(d.wins||0) +
          '</td><td class="num">' + esc(d.losses||0) + '</td></tr>';
      }
      p += '</tbody></table>';
    } else {
      p += '<div class="empty">No closed trades in lookback window</div>';
    }
  }
  document.getElementById("pl").innerHTML = p;
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
