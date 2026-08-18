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
  nav.lanes { display: flex; gap: 6px; flex: 0 0 auto; }
  nav.lanes a {
    color: var(--muted); text-decoration: none; font-family: var(--mono);
    font-size: 12px; padding: 5px 11px; border: 1px solid var(--border);
    border-radius: 999px; white-space: nowrap; line-height: 1.2;
  }
  nav.lanes a:hover { color: var(--text); border-color: var(--muted); }
  nav.lanes a.on {
    color: var(--bg); background: var(--accent); border-color: var(--accent);
    font-weight: 600;
  }
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
  /* NSE rotator ranking bands: buy list, hold-only buffer, and the rules
     marking each threshold. Left border keeps the zones readable while
     scrolling horizontally on mobile. */
  tr.nse-buy td:first-child { border-left: 3px solid var(--green); }
  tr.nse-keep td:first-child { border-left: 3px solid var(--amber); }
  tr.nse-held { background: rgba(31, 111, 235, 0.10); }
  tr.nse-rule td {
    color: var(--muted); font-size: 10px; letter-spacing: 0.04em;
    padding: 3px 8px; background: rgba(255, 255, 255, 0.03);
    border-bottom: 1px solid var(--border);
  }
  .tag {
    font-size: 9px; text-transform: uppercase; letter-spacing: 0.06em;
    padding: 1px 5px; border-radius: 3px; background: #243044; color: var(--muted);
  }
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
  .intel { margin: 12px 0 16px; }
  .kpis {
    display: grid; gap: 8px;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    margin-bottom: 12px;
  }
  @media (min-width: 640px) {
    .kpis { grid-template-columns: repeat(4, minmax(0, 1fr)); }
  }
  .kpi .lab {
    color: var(--muted); font-size: 10px; text-transform: uppercase;
    letter-spacing: 0.04em; margin-bottom: 2px;
  }
  .kpi .v { font-family: var(--mono); font-size: 16px; font-weight: 600; }
  .chart-wrap { margin: 8px 0 12px; }
  .eq-svg { width: 100%; height: auto; display: block; }
  .legend { display: flex; gap: 14px; margin: 4px 0 8px; font-family: var(--mono); font-size: 11px; color: var(--muted); }
  .legend i { display: inline-block; width: 12px; height: 2px; margin-right: 6px; vertical-align: middle; }
  .bar-row { display: grid; grid-template-columns: 7.5rem 1fr 5.5rem; gap: 8px; align-items: center; margin: 4px 0; font-family: var(--mono); font-size: 11px; }
  .bar-track { height: 8px; background: #243044; border-radius: 4px; overflow: hidden; }
  .bar-fill { height: 100%; background: var(--accent); }
  .bar-fill.pos { background: var(--green); }
  .bar-fill.neg { background: var(--red); }
</style>
</head>
<body>
<header>
  <h1><span>Tradex</span> Ops</h1>
  <nav class="lanes" id="lanes"></nav>
  <div class="meta header-meta" id="meta">loading…</div>
</header>
<div class="banner" id="banner"></div>
<div class="grid">
  <section class="full" id="sec-etf" hidden>
    <h2>ASX ETF monitor</h2>
    <div id="etf"></div>
  </section>
  <section class="full" id="sec-nse" hidden>
    <h2>NSE momentum rotator</h2>
    <div id="nse"></div>
  </section>
  <section class="full" id="sec-asx" hidden>
    <h2>ASX 200 momentum rotator</h2>
    <div id="asx"></div>
  </section>
</div>
<script>
const token = new URLSearchParams(location.search).get("token") || "";
// Which lane this page shows. "/" keeps its historical landing spot on ETF.
const PATH = location.pathname.replace(/\/+$/, "");
const LANE = PATH === "/nse" ? "nse" : PATH === "/asx" ? "asx" : "etf";
const LANES = [["etf", "/etf", "ASX ETF"], ["nse", "/nse", "NSE rotator"], ["asx", "/asx", "ASX rotator"]];
const LANE_TITLE = {etf: "ASX ETF", nse: "NSE rotator", asx: "ASX rotator"};
function renderLanes() {
  // location.search carries ?token=…; every lane link must keep it or the next
  // page loads unauthenticated. Escaped because it is attacker-controllable.
  const q = esc(location.search);
  document.getElementById("lanes").innerHTML = LANES.map(
    ([id, href, label]) =>
      '<a href="' + href + q + '"' + (LANE === id ? ' class="on"' : '') +
      '>' + esc(label) + '</a>').join("");
  document.getElementById("sec-" + LANE).hidden = false;
  document.title = "Tradex · " + (LANE_TITLE[LANE] || "ASX ETF");
}
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
function nseMom(m) {
  if (m == null || m === "") return "—";
  const pct = Math.round(Number(m) * 100);
  return (pct > 0 ? "+" : "") + pct + "%";
}
function nsePrice(v) {
  if (v == null || v === "") return "—";
  const n = Number(v);
  return n >= 100 ? "₹" + n.toFixed(0) : "₹" + n.toFixed(2);
}
function nseMoney(v) {
  if (v == null || v === "") return "—";
  return "₹" + Math.round(Number(v)).toLocaleString("en-IN");
}
function nsePct(v) {
  if (v == null || v === "") return "—";
  const pct = Math.round(Number(v) * 1000) / 10;
  return (pct > 0 ? "+" : "") + pct + "%";
}
function nseMc(v) {
  if (v == null || v <= 0) return "n/a";
  const cr = v / 1e7;
  if (cr >= 100000) return "₹" + (cr / 100000).toFixed(2) + "L Cr";
  if (cr >= 1000) return "₹" + (cr / 1000).toFixed(1) + "K Cr";
  return "₹" + Math.round(cr) + " Cr";
}
function asxPrice(v) {
  if (v == null || v === "") return "—";
  // Cents always: an ASX book is full of names under A$10 where whole dollars
  // would hide the actual quote.
  return "A$" + Number(v).toFixed(2);
}
// asxMoney formats aggregate amounts (order value, capital) — grouped, no
// cents. Distinct from asxPrice, which keeps cents because ASX share prices
// are routinely under A$10 and rounding them hides the actual quote.
function asxMoney(v) {
  if (v == null || v === "") return "—";
  return "A$" + Math.round(Number(v)).toLocaleString("en-AU");
}
function asxMc(v) {
  if (v == null || v <= 0) return "n/a";
  if (v >= 1e9) return "A$" + (v / 1e9).toFixed(1) + "B";
  if (v >= 1e6) return "A$" + Math.round(v / 1e6) + "M";
  return "A$" + Math.round(v);
}
// Per-market descriptor for renderRotator: the NSE and ASX lanes run identical
// strategy logic, so they share one renderer and differ only in currency,
// field names and labels.
const MARKETS = {
  nse: {
    label: "NSE", indexLabel: "Nifty", indexEMALabel: "EMA200",
    closeKey: "nifty_close", emaKey: "nifty_ema200",
    mcapKey: "market_cap_inr", orderValueKey: "approx_value_inr",
    capitalKey: "total_capital_inr", unrealKey: "unrealized_inr",
    costKey: "cost_inr", valueKey: "value_inr",
    price: nsePrice, money: nseMoney, mcap: nseMc,
  },
  asx: {
    label: "ASX", indexLabel: "XJO", indexEMALabel: "EMA200",
    closeKey: "index_close", emaKey: "index_ema200",
    mcapKey: "market_cap_aud", orderValueKey: "approx_value_aud",
    capitalKey: "total_capital_aud", unrealKey: "unrealized_aud",
    costKey: "cost_aud", valueKey: "value_aud",
    price: asxPrice, money: asxMoney, mcap: asxMc,
  },
};
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));
}
function kpi(lab, val, extra) {
  return '<div class="card kpi"><div class="lab">' + esc(lab) + '</div><div class="v ' + (extra || "") + '">' + val + '</div></div>';
}
function intelBlock(eq, M) {
  if (!eq) return "";
  const money = M.money || M.price;
  const pts = eq.points || [];
  const last = pts.length ? pts[pts.length - 1] : null;
  let html = '<div class="intel"><h3>Book intelligence</h3>';
  if (last) {
    const pnl = last.value - last.cost;
    const pct = last.cost > 0 ? pnl / last.cost : null;
    html += '<div class="kpis">';
    html += kpi("Invested", last.cost > 0 ? money(last.cost) : "—");
    html += kpi("Market value", money(last.value));
    html += kpi("Unrealized", last.cost > 0 ? money(pnl) : "—", last.cost > 0 ? cls(pnl) : "");
    html += kpi("Return", pct == null ? "—" : nsePct(pct), cls(pct));
    html += "</div>";
  }
  if (pts.length >= 2) html += equityChart(pts, eq.events || []);
  html += allocBars(eq.allocation || [], M);
  html += contribBars(eq.allocation || [], M);
  html += followTable(eq.follow_through || []);
  if (eq.events && eq.events.length) {
    html += '<div class="meta">Events: ' + eq.events.map(e => esc(e.date) + " " + esc(e.kind) + " — " + esc(e.label)).join(" · ") + "</div>";
  }
  if (eq.note) html += '<div class="meta">' + esc(eq.note) + "</div>";
  html += "</div>";
  return html;
}
function equityChart(pts, events) {
  const w = 640, h = 200, l = 54, r = 10, t = 10, b = 26;
  const innerW = w - l - r, innerH = h - t - b;
  let min = Infinity, max = -Infinity;
  let hasCost = false;
  for (const p of pts) {
    if (p.cost > 0) { hasCost = true; min = Math.min(min, p.cost); max = Math.max(max, p.cost); }
    if (p.value > 0) { min = Math.min(min, p.value); max = Math.max(max, p.value); }
  }
  if (!(max > min)) { max = min + 1; }
  const pad = (max - min) * 0.08 || 1;
  min -= pad; max += pad;
  const xAt = i => l + innerW * i / Math.max(pts.length - 1, 1);
  const yAt = v => t + innerH * (1 - (v - min) / (max - min));
  function pathFor(key) {
    const d = [];
    for (let i = 0; i < pts.length; i++) {
      const v = Number(pts[i][key]);
      if (!(v > 0)) continue;
      d.push((d.length ? "L" : "M") + xAt(i).toFixed(1) + " " + yAt(v).toFixed(1));
    }
    return d.join(" ");
  }
  function axisMoney(v) {
    const n = Math.abs(v);
    if (n >= 1e7) return (v / 1e7).toFixed(1) + "Cr";
    if (n >= 1e5) return (v / 1e5).toFixed(1) + "L";
    if (n >= 1000) return (v / 1000).toFixed(0) + "k";
    return String(Math.round(v));
  }
  let svg = '<svg viewBox="0 0 ' + w + " " + h + '" class="eq-svg" role="img" aria-label="Invested versus market value">';
  svg += '<line x1="' + l + '" y1="' + t + '" x2="' + l + '" y2="' + (h - b) + '" stroke="#2d3a4d"/>';
  svg += '<line x1="' + l + '" y1="' + (h - b) + '" x2="' + (w - r) + '" y2="' + (h - b) + '" stroke="#2d3a4d"/>';
  svg += '<text x="4" y="' + (t + 10) + '" fill="#8b9bb4" font-size="10" font-family="ui-monospace,monospace">' + axisMoney(max) + "</text>";
  svg += '<text x="4" y="' + (h - b) + '" fill="#8b9bb4" font-size="10" font-family="ui-monospace,monospace">' + axisMoney(min) + "</text>";
  if (hasCost) svg += '<path d="' + pathFor("cost") + '" fill="none" stroke="#8b9bb4" stroke-width="1.5"/>';
  svg += '<path d="' + pathFor("value") + '" fill="none" stroke="#3d9cf0" stroke-width="2"/>';
  for (const e of events) {
    let idx = -1, best = 1e9;
    for (let i = 0; i < pts.length; i++) {
      const diff = Math.abs(Date.parse(pts[i].date) - Date.parse(e.date));
      if (diff < best) { best = diff; idx = i; }
    }
    if (idx < 0) continue;
    const x = xAt(idx).toFixed(1);
    const col = e.kind === "algo" ? "#e6b84d" : "#8b9bb4";
    svg += '<line x1="' + x + '" y1="' + t + '" x2="' + x + '" y2="' + (h - b) + '" stroke="' + col + '" stroke-dasharray="3 3" opacity="0.7"/>';
  }
  svg += '<text x="' + l + '" y="' + (h - 8) + '" fill="#8b9bb4" font-size="10" font-family="ui-monospace,monospace">' + esc(pts[0].date) + "</text>";
  svg += '<text x="' + (w - r) + '" y="' + (h - 8) + '" fill="#8b9bb4" font-size="10" text-anchor="end" font-family="ui-monospace,monospace">' + esc(pts[pts.length - 1].date) + "</text>";
  svg += "</svg>";
  let html = '<div class="chart-wrap"><div class="legend">';
  if (hasCost) html += '<span><i style="background:#8b9bb4"></i>Invested</span>';
  html += '<span><i style="background:#3d9cf0"></i>Market value</span>';
  html += '<span><i style="background:#e6b84d"></i>Algo / book events</span></div>';
  html += svg + "</div>";
  return html;
}
function allocBars(alloc, M) {
  if (!alloc.length) return "";
  const money = M.money || M.price;
  const maxW = Math.max.apply(null, alloc.map(a => a.weight || 0));
  let html = "<h3>Allocation</h3>";
  for (const a of alloc.slice(0, 10)) {
    const pct = Math.round((a.weight || 0) * 1000) / 10;
    const width = maxW > 0 ? Math.round((a.weight || 0) / maxW * 100) : 0;
    html += '<div class="bar-row"><div>' + esc(a.symbol) + "</div>";
    html += '<div class="bar-track"><div class="bar-fill" style="width:' + width + '%"></div></div>';
    html += '<div class="num">' + pct + "% · " + (a.value ? money(a.value) : "—") + "</div></div>";
  }
  return html;
}
function contribBars(alloc, M) {
  const rows = alloc.filter(a => a.unrealized != null);
  if (!rows.length) return "";
  const money = M.money || M.price;
  const maxAbs = Math.max.apply(null, rows.map(a => Math.abs(a.unrealized || 0))) || 1;
  let html = "<h3>P&amp;L contribution</h3>";
  for (const a of rows.slice().sort((x, y) => Math.abs(y.unrealized) - Math.abs(x.unrealized)).slice(0, 10)) {
    const width = Math.round(Math.abs(a.unrealized) / maxAbs * 100);
    html += '<div class="bar-row"><div>' + esc(a.symbol) + "</div>";
    html += '<div class="bar-track"><div class="bar-fill ' + cls(a.unrealized) + '" style="width:' + width + '%"></div></div>';
    html += '<div class="num ' + cls(a.unrealized) + '">' + money(a.unrealized) + "</div></div>";
  }
  return html;
}
function followTable(rows) {
  if (!rows.length) return "";
  const done = rows.filter(r => r.status === "executed").length;
  let html = "<h3>Algo vs book <span class=\"meta\">last recommendation · " + done + "/" + rows.length + " followed</span></h3>";
  html += '<div class="scroll"><table><thead><tr><th>Side</th><th>Symbol</th><th class="num">Qty</th><th>Status</th></tr></thead><tbody>';
  for (const r of rows) {
    html += "<tr><td>" + esc(r.side) + "</td><td><strong>" + esc(r.symbol) + "</strong></td>";
    html += '<td class="num">' + (r.qty || "—") + '</td><td class="' + (r.status === "executed" ? "pos" : "neg") + '">' + esc(r.status) + "</td></tr>";
  }
  html += "</tbody></table></div>";
  return html;
}
function renderRotator(rec, M) {
  if (!rec) return '';
  if (rec.error) return '<div class="empty">' + esc(rec.error) + '</div>';
  // Aggregates use M.money when the market defines one; NSE does not, so it
  // falls back to M.price and its rendering is unchanged by the ASX lane.
  const money = M.money || M.price;
  let html = '<div class="nse-container">';
  const pf = rec.portfolio;
  if (pf) {
    if (pf.error) {
      html += '<div class="empty">' + esc(pf.error) + '</div>';
    } else {
      const holdings = (pf.holdings || []).slice().sort((a, b) => {
        const ua = a[M.unrealKey], ub = b[M.unrealKey];
        if (ua == null && ub == null) return 0;
        if (ua == null) return 1;
        if (ub == null) return -1;
        return ub - ua;
      });
      html += '<div class="nse-section">';
      html += '<h3>Current holdings <span class="meta">(portfolio.json)</span></h3>';
      if (!holdings.length) {
        html += '<div class="meta">No positions</div>';
      } else {
        html += '<div class="scroll"><table><thead><tr><th>Symbol</th><th class="num">Qty</th><th class="num">Avg</th><th class="num">Last</th><th class="num">% vs avg</th><th class="num">Unrealized</th></tr></thead><tbody>';
        for (const h of holdings) {
          const pct = h.pct_vs_avg;
          const unr = h[M.unrealKey];
          html += '<tr><td><strong>' + esc(h.symbol) + '</strong></td><td class="num">' + (h.qty ?? "—") +
            '</td><td class="num">' + M.price(h.avg_price) +
            '</td><td class="num">' + M.price(h.last_price) +
            '</td><td class="num ' + cls(pct) + '">' + nsePct(pct) +
            '</td><td class="num ' + cls(unr) + '">' + (unr == null ? "—" : M.price(unr)) +
            '</td></tr>';
        }
        html += '</tbody></table></div>';
        const qs = pf.quote_summary;
        if (qs) {
          html += '<div class="meta">Portfolio mark-to-market';
          if (qs.priced_holdings != null && qs.total_holdings != null && qs.priced_holdings < qs.total_holdings) {
            html += ' (' + qs.priced_holdings + '/' + qs.total_holdings + ' priced)';
          }
          html += ': <span class="' + cls(qs[M.unrealKey]) + '">' + M.price(qs[M.unrealKey]) +
            ' (' + nsePct(qs.pct_vs_avg) + ')</span>';
          html += ' · cost ' + money(qs[M.costKey]) + ' → value ' + money(qs[M.valueKey]);
          html += '</div>';
        }
      }
      html += '<div class="meta">as of ' + esc(pf.as_of || '—');
      if (pf[M.capitalKey] != null) html += ' · capital ' + money(pf[M.capitalKey]);
      if (pf.notes) html += ' · ' + esc(pf.notes);
      html += '</div></div>';
    }
  }
  html += intelBlock(rec.equity, M);
  const recLabel = rec.month ? ' (' + rec.month + ')' : '';
  // regime_filter is absent on records written before the filter became
  // configurable, when it was always on — undefined must read as true.
  const regimeFilterOn = rec.regime_filter !== false;
  const regime = rec.regime_invested ? "INVESTED"
    : regimeFilterOn ? "CASH — exit all positions"
    : "BELOW EMA200 — staying invested (regime filter off)";
  html += '<div class="meta">Last recommendation' + esc(recLabel) + ': <strong>' + esc(regime) + '</strong>';
  if (rec[M.closeKey] != null && rec[M.emaKey] != null) {
    html += ' · ' + M.indexLabel + ' ' + Number(rec[M.closeKey]).toFixed(0) + ' vs ' + M.indexEMALabel + ' ' + Number(rec[M.emaKey]).toFixed(0);
  }
  html += '</div>';
  // params is absent on recommendations written before the exit-hysteresis
  // change; every use below falls back to the old single-lookback rendering.
  const prm = rec.params || {};
  const topK = prm.top_k || 0, exitN = prm.exit_rank_n || 0;
  const fastM = prm.lookback_months || 6, slowM = prm.exit_lookback_months || 0;
  const heldSet = new Set(((rec.portfolio || {}).holdings || []).map(h => h.symbol));
  const ranked = rec.top_ranked || [];
  if (ranked.length) {
    html += '<div class="nse-section">';
    if (slowM && exitN) {
      html += '<h3>Top ' + ranked.length + ' ' + M.label + ' stocks ' +
        '<span class="meta">buy top ' + topK + ' by ' + fastM + 'm · sell below rank ' +
        exitN + ' on both ' + fastM + 'm &amp; ' + slowM + 'm</span></h3>';
    } else {
      html += '<h3>Top ' + ranked.length + ' ' + M.label + ' stocks (' + fastM + 'm momentum)</h3>';
    }
    html += '<div class="scroll"><table><thead><tr><th>Rank</th><th>Symbol</th><th>Name</th><th class="num">' +
      fastM + 'm</th>' + (slowM ? '<th class="num">' + slowM + 'm</th>' : '') +
      '<th class="num">Price</th><th class="num">MCap</th></tr></thead><tbody>';
    for (let i = 0; i < ranked.length; i++) {
      const t = ranked[i];
      const rank = i + 1;
      // Row bands make the rule legible: buy zone, hold-only zone, out.
      let band = '';
      if (topK && rank <= topK) band = 'nse-buy';
      else if (exitN && rank <= exitN) band = 'nse-keep';
      const held = heldSet.has(t.symbol);
      html += '<tr class="' + band + (held ? ' nse-held' : '') + '"><td>' + rank +
        '</td><td><strong>' + esc(t.symbol) + '</strong>' + (held ? ' <span class="tag">held</span>' : '') +
        '</td><td>' + esc(t.company_name || '') +
        '</td><td class="num ' + cls(t.momentum) + '">' + nseMom(t.momentum) + '</td>' +
        (slowM ? '<td class="num ' + cls(t.momentum_slow) + '">' + nseMom(t.momentum_slow) + '</td>' : '') +
        '<td class="num">' + M.price(t.last_close) + '</td><td class="num">' + M.mcap(t[M.mcapKey]) + '</td></tr>';
      // Threshold rules: after the last buy slot, and after the exit buffer.
      if (topK && rank === topK && ranked.length > topK) {
        html += '<tr class="nse-rule"><td colspan="' + (slowM ? 7 : 6) + '">↑ buy list (top ' + topK +
          ') · ↓ hold only — not bought, but not sold either</td></tr>';
      }
      if (exitN && rank === exitN && ranked.length > exitN) {
        html += '<tr class="nse-rule"><td colspan="' + (slowM ? 7 : 6) + '">↑ kept if held · ↓ sold if held</td></tr>';
      }
    }
    html += '</tbody></table></div></div>';
  }
  if (rec.orders && rec.orders.length) {
    html += '<div class="nse-section">';
    html += '<h3>Recommended orders <span class="meta">(last rotator run' + esc(recLabel) + ')</span></h3>';
    html += '<div class="scroll"><table><thead><tr><th>Side</th><th>Symbol</th><th>Name</th><th class="num">Qty</th><th class="num">Price</th><th class="num">Value</th></tr></thead><tbody>';
    for (const o of rec.orders) {
      html += '<tr class="' + (o.side === "SELL" ? "alert" : "") + '"><td>' + esc(o.side) +
        '</td><td><strong>' + esc(o.symbol) + '</strong></td><td>' + esc(o.company_name || '') +
        '</td><td class="num">' + (o.qty ?? "—") + '</td><td class="num">' + M.price(o.last_close) +
        '</td><td class="num">' + money(o[M.orderValueKey]) + '</td></tr>';
    }
    html += '</tbody></table></div></div>';
  }
  // holds_info carries the ranks that explain each hold; older
  // recommendations only have the plain symbol list.
  if (rec.holds_info && rec.holds_info.length) {
    html += '<div class="nse-section">';
    html += '<h3>Holds <span class="meta">kept while inside rank ' + exitN + ' on either list</span></h3>';
    html += '<div class="scroll"><table><thead><tr><th>Symbol</th><th class="num">' + fastM +
      'm rank</th><th class="num">' + slowM + 'm rank</th><th>Kept by</th></tr></thead><tbody>';
    for (const h of rec.holds_info) {
      const byFast = h.rank > 0 && exitN && h.rank <= exitN;
      const bySlow = h.rank_slow > 0 && exitN && h.rank_slow <= exitN;
      const why = byFast && bySlow ? 'both' : byFast ? fastM + 'm' : bySlow ? slowM + 'm' : '—';
      html += '<tr><td><strong>' + esc(h.symbol) + '</strong></td><td class="num">' +
        (h.rank > 0 ? '#' + h.rank : '—') + '</td><td class="num">' +
        (h.rank_slow > 0 ? '#' + h.rank_slow : '—') + '</td><td>' + esc(why) + '</td></tr>';
    }
    html += '</tbody></table></div></div>';
  } else if (rec.holds && rec.holds.length) {
    html += '<div class="meta">Hold: ' + esc(rec.holds.join(", ")) + '</div>';
  }
  // Names removed by the price floor (ASX lane only; absent elsewhere).
  // Shown because a silent filter over a 200-name universe is how a
  // data-quality gate stops being noticed — and this one can drop a name
  // the user still holds.
  if (rec.below_min_price && rec.below_min_price.length) {
    const floor = prm.min_price_aud;
    html += '<div class="nse-section">';
    html += '<h3>Below price floor <span class="meta">not ranked' +
      (floor ? ' · under ' + M.price(floor) : '') + '</span></h3>';
    html += '<div class="meta">' + esc(rec.below_min_price.join(", ")) + '</div>';
    html += '</div>';
  }
  if (rec.warnings && rec.warnings.length) {
    html += '<div class="nse-section warnings">';
    for (const w of rec.warnings) {
      html += '<div class="empty">⚠ ' + esc(w) + '</div>';
    }
    html += '</div>';
  }
  html += '<div class="meta">as of ' + esc(rec.run_at || rec.month || '—') + '</div>';
  html += '</div>';
  return html;
}

