const $ = (sel) => document.querySelector(sel);

const state = {
  tab: document.body.dataset.tab || "endpoint",
  page: 1,
  pageSize: 50,
  sort: "latency",
  dir: "asc",
  status: "",
  total: 0,
  pages: 0,
};

function fmtTime(s) {
  if (!s || s.startsWith("0001-01-01")) return "—";
  return new Date(s).toLocaleTimeString();
}

async function loadStats() {
  try {
    const r = await fetch("/api/stats");
    const d = await r.json();

    if ($("#sTotalReq"))  $("#sTotalReq").textContent = (d.requests || 0).toLocaleString();
    if ($("#sErrors"))    $("#sErrors").textContent   = (d.errors || 0).toLocaleString();
    if ($("#sInTokens"))  $("#sInTokens").textContent = (d.input_tokens || 0).toLocaleString();
    if ($("#sOutTokens")) $("#sOutTokens").textContent = (d.output_tokens || 0).toLocaleString();
    if ($("#sEstCost"))   $("#sEstCost").textContent  = `~$${(d.est_cost || 0).toFixed(4)}`;

    if ($("#sTotal"))   $("#sTotal").textContent   = d.proxy_total;
    if ($("#sAlive"))   $("#sAlive").textContent   = d.proxy_alive;
    if ($("#sDead"))    $("#sDead").textContent    = d.proxy_dead;
    if ($("#sUnknown")) $("#sUnknown").textContent = d.proxy_unknown;
    if ($("#sOK"))      $("#sOK").textContent      = d.successes;
    if ($("#sFail"))    $("#sFail").textContent    = d.fails;

    // Render Charts on Usage Tab
    if (state.tab === "usage") {
      renderAreaChart(d.hourly_reqs || Array(24).fill(0));
      renderHeatmap(d.daily_reqs || {});
    }

    // Progress Bar Update
    if (d.validation && $("#progressSection")) {
      const v = d.validation;
      const titleEl = $("#progressTitle");
      const statsEl = $("#progressStats");
      const fillEl  = $("#progressBarFill");

      if (v.validating) {
        const pct = v.to_validate > 0 ? Math.floor((v.tested / v.to_validate) * 100) : 0;
        titleEl.textContent = "Validating Proxies...";
        statsEl.textContent = `${v.tested} / ${v.to_validate} (${pct}%)`;
        fillEl.style.width = `${pct}%`;
      } else {
        titleEl.textContent = "Validation Completed / Idle";
        statsEl.textContent = `${v.to_validate || 0} Tested (100%)`;
        fillEl.style.width = v.to_validate > 0 ? "100%" : "0%";
      }
    }
  } catch (e) { console.error(e); }
}

