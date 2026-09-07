/**
 * The stats dashboard: one static page, served by this Worker.
 *
 * SERVED FROM THE WORKER RATHER THAN FROM PAGES, and that is a
 * deviation from ORC-233's own architecture note worth arguing. That
 * note wanted the page in the Pages output and Access on a subdomain,
 * "which also puts page and API same-origin and removes CORS entirely".
 * The second half does not follow from the first: a Pages custom domain
 * and this Worker are different origins, so the page would still be
 * cross-origin to the store AND would still need a credential a browser
 * cannot safely hold. Serving it from the Worker makes the same-origin
 * claim true by construction — one hostname, one Access application,
 * and the viewer's own identity as the credential.
 *
 * NO CHART LIBRARY, and this is the second deviation. The note wanted
 * Chart.js vendored as one pinned file; that is ~200KB of third-party
 * minified JavaScript committed to the repo, and the charts here are
 * stacked bars and a total line over a few dozen buckets. The SVG below
 * is about sixty lines and has no supply chain, no pin to bump and no
 * CDN to be down. "The toolchain is the maintenance cost, not the
 * library" was the note's own reasoning; a library is a maintenance
 * cost too when the alternative is this small.
 */

export interface DashboardOptions {
  /** Project names offered in the selector, best-effort. */
  projects: string[];
}

/**
 * The page.
 *
 * A template string rather than a file read at runtime: a Worker has no
 * filesystem, and the alternative is a build step, which is the thing
 * this is avoiding.
 */