async function refresh() {
  const cfg = await api("/api/ui-config");
  // Only the visible lane is fetched — the other endpoint is not just hidden,
  // it is never requested, so NSE quote lookups stop firing on the ETF page.
  const [etf, nse, asx] = await Promise.all([
    LANE === "etf" ? api("/api/etf").catch(() => ({error: "not available"})) : null,
    LANE === "nse" ? api("/api/nse").catch(() => ({error: "not available"})) : null,
    LANE === "asx" ? api("/api/asx").catch(() => ({error: "not available"})) : null,
  ]);
  document.getElementById("meta").textContent =
    "tz=" + cfg.reporting_tz +
    " · refresh=" + (cfg.refresh_interval_ms/1000) + "s" +
    (cfg.mock ? " · MOCK" : "");

  // ETF Monitor
  let etfHtml = '';
  if (etf && etf.error) {
    etfHtml = '<div class="empty">' + esc(etf.error) + '</div>';
  } else if (etf) {
    let html = '<div class="etf-container">';
    const held = etf.holdings || {};
    if (held.error) {
      html += '<div class="empty">' + esc(held.error) + '</div>';
    } else if (held.holdings && held.holdings.length) {
      html += '<div class="etf-section"><h3>Current holdings <span class="meta">(holdings.json)</span></h3>';
      html += '<div class="scroll"><table><thead><tr><th>Ticker</th><th class="num">Qty</th><th class="num">Avg</th></tr></thead><tbody>';
      for (const h of held.holdings) {
        html += '<tr><td><strong>' + esc(h.ticker || h.symbol) + '</strong></td><td class="num">' +
          (h.qty ?? '—') + '</td><td class="num">' + (h.avg_price ? asxPrice(h.avg_price) : '—') + '</td></tr>';
      }
      html += '</tbody></table></div>';
      html += '<div class="meta">as of ' + esc(held.as_of || '—') + '</div></div>';
    }
    html += intelBlock(etf.equity, MARKETS.asx);
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

  // Shared by the NSE and ASX lanes — same strategy, same rendering; M carries
  // the currency, field names and labels that differ. Every field access below
  // is guarded because the dashboard also renders historical records written
  // before a field existed.
  if (LANE === "nse") {
    document.getElementById("nse").innerHTML =
      renderRotator(nse, MARKETS.nse) || '<div class="empty">Loading…</div>';
  }
  if (LANE === "asx") {
    document.getElementById("asx").innerHTML =
      renderRotator(asx, MARKETS.asx) || '<div class="empty">Loading…</div>';
  }
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
renderLanes();
loop();
</script>
</body>
</html>
`