// ── 1. Smoothed Area Chart (00:00 -> 23:59) ───────────────────────
function renderAreaChart(data) {
  const canvas = $("#areaChartCanvas");
  if (!canvas) return;
  const ctx = canvas.getContext("2d");
  const rect = canvas.getBoundingClientRect();
  canvas.width = rect.width * (window.devicePixelRatio || 1);
  canvas.height = 180 * (window.devicePixelRatio || 1);
  ctx.scale(window.devicePixelRatio || 1, window.devicePixelRatio || 1);

  const width = rect.width;
  const height = 180;
  const padding = { top: 20, right: 20, bottom: 30, left: 35 };
  const graphW = width - padding.left - padding.right;
  const graphH = height - padding.top - padding.bottom;

  ctx.clearRect(0, 0, width, height);

  const maxVal = Math.max(5, ...data);
  const points = data.map((val, idx) => {
    const x = padding.left + (idx / 23) * graphW;
    const y = padding.top + graphH - (val / maxVal) * graphH;
    return { x, y, val };
  });

  // Draw Grid Lines & X Labels
  ctx.strokeStyle = "rgba(48, 54, 61, 0.4)";
  ctx.fillStyle = "#8b949e";
  ctx.font = "10px ui-monospace, sans-serif";
  ctx.textAlign = "center";

  for (let i = 0; i <= 23; i += 3) {
    const x = padding.left + (i / 23) * graphW;
    ctx.beginPath();
    ctx.moveTo(x, padding.top);
    ctx.lineTo(x, padding.top + graphH);
    ctx.stroke();
    const label = (i < 10 ? "0" + i : i) + ":00";
    ctx.fillText(label, x, height - 10);
  }

  // Draw Smoothed Area Path (Catmull-Rom / Bezier Curve)
  ctx.beginPath();
  ctx.moveTo(points[0].x, points[0].y);
  for (let i = 0; i < points.length - 1; i++) {
    const xc = (points[i].x + points[i + 1].x) / 2;
    const yc = (points[i].y + points[i + 1].y) / 2;
    ctx.quadraticCurveTo(points[i].x, points[i].y, xc, yc);
  }
  ctx.lineTo(points[points.length - 1].x, points[points.length - 1].y);

  // Stroke Line
  ctx.strokeStyle = "#f0883e";
  ctx.lineWidth = 2.5;
  ctx.stroke();

  // Gradient Fill
  ctx.lineTo(padding.left + graphW, padding.top + graphH);
  ctx.lineTo(padding.left, padding.top + graphH);
  ctx.closePath();

  const gradient = ctx.createLinearGradient(0, padding.top, 0, padding.top + graphH);
  gradient.addColorStop(0, "rgba(240, 136, 62, 0.35)");
  gradient.addColorStop(1, "rgba(240, 136, 62, 0.0)");
  ctx.fillStyle = gradient;
  ctx.fill();

  // Draw Dots
  points.forEach(p => {
    if (p.val > 0) {
      ctx.beginPath();
      ctx.arc(p.x, p.y, 4, 0, Math.PI * 2);
      ctx.fillStyle = "#f0883e";
      ctx.fill();
      ctx.strokeStyle = "#161b22";
      ctx.lineWidth = 1.5;
      ctx.stroke();
    }
  });
}

// ── 2. GitHub-style Annual Heatmap Graph ──────────────────────────
function renderHeatmap(dailyMap) {
  const container = $("#heatmapContainer");
  if (!container) return;
  container.innerHTML = "";

  const today = new Date();
  const startDate = new Date();
  startDate.setDate(today.getDate() - 364); // Past 365 days (52 weeks)

  let totalPastYearReqs = 0;
  let maxDaily = 1;
  Object.values(dailyMap).forEach(v => {
    if (v > maxDaily) maxDaily = v;
    totalPastYearReqs += v;
  });

  if ($("#heatmapTotal")) {
    $("#heatmapTotal").textContent = `${totalPastYearReqs.toLocaleString()} requests in past year`;
  }

  const cur = new Date(startDate);
  while (cur <= today) {
    const dateStr = cur.toISOString().split("T")[0];
    const count = dailyMap[dateStr] || 0;

    let level = 0;
    if (count > 0) {
      const ratio = count / maxDaily;
      if (ratio <= 0.25) level = 1;
      else if (ratio <= 0.5) level = 2;
      else if (ratio <= 0.75) level = 3;
      else level = 4;
    }

    const sq = document.createElement("div");
    sq.className = `square lvl-${level}`;
    sq.title = `${dateStr}: ${count} requests`;
    container.appendChild(sq);

    cur.setDate(cur.getDate() + 1);
  }
}

