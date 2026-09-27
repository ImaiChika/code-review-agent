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
  const res = await fetch(path, opts);
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

function pipelineHTML() {
  const steps = [
    ["STEP-1", "输入解析", "unified diff / git 工作区 / API 上传"],
    ["STEP-2", "规则装载", "6 条内置规则 + YAML 自定义规则"],
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
      <div class="tab" id="tab-repo" onclick="switchSource('repo')">仓库路径 ${help("填写服务器本机 git 仓库路径，取其未提交变更进行审查")}</div>
    </div>

    <div id="src-diff">
      ${samplesCache.length ? `<div class="chips">${chips}</div>` : ""}
      <div class="field">
        <label>DIFF 内容</label>
        <textarea class="ta" id="ta-diff" placeholder="--- a/creds.go&#10;+++ b/creds.go&#10;@@ -1,2 +1,4 @@&#10; package creds&#10;&#10;+var apiKey = &quot;sk-...&quot;"></textarea>
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
  log("就绪 — 粘贴 diff 或载入示例后执行");
}

function switchSource(src) {
  reviewSource = src;
  document.getElementById("tab-diff").classList.toggle("on", src === "diff");
  document.getElementById("tab-repo").classList.toggle("on", src === "repo");
  document.getElementById("src-diff").style.display = src === "diff" ? "" : "none";
  document.getElementById("src-repo").style.display = src === "repo" ? "" : "none";
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
  const body = {};
  if (reviewSource === "diff") {
    body.diff_content = document.getElementById("ta-diff").value;
    if (!body.diff_content.trim()) { log("diff 内容为空"); return; }
  } else {
    body.repo_path = document.getElementById("in-repo").value.trim();
    body.sandbox = document.getElementById("cb-sandbox").checked;
    if (!body.repo_path) { log("请填写仓库路径"); return; }
  }

  btn.disabled = true;
  btn.innerHTML = `<span class="spin"></span> 审查中 …`;
  log("审查管线执行中 …");
  const t0 = performance.now();

  try {
    const rep = await api("/api/reviews", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const ms = (performance.now() - t0).toFixed(0);
    renderReviewResult(rep);
    log(`审查完成 ${rep.task_id} — 风险 ${rep.monitor.risk_score.toFixed(0)}/100 (${rep.monitor.risk_grade}) · 耗时 ${ms}ms`);
  } catch (e) {
    document.getElementById("review-result").innerHTML =
      `<div class="notice">✗ ${esc(e.message)}</div>`;
    log(`审查失败 — ${esc(e.message)}`);
  } finally {
    btn.disabled = false;
    btn.innerHTML = "执行审查";
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
