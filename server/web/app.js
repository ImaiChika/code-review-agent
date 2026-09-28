/* ═══════════════════════════════════════════════════════════
   CR//AGENT — 控制台逻辑（vanilla JS，无构建、无依赖）
   hash 路由：#/dashboard  #/review  #/tasks  #/task/:id  #/rules
   说明性文字一律收敛为 ？ 悬浮提示（.help）
   ═══════════════════════════════════════════════════════════ */

"use strict";

const $view = document.getElementById("view");
const $crumb = document.getElementById("crumb");
const $statusline = document.getElementById("statusline");
const $meta = document.getElementById("topbar-meta");

/* ---------- 认证（M7-F2）----------
   入口链接 http://host/?token=<token> 会自动保存到 localStorage，
   之后写请求带上 X-Auth-Token；浏览（GET）无需 token。 */
const urlToken = new URLSearchParams(location.search).get("token");
if (urlToken) {
  localStorage.setItem("cra_token", urlToken);
  history.replaceState(null, "", location.pathname + location.hash); // 清掉地址栏 token，防截图/转发泄漏
}
const authToken = localStorage.getItem("cra_token") || "";

/* ---------- 常量映射 ---------- */
const SEV = {
  high:   { label: "高危", cls: "high" },
  medium: { label: "中危", cls: "medium" },
  low:    { label: "低危", cls: "low" },
  info:   { label: "信息", cls: "info" },
};
const CAT = {
  security: "安全风险", resource: "资源泄漏", error_handling: "错误处理",
  testing: "测试缺失", lifecycle: "生命周期", sensitive_leak: "敏感信息泄漏",
  concurrency: "并发问题",
};
const GRADE_COLOR = { A: "gA", B: "gB", C: "gC", D: "gD", F: "gF" };

/* ---------- 工具 ---------- */
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
function fmtTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d)) return esc(iso);
  const p = n => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
function badge(sev) {
  const s = SEV[sev] || { label: sev, cls: "info" };
  return `<span class="badge ${s.cls}">${s.label}</span>`;
}
function gradeBadge(g) {
  if (!g) return "—";
  return `<span class="badge ${GRADE_COLOR[g] || ""}">${g}</span>`;
}
function severityColor(cls) {
  return { high: "var(--red)", medium: "var(--orange)", low: "var(--blue)", info: "var(--ink-dim)" }[cls] || "var(--accent)";
}
async function api(path, opts) {
  opts = opts || {};
  const headers = Object.assign({}, opts.headers);
  if (authToken) headers["X-Auth-Token"] = authToken;
  const res = await fetch(path, Object.assign({}, opts, { headers }));
  const body = await res.json().catch(() => ({ error: "响应解析失败" }));
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body;
}
function setStatus(html) { $statusline.innerHTML = html; }
function log(msg) { setStatus(esc(msg)); }

/* ？ 悬浮提示（纯 CSS hover 展开） */
function help(tip, left) {
  return `<span class="help${left ? " tip-left" : ""}">?<span class="tip">${tip}</span></span>`;
}

/* 面板：标题 + 可选 ？ 提示 + 内容 */
function panel(label, body, tip, left) {
  return `<div class="panel">
    <div class="panel-title">${esc(label)}${tip ? help(tip, left) : ""}</div>
    <div class="panel-body">${body}</div>
  </div>`;
}

/* 标记 evidence 中的脱敏占位为高亮 */
function renderEvidence(ev) {
  return esc(ev)
    .replace(/(\*\*\*REDACTED\*\*\*|\*\*\*PRIVATE_KEY_REMOVED\*\*\*)/g, '<span class="redacted">$1</span>');
}