async function loadProxies() {
  if (state.tab !== "proxies" || !$("#proxyTable")) return;
  try {
    const params = new URLSearchParams({
      page: state.page,
      pageSize: state.pageSize,
      sort: state.sort,
      dir: state.dir,
      status: state.status,
    });
    const r = await fetch("/api/proxies?" + params.toString());
    const d = await r.json();
    state.total = d.total;
    state.pages = d.pages;

    const tbody = $("#proxyTable tbody");
    tbody.innerHTML = "";
    for (const p of (d.items || [])) {
      const flag = p.flag || "🌐";
      const proto = (p.protocol || "http").toUpperCase();
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td class="mono"><span class="flag" title="${p.country || 'Unknown'}">${flag}</span>${p.address}</td>
        <td><span class="proto-tag proto-${p.protocol}">${proto}</span></td>
        <td><span class="status ${p.status}">${p.status}</span></td>
        <td>${p.latency_ms}</td>
        <td>${fmtTime(p.last_check)}</td>
        <td>${p.successes}</td>
        <td>${p.fails}</td>`;
      tbody.appendChild(tr);
    }

    if ($("#proxyCount")) $("#proxyCount").textContent = `${state.total} entries`;
    if ($("#pageInfo"))   $("#pageInfo").textContent = `Page ${state.page} / ${state.pages || 1}`;

    if ($("#firstPage")) $("#firstPage").disabled = state.page <= 1;
    if ($("#prevPage"))  $("#prevPage").disabled  = state.page <= 1;
    if ($("#nextPage"))  $("#nextPage").disabled  = state.page >= state.pages;
    if ($("#lastPage"))  $("#lastPage").disabled  = state.page >= state.pages;

    document.querySelectorAll("#proxyTable th[data-sort]").forEach(th => {
      th.classList.remove("sort-asc", "sort-desc");
      if (th.dataset.sort === state.sort) {
        th.classList.add(state.dir === "asc" ? "sort-asc" : "sort-desc");
      }
    });
  } catch (e) { console.error(e); }
}

async function loadLogs() {
  if (state.tab !== "logs" || !$("#logs")) return;
  try {
    const r = await fetch("/api/logs");
    const list = await r.json();
    const el = $("#logs");
    el.innerHTML = (list || []).slice(-100).map(l => `
      <div class="log-line">
        <span class="muted">${new Date(l.time).toLocaleTimeString()}</span>
        <span class="level l-${l.level}">${l.level}</span>
        <span>${escapeHTML(l.msg)}</span>
      </div>`).join("");
    el.scrollTop = el.scrollHeight;
  } catch (e) { console.error(e); }
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c => ({
    "&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"
  }[c]));
}

document.addEventListener("click", (e) => {
  if (e.target.classList.contains("copy")) {
    const t = e.target.dataset.copy;
    navigator.clipboard.writeText(t).then(() => {
      const old = e.target.textContent; e.target.textContent = "copied!";
      setTimeout(() => e.target.textContent = old, 1200);
    });
  }
});

// Event listeners for proxies tab
if (state.tab === "proxies") {
  document.querySelectorAll("#proxyTable th[data-sort]").forEach(th => {
    th.addEventListener("click", () => {
      const k = th.dataset.sort;
      if (state.sort === k) {
        state.dir = state.dir === "asc" ? "desc" : "asc";
      } else {
        state.sort = k;
        state.dir = (k === "latency" || k === "fails") ? "asc" : "desc";
      }
      state.page = 1;
      loadProxies();
    });
  });

  if ($("#statusFilter")) {
    $("#statusFilter").addEventListener("change", (e) => {
      state.status = e.target.value;
      state.page = 1;
      loadProxies();
    });
  }
  if ($("#pageSize")) {
    $("#pageSize").addEventListener("change", (e) => {
      state.pageSize = parseInt(e.target.value, 10) || 50;
      state.page = 1;
      loadProxies();
    });
  }
  if ($("#firstPage")) $("#firstPage").addEventListener("click", () => { state.page = 1; loadProxies(); });
  if ($("#prevPage"))  $("#prevPage").addEventListener("click",  () => { if (state.page > 1) state.page--; loadProxies(); });
  if ($("#nextPage"))  $("#nextPage").addEventListener("click",  () => { if (state.page < state.pages) state.page++; loadProxies(); });
  if ($("#lastPage"))  $("#lastPage").addEventListener("click",  () => { state.page = Math.max(1, state.pages); loadProxies(); });
  if ($("#gotoPage")) {
    $("#gotoPage").addEventListener("change", (e) => {
      const n = parseInt(e.target.value, 10);
      if (n >= 1 && n <= state.pages) { state.page = n; loadProxies(); }
      e.target.value = "";
    });
  }

  if ($("#refresh")) {
    $("#refresh").addEventListener("click", async () => {
      await fetch("/api/refresh", { method: "POST" });
      setTimeout(loadAll, 1500);
    });
  }
  if ($("#validate")) {
    $("#validate").addEventListener("click", async () => {
      await fetch("/api/validate", { method: "POST" });
    });
  }
}

function loadAll() {
  loadStats();
  if (state.tab === "proxies") loadProxies();
  if (state.tab === "logs") loadLogs();
}

loadAll();
setInterval(loadAll, 5000);
