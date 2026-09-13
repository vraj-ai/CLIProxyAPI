package main

// hub_page.go: the Fleet control-plane page, served as a plugin resource.
// The shell carries no data — state and mutations come from the key-gated
// management routes, so the page asks for the management key once and keeps
// it in localStorage like the core console does.

const hubPageHTML = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Fleet</title>
<style>
  :root { --bg:#0d1117; --panel:#161b22; --line:#30363d; --text:#e6edf3; --muted:#8b949e; --accent:#3fb950; --warn:#d29922; --link:#58a6ff; --bad:#f85149; --chip:#21262d; }
  * { box-sizing:border-box; }
  body { background:var(--bg); color:var(--text); font:14px/1.5 -apple-system,system-ui,sans-serif; margin:0; }
  .top { max-width:1160px; margin:0 auto; padding:20px 24px 40px; }
  header { display:flex; align-items:baseline; justify-content:space-between; gap:16px; flex-wrap:wrap; }
  h1 { font-size:18px; margin:0; font-weight:600; }
  h1 .sub { color:var(--muted); font-weight:400; font-size:13px; margin-left:10px; }
  .keybox { display:flex; gap:8px; align-items:center; }
  .keybox input { background:var(--chip); border:1px solid var(--line); border-radius:6px; color:var(--text); padding:6px 10px; font-size:13px; width:240px; }
  .keybox input:focus { outline:2px solid var(--link); outline-offset:0; }
  .keybox .hint { color:var(--muted); font-size:12px; }
  .card { background:var(--panel); border:1px solid var(--line); border-radius:10px; padding:14px 16px; margin-top:14px; }
  .card h2 { font-size:11px; letter-spacing:.08em; text-transform:uppercase; color:var(--muted); margin:0 0 10px; font-weight:600; display:flex; justify-content:space-between; align-items:center; }
  .card h2 .act { font-size:11px; font-weight:400; }
  .grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(250px,1fr)); gap:12px; }
  .card.g { margin-top:0; }
  .pipe { display:flex; align-items:center; gap:10px; flex-wrap:wrap; font-size:13px; }
  .pipe .node { background:var(--chip); border:1px solid var(--line); border-radius:8px; padding:6px 10px; display:flex; gap:7px; align-items:center; }
  .pipe .arrow { color:var(--muted); }
  .dot { width:8px; height:8px; border-radius:50%; background:var(--muted); display:inline-block; }
  .dot.on { background:var(--accent); } .dot.off { background:var(--bad); } .dot.idle { background:var(--warn); }
  .mono { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:12px; }
  .muted { color:var(--muted); }
  .link { color:var(--link); text-decoration:none; } .link:hover { text-decoration:underline; }
  .quota-name { font-weight:600; display:flex; justify-content:space-between; align-items:baseline; }
  .quota-name .plan { font-weight:400; font-size:12px; color:var(--muted); }
  .res { margin-top:10px; }
  .res .lbl { display:flex; justify-content:space-between; font-size:12px; color:var(--muted); margin-bottom:3px; }
  .bar { position:relative; height:8px; background:var(--chip); border-radius:4px; overflow:hidden; }
  .bar .fill { position:absolute; left:0; top:0; bottom:0; background:var(--accent); border-radius:4px; }
  .bar.behind .fill { background:var(--bad); }
  .bar .tick { position:absolute; top:-2px; bottom:-2px; width:2px; background:var(--text); opacity:.7; }
  .badge { display:inline-block; font-size:11px; padding:1px 7px; border-radius:8px; border:1px solid var(--line); color:var(--muted); }
  .badge.ok { color:var(--accent); border-color:var(--accent); }
  .badge.warn { color:var(--warn); border-color:var(--warn); }
  .badge.bad { color:var(--bad); border-color:var(--bad); }
  .rows { width:100%; border-collapse:collapse; font-size:13px; }
  .rows td, .rows th { padding:6px 8px 6px 0; border-top:1px solid var(--line); text-align:left; }
  .rows th { font-size:11px; text-transform:uppercase; letter-spacing:.08em; color:var(--muted); font-weight:600; border-top:none; }
  .cand { display:flex; justify-content:space-between; align-items:center; padding:7px 10px; border:1px solid var(--line); border-radius:8px; margin-top:6px; }
  .cand.next { border-color:var(--accent); }
  .cand .why { font-size:12px; color:var(--muted); }
  .cand .why.bad { color:var(--bad); }
  .sw { position:relative; width:34px; height:20px; flex:none; cursor:pointer; }
  .sw input { opacity:0; width:0; height:0; }
  .sw .tr { position:absolute; inset:0; background:var(--chip); border:1px solid var(--line); border-radius:10px; transition:.15s; }
  .sw .tr::after { content:""; position:absolute; left:2px; top:2px; width:14px; height:14px; border-radius:50%; background:var(--muted); transition:.15s; }
  .sw input:checked + .tr { background:#1f6feb33; border-color:var(--link); }
  .sw input:checked + .tr::after { transform:translateX(14px); background:var(--link); }
  .sw input:disabled + .tr { opacity:.4; cursor:not-allowed; }
  .chips { display:flex; flex-wrap:wrap; gap:6px; }
  .chip { font-size:12px; padding:3px 10px; border-radius:12px; border:1px solid var(--line); background:var(--chip); color:var(--muted); cursor:pointer; font-family:ui-monospace,monospace; }
  .chip.on { color:var(--accent); border-color:var(--accent); background:#3fb95014; }
  .chip:hover { border-color:var(--link); }
  .toast { position:fixed; bottom:18px; right:18px; background:var(--panel); border:1px solid var(--line); border-radius:8px; padding:10px 14px; font-size:13px; max-width:380px; box-shadow:0 8px 24px #0008; }
  .toast.err { border-color:var(--bad); }
  .toast.ok { border-color:var(--accent); }
  .empty { color:var(--muted); font-size:13px; }
  .effort-ladder { display:flex; gap:5px; flex-wrap:wrap; margin-top:8px; }
  .effort-ladder .e { font-size:11px; padding:2px 8px; border-radius:6px; background:var(--chip); border:1px solid var(--line); color:var(--muted); font-family:ui-monospace,monospace; }
  #lock { text-align:center; padding:80px 20px; }
  #lock .card { display:inline-block; text-align:left; min-width:340px; }
  .search { background:var(--chip); border:1px solid var(--line); border-radius:6px; color:var(--text); padding:5px 9px; font-size:12px; width:180px; }
  footer { color:var(--muted); font-size:12px; margin-top:18px; display:flex; justify-content:space-between; flex-wrap:wrap; gap:8px; }
</style></head><body>
<div class="top">
<header>
  <h1>Fleet<span class="sub" id="linkline"></span></h1>
  <div class="keybox">
    <input id="key" type="password" placeholder="management key" autocomplete="off">
    <span class="hint" id="keyhint"></span>
  </div>
</header>

<div id="lock"><div class="card"><h2>Management key</h2>
  <p class="muted" style="margin:0 0 10px">The hub reads and mutates live proxy state, so it uses the same key-gated management API as the console. Paste the key once; it stays in this browser.</p>
  <div class="muted mono" id="lockerr"></div>
</div></div>

<div id="dash" hidden>

<div class="card"><h2>Pipeline<span class="act muted" id="pipenote"></span></h2>
  <div class="pipe" id="pipeline"></div>
</div>

<div class="card"><h2>Quota pace<span class="act muted">used vs elapsed · openusage --force (15s cache)</span></h2>
  <div class="grid" id="quota"></div>
</div>

<div class="card"><h2>CPA Router<span class="act muted" id="routersub"></span></h2>
  <div id="routerbody"></div>
  <h2 style="margin-top:16px">Recent decisions</h2>
  <div id="decisions" class="muted" style="font-size:12px"></div>
</div>

<div class="card"><h2>Providers<span class="act muted">toggles write through the core management API</span></h2>
  <table class="rows"><thead><tr><th>provider</th><th>kind</th><th>models</th><th>state</th><th></th></tr></thead><tbody id="providers"></tbody></table>
</div>

<div class="card"><h2>Models · pxpipe scope<span class="act muted">lit = request body gets pxpipe transform</span></h2>
  <input class="search" id="mfilter" placeholder="filter models…">
  <div class="chips" id="models" style="margin-top:10px"></div>
</div>

<div class="card"><h2>Fleet flags<span class="act muted">plugins/fleet/config — persists to config.yaml</span></h2>
  <div class="pipe" id="flags"></div>
</div>

<footer>
  <span id="stamp"></span>
  <span><a class="link" href="/v0/resource/plugins/fleet/savings" target="_blank">savings ↗</a> · <a class="link" href="http://127.0.0.1:47821/dashboard" target="_blank">pxpipe dashboard ↗</a></span>
</footer>
</div>
</div>
<div id="toast"></div>
<script>
const MGMT = "/v0/management";
const LS = "fleet.hub.key";
const keyEl = document.getElementById("key");
keyEl.value = localStorage.getItem(LS) || "";
keyEl.addEventListener("change", () => { localStorage.setItem(LS, keyEl.value); refresh(); });
keyEl.addEventListener("keydown", e => { if (e.key === "Enter") { localStorage.setItem(LS, keyEl.value); refresh(); } });

function toast(msg, cls) {
  const t = document.getElementById("toast");
  t.textContent = msg; t.className = "toast " + (cls || "ok");
  setTimeout(() => { t.textContent = ""; t.className = ""; }, 3200);
}
async function api(path, opts) {
  const r = await fetch(MGMT + path, Object.assign({ headers: { "authorization": "Bearer " + keyEl.value, "content-type": "application/json" } }, opts || {}));
  if (r.status === 401 || r.status === 403) { throw Object.assign(new Error("bad management key"), { auth: true }); }
  if (!r.ok) { let m = "HTTP " + r.status; try { m = (await r.json()).error || m; } catch (e) {} throw new Error(m); }
  return r.json();
}
const pct = x => (x * 100).toFixed(0) + "%";
const fmt = n => n >= 1000 ? (n / 1000).toFixed(1) + "k" : (Math.round(n * 10) / 10).toString();
function ago(iso) {
  const s = (Date.now() - new Date(iso)) / 1000;
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  return Math.floor(s / 3600) + "h ago";
}
function until(iso) {
  const s = (new Date(iso) - Date.now()) / 1000;
  if (s < 0) return "now";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h" + Math.floor((s % 3600) / 60) + "m";
  return Math.floor(s / 86400) + "d" + Math.floor((s % 86400) / 3600) + "h";
}

function renderPipe(s) {
  const nodes = [
    ["harness", true, "pi / any client"],
    ["pxpipe proxy", s.pxpipe.reachable, s.pxpipe.reachable ? "47821" : "down"],
    ["shim", s.pxpipe.shim_enabled ? s.pxpipe.shim_up : null, s.pxpipe.shim_enabled ? (s.pxpipe.shim_up ? "transform hop" : "down") : "off"],
    ["cliproxyapi", true, "8317"],
    ["fleet plugin", true, "caveman·ponytail·router"],
    ["lead agent", s.router.enabled ? !!s.router.eligible : null, s.router.eligible || (s.router.enabled ? "none eligible" : "router off")]
  ];
  document.getElementById("pipeline").innerHTML = nodes.map(n => {
    const dot = n[1] === null ? "idle" : (n[1] ? "on" : "off");
    return '<div class="node"><span class="dot ' + dot + '"></span><b>' + n[0] + '</b><span class="muted">' + n[2] + '</span></div>';
  }).join('<span class="arrow">→</span>');
  document.getElementById("linkline").textContent = "· point harnesses at " + s.link;
}

function renderQuota(s) {
  const el = document.getElementById("quota");
  if (!s.quota || !s.quota.length) { el.innerHTML = '<div class="empty">openusage unavailable — install or check PATH.</div>'; return; }
  el.innerHTML = s.quota.map(p => {
    const res = (p.resources || []).map(r => {
      const used = r.used_frac || 0, elapsed = r.paced_frac || 0;
      const behind = r.paced;
      return '<div class="res"><div class="lbl"><span>' + r.name + '</span><span>' +
        fmt(r.remaining) + "/" + fmt(r.limit) + ' · resets ' + (r.resets_at ? until(r.resets_at) : "—") +
        ' <span class="badge ' + (behind ? "bad" : "ok") + '">' + (behind ? "behind pace" : "on pace") + '</span></span></div>' +
        '<div class="bar' + (behind ? " behind" : "") + '"><div class="fill" style="width:' + Math.min(100, used * 100) + '%"></div>' +
        '<div class="tick" style="left:' + Math.min(99, elapsed * 100) + '%"></div></div></div>';
    }).join("");
    return '<div class="card g"><div class="quota-name">' + p.name + '<span class="plan">' + (p.plan || "") + (p.stale ? " · stale" : "") + '</span></div>' + (res || '<div class="empty">no resources</div>') + '</div>';
  }).join("");
}

function renderRouter(s) {
  const r = s.router;
  document.getElementById("routersub").textContent = r.enabled ? (r.eligible ? "next request → " + r.eligible : "enabled · nothing eligible right now") : "disabled";
  const cands = (r.candidates || []).map(c =>
    '<div class="cand' + (c.ok ? " next" : "") + '"><span>' + c.label + ' <span class="muted mono">' + (c.model || "—") + " · " + (c.effort || "—") + '</span></span>' +
    '<span class="why' + (c.ok ? "" : " bad") + '">' + (c.ok ? "eligible" : c.reason) + '</span></div>').join("");
  document.getElementById("routerbody").innerHTML =
    '<div class="effort-ladder">' + ["minimal", "low", "medium", "high", "xhigh", "max", "ultra"].map(e => '<span class="e">' + e + "</span>").join("") + "</div>" +
    '<div style="margin-top:10px">' + (cands || '<span class="empty">router disabled</span>') + "</div>";
  const dec = (r.decisions || []).slice(-14).reverse().map(d =>
    '<div class="res" style="margin-top:6px"><div class="lbl"><span class="mono">' + d.correlation_id + '</span><span>' + ago(d.at) + '</span></div>' +
    '<span class="' + (d.outcome === "served" || d.outcome === "completed" ? "" : "muted") + '">' + d.outcome + (d.agent ? " → " + d.agent : "") + (d.effort ? " · " + d.effort : "") + (d.reason ? ' <span class="badge warn">' + d.reason + "</span>" : "") + (d.sidekick ? ' <span class="badge">sidekick: ' + d.sidekick + "</span>" : "") + '</span></div>').join("");
  document.getElementById("decisions").innerHTML = dec || '<span class="empty">no router traffic yet</span>';
}

async function toggleAuth(name, disabled, el) {
  el.disabled = true;
  try {
    await api("/auth-files/status", { method: "PATCH", body: JSON.stringify({ name: name, disabled: disabled }) });
    toast((disabled ? "disabled " : "enabled ") + name); refresh();
  } catch (e) { toast(e.message, "err"); el.disabled = false; refresh(); }
}
async function toggleCompat(name, disabled, el) {
  el.disabled = true;
  try {
    await api("/openai-compatibility", { method: "PATCH", body: JSON.stringify({ name: name, value: { disabled: disabled } }) });
    toast((disabled ? "disabled " : "enabled ") + name); refresh();
  } catch (e) { toast(e.message, "err"); el.disabled = false; refresh(); }
}
function renderProviders(s) {
  document.getElementById("providers").innerHTML = (s.providers || []).map((p, i) => {
    const dis = p.disabled;
    const label = p.kind === "plugin" ? '<span class="badge">virtual</span>' : (dis ? '<span class="badge bad">off</span>' : '<span class="badge ok">on</span>');
    const sw = '<label class="sw"><input type="checkbox" ' + (dis ? "" : "checked") + ' onchange="providerToggle(' + i + ',this.checked,this)"><span class="tr"></span></label>';
    return "<tr><td><b>" + p.name + '</b> <span class="muted">' + (p.detail || "") + '</span></td><td class="muted">' + p.kind + "</td><td class=\"muted\">" + (p.models || []).length + "</td><td>" + label + "</td><td>" + sw + "</td></tr>";
  }).join("");
  window.__providers = s.providers;
}
async function providerToggle(i, on, el) {
  const p = window.__providers[i];
  if (p.kind === "plugin") { await setFlag("router_enabled", on, el); return; }
  if (p.kind === "compat") { await toggleCompat(p.name, !on, el); return; }
  await toggleAuth(p.detail, !on, el);
}

async function toggleScope(model, on, el) {
  el.style.opacity = .4;
  try {
    await api("/fleet/pxpipe/scope/toggle", { method: "POST", body: JSON.stringify({ model: model, on: on }) });
    refresh();
  } catch (e) { toast(e.message, "err"); el.style.opacity = 1; }
}
function renderModels(s) {
  const q = (document.getElementById("mfilter").value || "").toLowerCase();
  const scope = new Set(s.pxpipe.scope || []);
  const list = (s.models || []).filter(m => !q || m.toLowerCase().includes(q));
  document.getElementById("models").innerHTML = list.length
    ? list.map(m => '<span class="chip' + (scope.has(m) ? " on" : "") + '" title="toggle pxpipe transform" onclick="toggleScope(\'' + m.replace(/'/g, "") + '\',' + !scope.has(m) + ',this)">' + m + "</span>").join("")
    : '<span class="empty">no models served — enable a provider above</span>';
}
document.getElementById("mfilter").addEventListener("input", () => window.__state && renderModels(window.__state));

async function setFlag(key, val, el) {
  if (el) el.disabled = true;
  try {
    await api("/plugins/fleet/config", { method: "PATCH", body: JSON.stringify({ [key]: val }) });
    toast(key + " = " + val); refresh();
  } catch (e) { toast(e.message, "err"); refresh(); }
}
function renderFlags(s) {
  const f = s.flags;
  const rows = [["caveman", "caveman", f.caveman, "compressed-reply instruction"], ["ponytail", "ponytail", f.ponytail, "lazy-solution instruction"], ["router", "router_enabled", f.router, "cpa router virtual model"], ["pxpipe shim", "pxpipe_enabled", f.pxpipe, "transform hop in the link"]];
  document.getElementById("flags").innerHTML = rows.map(r =>
    '<div class="node"><label class="sw"><input type="checkbox" ' + (r[2] ? "checked" : "") + ' onchange="setFlag(\'' + r[1] + '\',' + !r[2] + ',this)"><span class="tr"></span></label><b>' + r[0] + '</b><span class="muted">' + r[3] + "</span></div>").join("");
}

let inflight = false;
async function refresh() {
  if (inflight) return; inflight = true;
  document.getElementById("keyhint").textContent = "";
  try {
    const s = await api("/fleet/state");
    window.__state = s;
    document.getElementById("lock").hidden = true;
    document.getElementById("dash").hidden = false;
    renderPipe(s); renderQuota(s); renderRouter(s); renderProviders(s); renderModels(s); renderFlags(s);
    document.getElementById("stamp").textContent = "updated " + new Date(s.generated_at).toLocaleTimeString() + " · auto-refresh 15s";
  } catch (e) {
    if (e.auth) {
      document.getElementById("lock").hidden = false;
      document.getElementById("dash").hidden = true;
      document.getElementById("lockerr").textContent = keyEl.value ? "Key rejected — check remote-management.secret-key." : "";
      document.getElementById("keyhint").textContent = "required";
    } else {
      toast("state fetch failed: " + e.message, "err");
    }
  }
  inflight = false;
}
refresh();
setInterval(refresh, 15000);
</script></body></html>`