export function dashboardHTML(opts: DashboardOptions): string {
  const projects = JSON.stringify(opts.projects);
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>pipeline stats</title>
<style>
  :root {
    color-scheme: light dark;
    --bg: #fbfbfa; --fg: #1c1c1a; --dim: #6b6b66; --line: #e0e0dc;
    --card: #ffffff; --accent: #3b6ea5;
  }
  @media (prefers-color-scheme: dark) {
    :root { --bg:#16171a; --fg:#e6e6e3; --dim:#95958f; --line:#2c2e33; --card:#1d1f23; }
  }
  * { box-sizing: border-box; }
  body { margin:0; background:var(--bg); color:var(--fg);
         font:14px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif; }
  header { padding:16px 20px; border-bottom:1px solid var(--line); }
  h1 { font-size:15px; margin:0 0 10px; font-weight:600; letter-spacing:.01em; }
  main { padding:20px; max-width:1100px; }
  .bar { display:flex; flex-wrap:wrap; gap:10px 14px; align-items:center; }
  label { font-size:12px; color:var(--dim); display:flex; gap:5px; align-items:center; }
  select, input, button {
    font:inherit; font-size:13px; padding:4px 7px; background:var(--card);
    color:var(--fg); border:1px solid var(--line); border-radius:5px;
  }
  input[type=text] { min-width:150px; }
  button { cursor:pointer; }
  section { margin:0 0 26px; }
  h2 { font-size:13px; font-weight:600; margin:0 0 3px; }
  .note { font-size:12px; color:var(--dim); margin:0 0 10px; max-width:70ch; }
  .card { background:var(--card); border:1px solid var(--line); border-radius:7px; padding:14px; overflow-x:auto; }
  table { border-collapse:collapse; font-size:13px; width:100%; }
  th,td { text-align:left; padding:4px 12px 4px 0; white-space:nowrap; }
  th { color:var(--dim); font-weight:500; }
  td.n { text-align:right; font-variant-numeric:tabular-nums; }
  .msg { color:var(--dim); font-size:13px; }
  .err { color:#b1442e; font-size:13px; white-space:pre-wrap; }
  .key { display:flex; flex-wrap:wrap; gap:4px 12px; font-size:12px; color:var(--dim); margin-top:8px; }
  .key i { width:9px; height:9px; border-radius:2px; display:inline-block; margin-right:4px; }
  svg { display:block; }
  svg text { font-size:10px; fill:var(--dim); }
</style>
</head>
<body>
<header>
  <h1>pipeline stats</h1>
  <div class="bar">
    <label>project <select id="project"></select></label>
    <label>bucket <select id="bucket">
      <option value="">all</option><option value="day">day</option>
      <option value="week" selected>week</option><option value="month">month</option>
    </select></label>
    <label>milestone <select id="milestone"><option value="">all</option></select></label>
    <label>has labels <input type="text" id="hasLabels" placeholder="bug,harness"></label>
    <label>lacks labels <input type="text" id="lacksLabels" placeholder="milestone-boundary"></label>
    <label>boundary tickets <select id="boundary">
      <option value="exclude">exclude</option><option value="include">include</option>
      <option value="only">only</option>
    </select></label>
    <label>states <input type="text" id="states" placeholder="(defaults)"></label>
    <label>not states <input type="text" id="notStates" placeholder="blocked"></label>
    <label><input type="checkbox" id="includeTerminal"> terminal</label>
    <label><input type="checkbox" id="includeQueue"> backlog/todo</label>
    <button id="go">reload</button>
  </div>
</header>
<main>
  <div id="error" class="err" hidden></div>

  <section>
    <h2>Actions minutes</h2>
    <p class="note">Per-job ceilings, which is the host's own billing model. Rounding
      waste is the difference from raw wall clock, not an estimate — for short frequent
      jobs it is a large share of the bill, and its remedy is a wider debounce rather
      than a different runner. The collector's own runs are excluded.</p>
    <div class="card" id="minutes"><span class="msg">loading…</span></div>
  </section>

  <section>
    <h2>Time in state</h2>
    <p class="note">Terminal states and the queue states are excluded unless asked for:
      backlog measures how early a ticket was known about and Todo measures how long the
      rest of the milestone took, and neither is this pipeline's time. An open interval
      counts as zero, so the same query asked twice agrees with itself.</p>
    <div class="card" id="states"><span class="msg">loading…</span></div>
  </section>

  <section>
    <h2>State instances</h2>
    <p class="note">Entries, not tickets. A ticket that bounced to Reworking three times
      contributes three — which is the rework-trend question.</p>
    <div class="card" id="instances"><span class="msg">loading…</span></div>
  </section>

  <section>
    <h2>Milestones</h2>
    <p class="note">Windows run from the previous boundary's completion to this one's.
      A boundary ticket's creation is not its milestone's start: it is created when the
      milestone is ready to close.</p>
    <div class="card" id="milestones"><span class="msg">loading…</span></div>
  </section>
</main>
<script>
const PROJECTS = ${projects};
const $ = (id) => document.getElementById(id);
const MIN = 60000;

// A fixed palette rather than a generated one: the same state must be
// the same colour between two loads, or a reader compares the wrong
// pair of bars. Keyed by name, with a stable hash fallback for a state
// nobody here named.
const NAMED = {
  designing:"#3b6ea5", design_review:"#6a9bd1", ready_for_design:"#9dbfe0",
  in_progress:"#4f8a5b", reconciling:"#7fae86", checks:"#a8c9ad",
  ready_for_rework:"#c9a227", reworking:"#e0b93c", blocked:"#b1442e",
  merged:"#7a5ea8", boundary_review:"#a98fc4", ready_for_boundary:"#c6b3da",
};
function colour(name) {
  if (NAMED[name]) return NAMED[name];
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
  return "hsl(" + h + " 42% 55%)";
}

function params() {
  const q = new URLSearchParams();
  const b = $("bucket").value; if (b) q.set("bucket", b);
  const m = $("milestone").value; if (m) q.set("milestone", m);
  for (const k of ["hasLabels","lacksLabels","states","notStates"]) {
    const v = $(k).value.trim(); if (v) q.set(k, v);
  }
  q.set("boundary", $("boundary").value);
  if ($("includeTerminal").checked) q.set("includeTerminal", "1");
  if ($("includeQueue").checked) q.set("includeQueue", "1");
  return q;
}

async function read(op, q) {
  const project = $("project").value;
  const res = await fetch("/stats/" + encodeURIComponent(project) + op +
    (q && q.toString() ? "?" + q.toString() : ""), { credentials: "same-origin" });
  if (!res.ok) {
    throw new Error(op + ": HTTP " + res.status + " — " + (await res.text()).trim());
  }
  return res.json();
}

// ---- drawing -------------------------------------------------------
//
// One stacked-bar routine, used three times. Buckets across, series
// stacked, values already summed by the store.

function stacked(el, buckets, seriesNames, valueOf, fmt) {
  if (buckets.length === 0) {
    el.innerHTML = '<span class="msg">nothing in this window</span>';
    return;
  }
  const W = Math.max(560, buckets.length * 46 + 60), H = 220;
  const padL = 52, padB = 34, padT = 8;
  let max = 0;
  for (const b of buckets) {
    let sum = 0;
    for (const s of seriesNames) sum += valueOf(b, s);
    if (sum > max) max = sum;
  }
  if (max <= 0) max = 1;
  const plotH = H - padB - padT, bw = (W - padL - 10) / buckets.length;
  const parts = ['<svg viewBox="0 0 ' + W + ' ' + H + '" width="' + W + '" height="' + H + '" role="img">'];
  for (let g = 0; g <= 4; g++) {
    const y = padT + plotH - (plotH * g) / 4;
    parts.push('<line x1="' + padL + '" y1="' + y + '" x2="' + W + '" y2="' + y +
      '" stroke="var(--line)"/>');
    parts.push('<text x="' + (padL - 6) + '" y="' + (y + 3) + '" text-anchor="end">' +
      fmt((max * g) / 4) + "</text>");
  }
  buckets.forEach((b, i) => {
    let acc = 0;
    for (const s of seriesNames) {
      const v = valueOf(b, s);
      if (v <= 0) continue;
      const h = (v / max) * plotH;
      const y = padT + plotH - acc - h;
      acc += h;
      parts.push('<rect x="' + (padL + i * bw + 3) + '" y="' + y + '" width="' + (bw - 6) +
        '" height="' + h + '" fill="' + colour(s) + '"><title>' + esc(b.name) + " · " +
        esc(s) + " · " + fmt(v) + "</title></rect>");
    }
    parts.push('<text x="' + (padL + i * bw + bw / 2) + '" y="' + (H - 12) +
      '" text-anchor="middle">' + esc(b.name) + "</text>");
  });
  parts.push("</svg>");
  parts.push('<div class="key">' + seriesNames.map((s) =>
    '<span><i style="background:' + colour(s) + '"></i>' + esc(s) + "</span>").join("") + "</div>");
  el.innerHTML = parts.join("");
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) =>
    ({ "&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;" })[c]);
}
function hours(ms) { return (ms / 3600000).toFixed(1); }
function mins(ms) { return Math.round(ms / MIN); }

function group(rows, seriesKey, valueKey) {
  const byBucket = new Map(), series = new Set();
  for (const r of rows) {
    const name = String(r.bucket);
    if (!byBucket.has(name)) byBucket.set(name, { name, v: {} });
    byBucket.get(name).v[r[seriesKey]] = Number(r[valueKey] ?? 0);
    series.add(String(r[seriesKey]));
  }
  return {
    buckets: [...byBucket.values()].sort((a, b) => a.name < b.name ? -1 : 1),
    series: [...series].sort(),
  };
}

// ---- loading -------------------------------------------------------

async function load() {
  $("error").hidden = true;
  const q = params();
  try {
    const [states, minutes, milestones] = await Promise.all([
      read("/states", q),
      read("/minutes", (() => {
        // Minutes are a property of runs, not of tickets, so only the
        // bucket carries over. Applying a label filter here would
        // silently answer a different question with the same chart.
        const m = new URLSearchParams();
        const b = $("bucket").value; if (b) m.set("bucket", b);
        return m;
      })()),
      read("/milestones"),
    ]);

    const mg = group(minutes.minutes ?? [], "series", "billable_ms");
    stacked($("minutes"), mg.buckets, mg.series, (b, s) => b.v[s] ?? 0, (v) => mins(v) + "m");
    const waste = (minutes.minutes ?? []).reduce((a, r) => a + Number(r.rounding_ms ?? 0), 0);
    const bill = (minutes.minutes ?? []).reduce((a, r) => a + Number(r.billable_ms ?? 0), 0);
    if (bill > 0) {
      $("minutes").insertAdjacentHTML("beforeend",
        '<p class="note" style="margin:10px 0 0">' + mins(bill) + " minute(s) total, " +
        mins(waste) + " of them (" + Math.round((waste / bill) * 100) +
        "%) per-job rounding waste.</p>");
    }

    const sg = group(states.states ?? [], "state", "total_ms");
    stacked($("states"), sg.buckets, sg.series, (b, s) => b.v[s] ?? 0, (v) => hours(v) + "h");
    const ig = group(states.states ?? [], "state", "instances");
    stacked($("instances"), ig.buckets, ig.series, (b, s) => b.v[s] ?? 0, (v) => Math.round(v));

    const ex = states.excluded ?? {};
    const excluded = [...(ex.terminalCategories ?? []), ...(ex.queueStates ?? []),
                      ...(ex.notStates ?? [])];
    $("states").insertAdjacentHTML("beforeend",
      '<p class="note" style="margin:10px 0 0">' +
      (excluded.length
        ? "Excluded: " + esc(excluded.join(", ")) + ". An empty bucket here means nothing " +
          "spent time there, not that you did not ask."
        : "Nothing excluded.") + "</p>");

    fillMilestones(milestones);
  } catch (e) {
    $("error").hidden = false;
    $("error").textContent = String(e && e.message ? e.message : e) +
      "\\n\\nA 401 here means this hostname is not behind the Access application the " +
      "Worker is configured for, or you reached it at its workers.dev origin, which " +
      "nothing fronts.";
    for (const id of ["minutes","states","instances","milestones"]) $(id).innerHTML = "";
  }
}

let milestonesFilled = false;
function fillMilestones(rows) {
  const el = $("milestones");
  if (!rows || rows.length === 0) {
    el.innerHTML = '<span class="msg">no milestones — the collector derives them from the boundary tickets</span>';
    return;
  }
  const head = "<tr><th>milestone</th><th>kind</th><th>window</th><th>days</th><th>boundary</th></tr>";
  const body = rows.map((m) => {
    const start = new Date(Number(m.window_start));
    const end = m.window_end == null ? null : new Date(Number(m.window_end));
    const days = end ? ((end - start) / 86400000).toFixed(1) : "open";
    return "<tr><td>" + esc(m.name) + "</td><td>" + esc(m.kind) + "</td><td>" +
      start.toISOString().slice(0, 10) + " → " +
      (end ? end.toISOString().slice(0, 10) : "…") + '</td><td class="n">' + days +
      "</td><td>" + esc(m.boundary_ticket ?? "") + "</td></tr>";
  }).join("");
  el.innerHTML = "<table>" + head + body + "</table>";

  if (!milestonesFilled) {
    milestonesFilled = true;
    const sel = $("milestone");
    for (const m of rows) {
      const o = document.createElement("option");
      o.value = m.name; o.textContent = m.name;
      sel.appendChild(o);
    }
  }
}

// ---- boot ----------------------------------------------------------

const url = new URL(location.href);
const wanted = url.searchParams.get("project");
for (const p of PROJECTS.length ? PROJECTS : [wanted].filter(Boolean)) {
  const o = document.createElement("option");
  o.value = p; o.textContent = p;
  $("project").appendChild(o);
}
if (wanted && ![...$("project").options].some((o) => o.value === wanted)) {
  const o = document.createElement("option");
  o.value = wanted; o.textContent = wanted;
  $("project").appendChild(o);
}
if (wanted) $("project").value = wanted;

$("go").addEventListener("click", load);
$("project").addEventListener("change", () => { milestonesFilled = false; $("milestone").length = 1; load(); });
load();
</script>
</body>
</html>`;
}