/* ---------- 路由 ---------- */
const routes = [
  { re: /^#\/dashboard$/,      view: viewDashboard, crumb: "~/code-review-agent/<b>dashboard</b>" },
  { re: /^#\/review$/,         view: viewReview,    crumb: "~/code-review-agent/<b>new-review</b>" },
  { re: /^#\/tasks$/,          view: viewTasks,     crumb: "~/code-review-agent/<b>tasks</b>" },
  { re: /^#\/task\/(.+)$/,     view: viewTask,      crumb: "~/code-review-agent/tasks/<b>:id</b>" },
  { re: /^#\/rules$/,          view: viewRules,     crumb: "~/code-review-agent/<b>engine</b>" },
];

async function route() {
  const hash = location.hash || "#/dashboard";
  for (const r of routes) {
    const m = hash.match(r.re);
    if (m) {
      $crumb.innerHTML = r.crumb.replace(":id", esc(m[1] || ""));
      document.querySelectorAll(".nav a").forEach(a =>
        a.classList.toggle("active", hash.startsWith(a.getAttribute("href"))));
      $view.innerHTML = `<div class="boot"><span class="spin"></span></div>`;
      try { await r.view(m[1]); }
      catch (e) {
        $view.innerHTML = `<div class="view-enter"><div class="notice">✗ ${esc(e.message)}</div>
          <a class="link" href="#/dashboard">← 返回总览</a></div>`;
      }
      return;
    }
  }
  location.hash = "#/dashboard";
}
window.addEventListener("hashchange", route);

/* ---------- 健康检查 ---------- */
async function initHealth() {
  try {
    const h = await api("/api/health");
    document.getElementById("foot-version").textContent = "v" + h.version;
    const el = document.getElementById("foot-status");
    el.textContent = "● 在线";
    el.classList.add("online");
    $meta.textContent = "";
  } catch {
    document.getElementById("foot-status").textContent = "● 离线";
  }
}

/* ══════════════ 视图 01：总览看板 ══════════════ */
async function viewDashboard() {
  log("加载统计 …");
  const [stats, rules] = await Promise.all([api("/api/stats"), api("/api/rules")]);
  const fs = stats.finding_stats || {};

  const sevMax = Math.max(1, ...Object.values(fs.by_severity || { x: 1 }));
  const sevRows = Object.entries(SEV).map(([k, s]) => {
    const n = (fs.by_severity || {})[k] || 0;
    return hbar(s.label, n, sevMax, severityColor(k));
  }).join("");

  const catEntries = Object.entries(fs.by_category || {});
  const catMax = Math.max(1, ...catEntries.map(e => e[1]));
  const catRows = catEntries.map(([k, n]) =>
    hbar(CAT[k] || k, n, catMax, "var(--accent)")).join("") ||
    `<div class="empty">暂无数据</div>`;

  const dimRows = (rules.dimensions || []).map(d =>
    hbar(d.name, `${Math.round(d.weight * 100)}%`, 30, "var(--violet)", true)).join("");

  const ruleRows = (fs.top_rules || []).map((r, i) => `
    <tr><td class="dim mono">TOP${i + 1}</td><td class="mono">${esc(r.rule_id)}</td>
    <td style="text-align:right">${r.count}</td></tr>`).join("") ||
    `<tr><td colspan="3" class="empty">暂无命中</td></tr>`;

  const recentRows = (stats.recent_risks || []).map(r => `
    <tr class="rowlink" onclick="location.hash='#/task/${esc(r.task_id)}'">
      <td class="mono dim">${esc(r.task_id)}</td>
      <td class="dim">${esc((r.input_path || "").split("/").pop())}</td>
      <td style="text-align:right">${r.risk_score.toFixed(0)}</td>
      <td style="text-align:right">${gradeBadge(r.risk_grade)}</td>
    </tr>`).join("") || `<tr><td colspan="4" class="empty">还没有任务</td></tr>`;

  $view.innerHTML = `
  <div class="view-enter stagger">
    <h2 class="view-title">总览看板</h2>

    <div class="grid grid-cards">
      <div class="stat"><div class="stat-label">审查任务</div>
        <div class="stat-value">${stats.total_tasks ?? 0}</div></div>
      <div class="stat"><div class="stat-label">问题发现 ${help("所有任务中高置信度问题（findings）的总数，不含低置信度警告")}</div>
        <div class="stat-value">${fs.total ?? 0}</div></div>
      <div class="stat"><div class="stat-label">平均风险分 ${help("各任务风险评分的均值。评分 0-100，越高越危险；等级 A(0-20) / B(20-40) / C(40-60) / D(60-80) / F(80-100)")}</div>
        <div class="stat-value">${(stats.avg_risk ?? 0).toFixed(1)}<small> /100</small></div></div>
      <div class="stat"><div class="stat-label">最高风险分 ${help("历史任务中最高的风险评分")}</div>
        <div class="stat-value">${(stats.max_risk ?? 0).toFixed(0)}<small> /100</small></div></div>
    </div>

    <div class="grid grid-2">
      ${panel("近 30 天趋势", trendSVG(stats.trend_daily),
        "柱 = 每天审查任务数；橙色点线 = 当天平均风险分。数据来自任务表 SQL 聚合")}
      ${panel("严重度分布", sevRows)}
      ${panel("问题分类", catRows)}
      ${panel("规则命中排行", `<table class="t"><thead><tr><th>#</th><th>RULE ID</th><th style="text-align:right">命中</th></tr></thead><tbody>${ruleRows}</tbody></table>`)}
      ${panel("最近任务", `<table class="t"><thead><tr><th>TASK</th><th>输入</th><th style="text-align:right">风险</th><th style="text-align:right">等级</th></tr></thead><tbody>${recentRows}</tbody></table>`)}
      ${panel("评分维度", dimRows, "风险评分由 6 个维度加权求和：安全问题 30% / 敏感信息 15% / 资源泄漏 20% / 错误处理 15% / 测试覆盖 15% / 并发问题 5%")}
      ${panel("业务管线", pipelineHTML(), "一次审查从输入到落库的完整流程，CLI 与 Web 服务共用同一条管线")}
    </div>
  </div>`;
  log(`总览就绪 — ${stats.total_tasks ?? 0} 个任务 · ${fs.total ?? 0} 条发现`);
}

function hbar(label, n, max, color, isText) {
  const pct = isText ? (Number(String(n).replace("%", "")) / max * 100) : (n / max * 100);
  return `<div class="hbar">
    <div class="hbar-label">${esc(label)}</div>
    <div class="hbar-track"><div class="hbar-fill" style="width:${Math.max(pct, 1.5)}%;background:${color}"></div></div>
    <div class="hbar-num">${isText ? esc(n) : n}</div>
  </div>`;
}

/* 近 30 天趋势 SVG：柱 = 任务数（左轴自动缩放），点线 = 平均风险分（0-100 右轴） */
function trendSVG(daily) {
  if (!daily || !daily.length) return `<div class="empty">暂无数据</div>`;
  const W = 620, H = 150, PL = 30, PR = 34, PT = 14, PB = 22;
  const iw = W - PL - PR, ih = H - PT - PB;
  const maxTasks = Math.max(1, ...daily.map(d => d.tasks));
  const bw = Math.min(24, iw / daily.length * 0.6);
  const step = iw / daily.length;
  const x = i => PL + step * i + step / 2;

  let bars = "", dots = "", labels = "";
  daily.forEach((d, i) => {
    const h = d.tasks / maxTasks * (ih - 8);
    bars += `<rect x="${(x(i) - bw / 2).toFixed(1)}" y="${(PT + ih - h).toFixed(1)}"
      width="${bw.toFixed(1)}" height="${h.toFixed(1)}" rx="2" fill="var(--accent)" opacity="0.35">
      </rect>`;
    const cy = PT + ih - (d.avg_risk / 100) * ih;
    dots += `<circle cx="${x(i).toFixed(1)}" cy="${cy.toFixed(1)}" r="2.6" fill="var(--orange)"></circle>`;
    if (i > 0) {
      const px = x(i - 1), py = PT + ih - (daily[i - 1].avg_risk / 100) * ih;
      dots += `<line x1="${px.toFixed(1)}" y1="${py.toFixed(1)}" x2="${x(i).toFixed(1)}" y2="${cy.toFixed(1)}"
        stroke="var(--orange)" stroke-width="1.4" opacity="0.8"></line>`;
    }
    if (daily.length <= 10 || i % Math.ceil(daily.length / 8) === 0 || i === daily.length - 1) {
      labels += `<text x="${x(i).toFixed(1)}" y="${H - 6}" font-size="9" fill="var(--ink-dim)"
        text-anchor="middle">${d.date.slice(5)}</text>`;
    }
  });

  return `<svg viewBox="0 0 ${W} ${H}" style="width:100%;height:auto">
    <line x1="${PL}" y1="${PT + ih}" x2="${W - PR}" y2="${PT + ih}" stroke="var(--line)"></line>
    <text x="${PL - 6}" y="${PT + 8}" font-size="9" fill="var(--ink-dim)" text-anchor="end">${maxTasks}</text>
    <text x="${PL - 6}" y="${PT + ih + 3}" font-size="9" fill="var(--ink-dim)" text-anchor="end">0</text>
    <text x="${W - PR + 6}" y="${PT + 8}" font-size="9" fill="var(--orange)">100</text>
    ${bars}${dots}${labels}
  </svg>
  <div style="display:flex;gap:16px;margin-top:6px;font-size:11px;color:var(--ink-dim)">
    <span><span style="display:inline-block;width:8px;height:8px;background:var(--accent);opacity:.45;border-radius:2px"></span> 任务数（峰值 ${maxTasks}/天）</span>
    <span><span style="display:inline-block;width:8px;height:2px;background:var(--orange);vertical-align:middle"></span> 平均风险分（0-100）</span>
  </div>`;
}

function pipelineHTML() {
  const steps = [
    ["STEP-1", "输入解析", "unified diff / git 工作区 / API 上传"],
    ["STEP-2", "规则装载", "7 条内置规则 + YAML 自定义规则"],
    ["STEP-3", "规则审查", "go/scanner 词法事实匹配新增行"],
    ["STEP-3.5", "沙箱执行", "go vet / go test，先过权限策略"],
    ["STEP-4", "去重降噪", "同键去重，低置信度转警告"],
    ["STEP-5", "风险评分", "6 维度加权 0-100 分 + A-F 等级"],
    ["STEP-6", "报告生成", "JSON + Markdown 双格式"],
    ["STEP-7", "落库审计", "SQLite 6 张表，按任务可查"],
  ];
  return `<div class="pipe">${steps.map(s => `
    <div class="pipe-step">
      <div class="ps-id">${s[0]}</div>
      <div class="ps-name">${s[1]}</div>
      <div class="ps-desc">${s[2]}</div>
    </div>`).join("")}</div>`;
}

/* ══════════════ 视图 02：新建审查 ══════════════ */
let reviewSource = "diff";
let samplesCache = null;

async function viewReview() {
  // 视图每次渲染 DOM 都重置为 diff 标签，全局状态必须跟着重置，
  // 否则从 repo 标签离开再回来，提交仍会读空的仓库输入框
  reviewSource = "diff";
  if (!samplesCache) {
    try { samplesCache = (await api("/api/samples")).samples || []; }
    catch { samplesCache = []; }
  }
  const chips = samplesCache.map((s, i) =>
    `<span class="chip" data-idx="${i}" onclick="loadSample(${i})">${esc(s.name)}</span>`).join("");

  $view.innerHTML = `
  <div class="view-enter">
    <h2 class="view-title">新建审查</h2>

    <div class="tabs">
      <div class="tab on" id="tab-diff" onclick="switchSource('diff')">粘贴 DIFF ${help("unified diff 格式：以 ---/+++ 开头，@@ 标注行号区间，+ 开头为新增行。只审查新增行")}</div>
      <div class="tab" id="tab-code" onclick="switchSource('code')">粘贴代码 ${help("不懂 diff？直接粘贴整个代码文件，全部内容按新增行审查")}</div>
      <div class="tab" id="tab-upload" onclick="switchSource('upload')">上传文件 ${help("选择一个或多个文本文件（.go 等），或一个 zip 压缩包；≤10MB，zip 内 ≤500 个文件、单个 ≤2MB；二进制文件自动跳过")}</div>
      <div class="tab" id="tab-pr" onclick="switchSource('pr')">GitHub PR ${help("粘贴 GitHub PR 链接（github.com/{owner}/{repo}/pull/123）或简写 owner/repo#123，自动拉取该 PR 的 diff 审查。公开仓库无需凭证；服务端配置 GITHUB_TOKEN 可提升限额")}</div>
      <div class="tab" id="tab-repo" onclick="switchSource('repo')">仓库路径 ${help("填写服务器本机 git 仓库路径，取其未提交变更进行审查")}</div>
    </div>

    <div id="src-diff">
      ${samplesCache.length ? `<div class="chips">${chips}</div>` : ""}
      <div class="field">
        <label>DIFF 内容</label>
        <textarea class="ta" id="ta-diff" placeholder="--- a/creds.go&#10;+++ b/creds.go&#10;@@ -1,2 +1,4 @@&#10; package creds&#10;&#10;+var apiKey = &quot;sk-...&quot;"></textarea>
      </div>
    </div>

    <div id="src-code" style="display:none">
      <div class="field">
        <label>文件名</label>
        <input class="in" id="in-code-name" value="main.go" placeholder="main.go 或 pkg/util.go">
      </div>
      <div class="field">
        <label>代码内容</label>
        <textarea class="ta" id="ta-code" placeholder="package main&#10;&#10;var apiKey = &quot;...&quot;"></textarea>
      </div>
    </div>

    <div id="src-upload" style="display:none">
      <div class="field">
        <label>选择文件（可多选；.go 等文本文件，或 .zip 压缩包）</label>
        <input class="in" type="file" id="in-upload" multiple>
      </div>
    </div>

    <div id="src-pr" style="display:none">
      <div class="field">
        <label>GitHub PR 链接</label>
        <input class="in" id="in-pr" placeholder="https://github.com/owner/repo/pull/123 或 owner/repo#123">
      </div>
    </div>

    <div id="src-repo" style="display:none">
      <div class="field">
        <label>仓库路径</label>
        <input class="in" id="in-repo" placeholder="/path/to/repo">
      </div>
      <div class="field">
        <label><input type="checkbox" id="cb-sandbox" style="vertical-align:-2px"> 执行沙箱 ${help("在沙箱中对仓库执行 go vet / go test。命令先经权限策略检查，deny/ask 不会执行")}</label>
      </div>
    </div>

    <button class="btn" id="btn-run" onclick="submitReview()">执行审查</button>
    <button class="btn btn-ghost" onclick="document.getElementById('ta-diff').value='';log('输入已清空')">清空</button>

    <div id="review-result" style="margin-top:22px"></div>
  </div>`;
  log("就绪 — 粘贴 diff / 粘贴代码 / 上传文件 / GitHub PR / 仓库路径");
}

const SRC_IDS = ["diff", "code", "upload", "pr", "repo"];

function switchSource(src) {
  reviewSource = src;
  for (const s of SRC_IDS) {
    const tab = document.getElementById("tab-" + s);
    const box = document.getElementById("src-" + s);
    if (tab) tab.classList.toggle("on", s === src);
    if (box) box.style.display = s === src ? "" : "none";
  }
}

function loadSample(i) {
  const s = samplesCache[i];
  if (!s) return;
  switchSource("diff");
  document.getElementById("ta-diff").value = s.content;
  document.querySelectorAll(".chip").forEach(c => c.classList.toggle("on", +c.dataset.idx === i));
  log(`已载入示例 ${s.name}`);
}

async function submitReview() {
  const btn = document.getElementById("btn-run");
  let submit; // () => Promise<{task_id,status}>

  if (reviewSource === "diff") {
    const diffText = document.getElementById("ta-diff").value;
    if (!diffText.trim()) { log("diff 内容为空"); return; }
    submit = () => api("/api/reviews", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ diff_content: diffText }),
    });
  } else if (reviewSource === "code") {
    // M7-F3：粘贴整文件 → files_content
    const name = document.getElementById("in-code-name").value.trim() || "main.go";
    const content = document.getElementById("ta-code").value;
    if (!content.trim()) { log("代码内容为空"); return; }
    submit = () => api("/api/reviews", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ files_content: { [name]: content } }),
    });
  } else if (reviewSource === "upload") {
    // M7-F3：multipart 上传（多文件或 zip）
    const input = document.getElementById("in-upload");
    if (!input.files || !input.files.length) { log("请选择要上传的文件"); return; }
    const fd = new FormData();
    for (const f of input.files) fd.append("files", f, f.name);
    submit = () => api("/api/reviews/upload", { method: "POST", body: fd });
  } else if (reviewSource === "pr") {
    // M7-F3：GitHub PR 链接
    const prURL = document.getElementById("in-pr").value.trim();
    if (!prURL) { log("请填写 GitHub PR 链接"); return; }
    submit = () => api("/api/reviews", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ pr_url: prURL }),
    });
  } else {
    const repoPath = document.getElementById("in-repo").value.trim();
    const sandbox = document.getElementById("cb-sandbox").checked;
    if (!repoPath) { log("请填写仓库路径"); return; }
    submit = () => api("/api/reviews", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ repo_path: repoPath, sandbox }),
    });
  }

  btn.disabled = true;
  btn.innerHTML = `<span class="spin"></span> 审查中 …`;
  log("已提交，等待审查队列 …");
  const t0 = performance.now();

  try {
    // M7-F1：202 + task_id，结果轮询任务档案
    const res = await submit();
    // 立即清掉上一次的结果，避免新结果到达前旧内容残留
    document.getElementById("review-result").innerHTML =
      `<div class="notice progress" style="display:flex;align-items:center;gap:10px">
        <span class="spin"></span><span>排队中 … ${esc(res.task_id)}</span></div>`;
    await pollAndRender(res.task_id, t0);
  } catch (e) {
    document.getElementById("review-result").innerHTML =
      `<div class="notice">✗ ${esc(e.message)}</div>`;
    log(`审查失败 — ${esc(e.message)}`);
  } finally {
    btn.disabled = false;
    btn.innerHTML = "执行审查";
  }
}

/* 轮询任务档案直到 completed/failed（M7-F1 异步队列） */
const POLL_INTERVAL = 700;
const POLL_DEADLINE_MS = 15 * 60 * 1000; // 前端兜底上限，超时提示（服务端另有看门狗）
const STATUS_LABEL = { queued: "排队中", running: "审查中", completed: "完成", failed: "失败" };

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function pollAndRender(taskID, t0) {
  const box = document.getElementById("review-result");
  const deadline = Date.now() + POLL_DEADLINE_MS;
  let lastStatus = "";

  for (;;) {
    let d = null;
    try { d = await api(`/api/tasks/${encodeURIComponent(taskID)}`); }
    catch { /* 瞬态失败（如刚入队的可见性窗口）忽略，下一轮重试 */ }

    const st = d && d.task ? d.task.status : lastStatus;
    lastStatus = st;

    if (st === "completed" && d && d.report) {
      const ms = (performance.now() - t0).toFixed(0);
      renderReviewResult(d.report);
      log(`审查完成 ${taskID} — 风险 ${d.report.monitor.risk_score.toFixed(0)}/100 (${d.report.monitor.risk_grade}) · 耗时 ${ms}ms`);
      return;
    }
    if (st === "failed") {
      const msg = (d && d.task && d.task.error_msg) || "审查执行失败";
      box.innerHTML = `<div class="notice">✗ 任务 ${esc(taskID)} 失败：${esc(msg)}</div>`;
      log(`审查失败 — ${esc(msg)}`);
      return;
    }

    const elapsed = ((performance.now() - t0) / 1000).toFixed(1);
    box.innerHTML = `
      <div class="notice progress" style="display:flex;align-items:center;gap:10px">
        <span class="spin"></span>
        <span>${STATUS_LABEL[st] || st} … ${esc(taskID)} · 已等待 ${elapsed}s</span>
      </div>`;
    log(`${STATUS_LABEL[st] || st} — ${taskID}`);

    if (Date.now() > deadline) {
      box.innerHTML = `<div class="notice">✗ 等待超时（${POLL_DEADLINE_MS / 60000} 分钟），任务 ${esc(taskID)} 仍在执行，可稍后在任务记录中查看结果</div>`;
      log("等待超时，停止轮询");
      return;
    }
    await sleep(POLL_INTERVAL);
  }
}

function gaugeHTML(score, grade) {
  const filled = Math.round(score / 10);
  let segs = "";
  for (let i = 0; i < 10; i++) segs += `<i class="${i < filled ? "on" : ""}"></i>`;
  const color = score >= 80 ? "var(--red)" : score >= 60 ? "var(--orange)" :
    score >= 40 ? "var(--orange)" : score >= 20 ? "#65a30d" : "var(--green)";
  const mean = { A: "低风险", B: "较低", C: "注意", D: "高风险", F: "严重" }[grade] || "";
  return `
    <div class="gauge">
      <div class="gauge-num" style="color:${color}">${score.toFixed(0)}<small> /100</small></div>
      <div class="gauge-seg">${segs}</div>
      <div class="gauge-grade" style="color:${color}">${grade}<small>${mean}</small></div>
    </div>`;
}

function findingHTML(f, idx) {
  return `
  <div class="finding f-${esc(f.severity)}" id="fd-${idx}">
    <div class="finding-head" onclick="document.getElementById('fd-${idx}').classList.toggle('open')">
      ${badge(f.severity)}
      <span class="f-loc">${esc(f.file)}:${f.line}</span>
      <span class="f-title">${esc(f.title)}</span>
      <span class="f-conf">conf ${Number(f.confidence).toFixed(2)} · ${esc(f.rule_id)}</span>
      <span class="f-arrow">▶</span>
    </div>
    <div class="finding-body">
      <pre>${renderEvidence(f.evidence)}</pre>
      <div class="finding-rec"><b>修复建议</b> ${esc(f.recommendation)}</div>
    </div>
  </div>`;
}

function renderReviewResult(rep) {
  const m = rep.monitor;
  // Go 的空切片会序列化为 null，统一兜底为数组
  rep.findings = rep.findings || [];
  rep.warnings = rep.warnings || [];
  const bySev = rep.summary.by_severity || {};
  const sevSummary = Object.entries(SEV).map(([k, s]) =>
    `${s.label} <b style="color:${severityColor(k)}">${bySev[k] || 0}</b>`).join(" · ");
  const sandbox = rep.sandbox_summary || {};

  $view.querySelector("#review-result").innerHTML = `
  <div class="view-enter">
    ${panel("风险判定", `
      ${gaugeHTML(m.risk_score, m.risk_grade)}
      <div class="kv" style="margin-top:14px">
        <span class="k">任务 ID</span><span class="v mono">${esc(rep.task_id)} —
          <a class="link" href="#/task/${esc(rep.task_id)}">查看档案</a></span>
        <span class="k">扫描</span><span class="v">${rep.files_count} 个文件（Go ${rep.go_files_count}） · ${sevSummary} · 去重移除 ${rep.summary.dedup_removed}</span>
        <span class="k">耗时</span><span class="v">${esc(m.total_duration)}</span>
        ${sandbox.total_runs ? `<span class="k">沙箱</span><span class="v">${sandbox.successful}/${sandbox.total_runs} 通过 · ${esc(sandbox.total_duration)}</span>` : ""}
      </div>`, "评分 0-100，越高越危险；等级 A-F。证据中的密钥已自动脱敏")}

    <div style="height:14px"></div>

    ${panel(`发现（${rep.findings.length}）`, rep.findings.length ? rep.findings.map(findingHTML).join("") : `<div class="empty">未发现问题</div>`,
      "高置信度（≥0.7）的问题。点击行展开代码证据与修复建议")}

    ${rep.warnings.length ? `<div style="height:14px"></div>
    ${panel(`警告（${rep.warnings.length}）`, rep.warnings.map(findingHTML).join(""),
      "置信度低于 0.7 的疑似问题，需人工复核")}` : ""}
  </div>`;
}

/* ══════════════ 视图 03：任务记录 ══════════════ */
async function viewTasks() {
  log("加载任务列表 …");
  const data = await api("/api/tasks?limit=100");
  const rows = (data.tasks || []).map(t => `
    <tr class="rowlink" onclick="location.hash='#/task/${esc(t.task_id)}'">
      <td class="mono">${esc(t.task_id)}</td>
      <td class="dim">${fmtTime(t.started_at)}</td>
      <td><span class="badge info">${esc(t.input_type)}</span></td>
      <td class="dim">${esc(t.input_path)}</td>
      <td style="text-align:right">${t.files_count}</td>
      <td style="text-align:right">${t.go_files_count}</td>
      <td class="dim">${esc(t.status)}</td>
    </tr>`).join("") || `<tr><td colspan="7" class="empty">还没有任务</td></tr>`;

  $view.innerHTML = `
  <div class="view-enter">
    <h2 class="view-title">任务记录</h2>
    ${panel(`任务（${data.count}）`, `<table class="t">
      <thead><tr><th>TASK ID</th><th>时间</th><th>类型</th><th>输入</th>
        <th style="text-align:right">文件</th><th style="text-align:right">GO</th><th>状态</th></tr></thead>
      <tbody>${rows}</tbody></table>`)}
  </div>`;
  log(`任务列表就绪 — ${data.count} 条`);
}

/* ══════════════ 视图 04：任务详情 ══════════════ */
let taskSevFilter = "all";

async function viewTask(taskID) {
  log(`加载任务 ${taskID} …`);
  const d = await api(`/api/tasks/${encodeURIComponent(taskID)}`);

  // M7-F1：进行中/失败任务没有报告，展示状态卡
  if (!d.report) {
    const st = d.task ? d.task.status : "unknown";
    $view.innerHTML = `
    <div class="view-enter">
      <h2 class="view-title">任务档案</h2>
      <div class="kv" style="margin-bottom:16px">
        <span class="k">任务</span><span class="v mono">${esc(taskID)} · ${esc((d.task && d.task.input_type) || "")}</span>
      </div>
      ${st === "failed"
        ? `<div class="notice">✗ 任务失败：${esc((d.task && d.task.error_msg) || "未知原因")}</div>`
        : `<div class="notice progress" style="display:flex;align-items:center;gap:10px"><span class="spin"></span>
           <span>${STATUS_LABEL[st] || st} … 尚无报告，稍后刷新</span></div>`}
    </div>`;
    log(`${STATUS_LABEL[st] || st} — ${taskID}（无报告）`);
    return;
  }

  const rep = d.report;
  // Go 的空切片会序列化为 null，统一兜底为数组
  rep.findings = rep.findings || [];
  rep.warnings = rep.warnings || [];
  rep.summary.by_severity = rep.summary.by_severity || {};
  const m = rep.monitor;
  taskSevFilter = "all";
  window._currentTask = { d, rep };

  const sandboxRows = (d.sandbox_runs || []).map(r => `
    <tr><td class="mono dim">${esc(r.command)}</td><td>${esc(r.backend)}</td>
    <td>${r.exit_code === 0 ? '<span class="badge allow">exit 0</span>' : `<span class="badge deny">exit ${r.exit_code}</span>`}</td>
    <td class="dim">${esc(r.duration)}</td></tr>`).join("") ||
    `<tr><td colspan="4" class="empty">本任务未执行沙箱</td></tr>`;

  const permRows = (d.permission_decisions || []).map(p => `
    <tr><td class="mono dim">${esc(p.command)}</td>
    <td><span class="badge ${esc(p.action)}">${esc(p.action)}</span></td>
    <td class="dim">${esc(p.reason)}</td></tr>`).join("") ||
    `<tr><td colspan="3" class="empty">无权限决策记录</td></tr>`;

  const skillLine = rep.skill && rep.skill.loaded
    ? `<span class="k">Skill</span><span class="v mono">${esc(rep.skill.name)} ${esc(rep.skill.version)}</span>`
    : "";

  $view.innerHTML = `
  <div class="view-enter">
    <h2 class="view-title">任务档案</h2>
    <div class="kv" style="margin-bottom:16px">
      <span class="k">任务</span><span class="v mono">${esc(rep.task_id)} · ${esc(rep.input_type)} · ${esc(rep.input_path)} · ${fmtTime(rep.start_time)} → ${esc(rep.duration)} ·
        <a class="link" href="/api/tasks/${encodeURIComponent(rep.task_id)}/report">下载报告</a></span>
      ${skillLine}
    </div>

    ${panel("风险判定", gaugeHTML(m.risk_score, m.risk_grade),
      "评分 0-100，越高越危险；等级 A(0-20) B(20-40) C(40-60) D(60-80) F(80-100)")}

    <div style="height:14px"></div>

    ${panel("发现", `<div class="chips" id="sev-filter">
        <span class="chip on" onclick="filterFindings('all', this)">全部</span>
        ${Object.entries(SEV).map(([k, s]) =>
          `<span class="chip" onclick="filterFindings('${k}', this)">${s.label} (${rep.summary.by_severity[k] || 0})</span>`).join("")}
        ${rep.warnings.length ? `<span class="chip" onclick="filterFindings('warning', this)">警告 (${rep.warnings.length})</span>` : ""}
      </div>
      <div id="findings-wrap"></div>`,
      "点击行展开代码证据（密钥已脱敏）与修复建议", true)}

    <div style="height:14px"></div>

    <div class="grid grid-2">
      ${panel("沙箱执行", `<table class="t"><thead><tr><th>命令</th><th>后端</th><th>结果</th><th>耗时</th></tr></thead><tbody>${sandboxRows}</tbody></table>`,
        "仅仓库模式且未关闭沙箱时执行 go vet / go test")}
      ${panel("权限决策", `<table class="t"><thead><tr><th>命令</th><th>决策</th><th>原因</th></tr></thead><tbody>${permRows}</tbody></table>`,
        "allow=允许执行，deny=拒绝，ask=需人工确认；deny/ask 的命令不会进入沙箱")}
      ${panel("监控", `<div class="kv">
        <span class="k">工具调用</span><span class="v">${m.tool_call_count} 次</span>
        <span class="k">规则</span><span class="v">${m.rule_count} 条 · ${esc(m.rule_duration)}</span>
        <span class="k">扫描文件</span><span class="v">${m.files_scanned} 个</span>
        <span class="k">产物</span><span class="v">入库 ${m.artifacts_saved} · 被拒 ${m.artifacts_rejected}</span>
        <span class="k">权限拦截</span><span class="v">${m.permission_denied} 次 · 异常 ${m.exception_count} 次</span>
      </div>`)}
      ${panel("任务元数据", `<div class="kv">
        <span class="k">状态</span><span class="v">${esc(d.task.status)}</span>
        <span class="k">文件</span><span class="v">${d.task.files_count}（Go ${d.task.go_files_count}）</span>
        <span class="k">开始</span><span class="v">${fmtTime(d.task.started_at)}</span>
        <span class="k">结束</span><span class="v">${fmtTime(d.task.completed_at)}</span>
        <span class="k">结果</span><span class="v">${rep.summary.total_findings} 发现 + ${rep.summary.total_warnings} 警告 · 去重移除 ${rep.summary.dedup_removed}</span>
      </div>`)}
    </div>
  </div>`;

  renderFindings();
  log(`档案就绪 — ${rep.summary.total_findings} 发现 / ${rep.summary.total_warnings} 警告`);
}

function renderFindings() {
  const { rep } = window._currentTask;
  const wrap = document.getElementById("findings-wrap");
  if (!wrap) return;
  let list = [];
  if (taskSevFilter === "all") list = rep.findings;
  else if (taskSevFilter === "warning") list = rep.warnings;
  else list = rep.findings.filter(f => f.severity === taskSevFilter);

  wrap.innerHTML = list.length ? list.map(findingHTML).join("")
    : `<div class="empty">该筛选下没有条目</div>`;
}

function filterFindings(sev, el) {
  taskSevFilter = sev;
  document.querySelectorAll("#sev-filter .chip").forEach(c => c.classList.remove("on"));
  el.classList.add("on");
  renderFindings();
}

/* ══════════════ 视图 05：规则引擎 ══════════════ */
async function viewRules() {
  log("加载规则引擎 …");
  const data = await api("/api/rules");

  const ruleRows = (data.rules || []).map(r => `
    <tr>
      <td class="mono">${esc(r.id)}</td>
      <td>${esc(r.name)}</td>
      <td>${badge(r.severity)}</td>
      <td class="dim">${esc(CAT[r.category] || r.category)}</td>
      <td><span class="badge ${r.source === "builtin" ? "info" : "low"}">${r.source === "builtin" ? "内置" : "YAML"}</span></td>
    </tr>`).join("");

  const dimRows = (data.dimensions || []).map(d => hbar(d.name, `${Math.round(d.weight * 100)}%`, 30, "var(--violet)", true)).join("");

  $view.innerHTML = `
  <div class="view-enter">
    <h2 class="view-title">规则引擎</h2>

    <div class="grid grid-2">
      ${panel(`已注册规则（${data.rules.length}）`, `<table class="t">
        <thead><tr><th>RULE ID</th><th>名称</th><th>级别</th><th>分类</th><th>来源 ${help("内置 = Go 代码实现；YAML = 用户通过 --rules-dir 加载的自定义规则")}</th></tr></thead>
        <tbody>${ruleRows}</tbody></table>`)}
      <div>
        <div style="margin-bottom:14px">
        ${panel("评分维度", dimRows, "每类问题按维度计分后加权求和，得到 0-100 风险分")}
        </div>
        ${panel("审查管线", pipelineHTML())}
      </div>
    </div>
  </div>`;
  log(`规则引擎就绪 — ${data.rules.length} 条规则`);
}

/* ---------- 启动 ---------- */
initHealth();
route();
