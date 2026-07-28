// chatdex dashboard —— 只读界面，原生 JS，无构建链。
//
// 安全要点：会话正文是任意文本，里面完全可能有 <script>。
// 一切进 DOM 的内容都必须走 esc()；命中高亮用后端给的两个控制字符哨兵
// （\x02 / \x03），转义之后再换成 <mark>，绝不把原始 HTML 注进页面。

const HIT_OPEN = '\u0002';
const HIT_CLOSE = '\u0003';

const $ = (id) => document.getElementById(id);
const esc = (s) =>
  String(s ?? '').replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

// 先整体转义，再把哨兵还原成高亮标签
const escHit = (s) =>
  esc(s).split(HIT_OPEN).join('<mark>').split(HIT_CLOSE).join('</mark>');

const fmtTime = (unix) =>
  unix ? new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false }) : '—';

const fmtRange = (a, b) => {
  if (!a && !b) return '—';
  const d = (u) => new Date(u * 1000);
  const sameDay = a && b && d(a).toDateString() === d(b).toDateString();
  return sameDay ? `${fmtTime(a)} → ${d(b).toLocaleTimeString('zh-CN', { hour12: false })}`
                 : `${fmtTime(a)} → ${fmtTime(b)}`;
};

const fmtBytes = (n) => {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`;
};

const KIND_LABEL = {
  user: '我', assistant: '助手', tool_use: '工具调用',
  tool_result: '工具结果', summary: '摘要',
};

const api = async (path) => {
  const r = await fetch(path);
  if (!r.ok) throw new Error(`${path} → ${r.status}`);
  return r.json();
};

// ---------- 检索 ----------

function currentQuery(extra = {}) {
  const p = new URLSearchParams();
  const set = (k, v) => { if (v) p.set(k, v); };
  set('q', $('q').value.trim());
  set('source', $('source').value);
  set('kind', $('kind').value);
  set('tool', $('tool').value.trim());
  set('project', $('project').value);
  const day = (id, endOfDay) => {
    const v = $(id).value;
    if (!v) return '';
    const t = new Date(v + (endOfDay ? 'T23:59:59' : 'T00:00:00'));
    return Math.floor(t.getTime() / 1000);
  };
  set('from', day('from', false));
  set('to', day('to', true));
  for (const [k, v] of Object.entries(extra)) set(k, v);
  return p;
}

let lastResults = [];

async function doSearch(offset = 0) {
  const results = $('results');
  results.innerHTML = '<p class="hint">检索中…</p>';
  try {
    const p = currentQuery({ limit: 20, offset });
    const res = await api('/api/search?' + p);
    lastResults = res.sessions || [];
    renderResults(res, offset);
  } catch (e) {
    results.innerHTML = `<p class="err">检索失败：${esc(e.message)}</p>`;
  }
}

function renderResults(res, offset) {
  const box = $('results');
  if (!res.sessions || res.sessions.length === 0) {
    box.innerHTML = res.no_match
      ? '<p class="hint">无命中。换个说法试试——搜索只按关键词字面匹配，不会给近似结果。</p>'
      : '<p class="hint">没有符合条件的会话。</p>';
    return;
  }

  const rows = res.sessions.map((s) => `
    <article class="hit" data-id="${s.id}" data-seq="${s.best_seq || 0}">
      <div class="hit-top">
        <span class="badge ${esc(s.source)}">${s.source === 'codex' ? 'Codex' : 'Claude'}</span>
        ${s.agent_label ? `<span class="badge sub">子代理</span>` : ''}
        <span class="proj">${esc(s.project_path || '（未知项目）')}</span>
        <span class="time">${fmtRange(s.started_at, s.ended_at)}</span>
      </div>
      ${s.summary ? `<p class="summary">${esc(s.summary)}</p>` : ''}
      <p class="snippet">${escHit(s.snippet)}</p>
      <div class="hit-foot">
        <span>${s.msg_count} 条消息</span>
        ${s.hits ? `<span>命中 ${s.hits} 处</span>` : ''}
        ${s.best_kind ? `<span>最佳命中：${esc(KIND_LABEL[s.best_kind] || s.best_kind)}${s.best_tool ? '·' + esc(s.best_tool) : ''}</span>` : ''}
      </div>
    </article>`).join('');

  const more = res.sessions.length === 20
    ? `<button id="more">下一页</button>` : '';
  const prev = offset > 0 ? `<button id="prev">上一页</button>` : '';
  box.innerHTML = rows + `<div class="pager">${prev}${more}</div>`;

  box.querySelectorAll('.hit').forEach((el) =>
    el.onclick = () => openSession(+el.dataset.id, +el.dataset.seq));
  if ($('more')) $('more').onclick = () => doSearch(offset + 20);
  if ($('prev')) $('prev').onclick = () => doSearch(Math.max(0, offset - 20));
}

// ---------- 时间线 ----------
//
// 与搜索**并列**而非取代（需求 9.1）：过滤条件两边共用，切视图不用重设。

let view = 'search';

async function loadTimeline() {
  const box = $('timeline');
  box.innerHTML = '<p class="hint">载入中…</p>';
  try {
    const gs = await api('/api/timeline?' + currentQuery({ limit: 200 }));
    if (!gs.length) {
      box.innerHTML = '<p class="hint">没有符合条件的会话。</p>';
      return;
    }
    box.innerHTML = gs.map((g) => `
      <section class="group">
        <h2>${esc(g.project_path)}
          <span class="count">${g.total} 个会话${g.total > g.sessions.length ? `（展开 ${g.sessions.length}）` : ''}</span>
        </h2>
        ${g.sessions.map((s) => `
          <div class="tl" data-id="${s.id}">
            <span class="badge ${esc(s.source)}">${s.source === 'codex' ? 'Codex' : 'Claude'}</span>
            ${s.agent_label ? '<span class="badge sub">子代理</span>' : ''}
            <span class="time">${fmtRange(s.started_at, s.ended_at)}</span>
            <span class="msgs">${s.msg_count} 条</span>
            <span class="label${s.has_summary ? ' from-summary' : ''}">${esc(s.label || '（无内容）')}</span>
          </div>`).join('')}
      </section>`).join('');
    box.querySelectorAll('.tl').forEach((el) =>
      el.onclick = () => openSession(+el.dataset.id, 0));
  } catch (e) {
    box.innerHTML = `<p class="err">时间线载入失败：${esc(e.message)}</p>`;
  }
}

function switchView(next) {
  view = next;
  for (const [id, v] of [['tab-search', 'search'], ['tab-timeline', 'timeline'], ['tab-chat', 'chat']]) {
    $(id).classList.toggle('active', view === v);
  }
  $('search-form').hidden = view !== 'search';
  $('results').hidden = view !== 'search';
  $('timeline').hidden = view !== 'timeline';
  $('chat').hidden = view !== 'chat';
  // 过滤条只对检索与时间线有意义
  document.querySelector('.filters').hidden = view === 'chat';
  refreshCurrentView();
}

function refreshCurrentView() {
  if (view === 'search') doSearch(0);
  else if (view === 'timeline') loadTimeline();
}

// ---------- 聊天助手 ----------
//
// 需求 10.7：必须展示 LLM 实际执行了哪些检索——不展示的话，
// 用户无从判断它是搜错了方向，还是确实没有。

let chatReady = false;

async function loadChatStatus() {
  try {
    const s = await api('/api/chat/status');
    chatReady = !!s.available;
    $('chat-form').hidden = !chatReady;
    $('chat-unavailable').hidden = chatReady;
    $('tab-chat').classList.toggle('disabled', !chatReady);
    if (!chatReady) {
      $('chat-unavailable').textContent =
        `聊天不可用：${s.reason || '本地 LLM 未就绪'}。检索与时间线不受影响。`;
    }
  } catch {
    chatReady = false;
    $('chat-form').hidden = true;
    $('chat-unavailable').hidden = false;
    $('chat-unavailable').textContent = '聊天状态未知（服务未响应）。';
  }
}

const TOOL_LABEL = {
  search_sessions: '检索', get_session: '读会话', list_projects: '看项目列表',
};

function describeArgs(a = {}) {
  const parts = [];
  if (a.query) parts.push(`「${a.query}」`);
  if (a.kind) parts.push(`类型=${a.kind}`);
  if (a.tool_name) parts.push(`工具=${a.tool_name}`);
  if (a.source) parts.push(`来源=${a.source}`);
  if (a.project) parts.push(`项目=${a.project}`);
  if (a.session_id) parts.push(`会话 #${a.session_id}`);
  if (a.from_seq) parts.push(`从第 ${a.from_seq} 条`);
  return parts.join(' · ');
}

// 答案里出现的「会话 12」「#12」都变成可点的链接
function linkifySessions(text) {
  // 模型的写法五花八门：「会话 12」「会话 id: 12」「会话 ID：12」「#12」都要能点
  return esc(text).replace(/(?:会话\s*(?:id)?\s*[:：]?\s*#?|#)(\d+)/gi,
    (m, id) => `<a href="#" class="sess-link" data-id="${id}">${m}</a>`);
}

async function askChat(question) {
  const log = $('chat-log');
  const turn = document.createElement('div');
  turn.className = 'turn';
  turn.innerHTML = `<div class="q">${esc(question)}</div><div class="steps"></div><div class="a"></div>`;
  log.prepend(turn);
  const steps = turn.querySelector('.steps');
  const answer = turn.querySelector('.a');

  const resp = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ question }),
  });
  if (!resp.ok) {
    answer.innerHTML = `<span class="err">${esc(await resp.text())}</span>`;
    return;
  }

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  let lastStep = null;

  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const chunks = buf.split('\n\n');
    buf = chunks.pop();
    for (const c of chunks) {
      const line = c.split('\n').find((l) => l.startsWith('data: '));
      if (!line) continue;
      let e;
      try { e = JSON.parse(line.slice(6)); } catch { continue; }

      if (e.type === 'tool') {
        lastStep = document.createElement('div');
        lastStep.className = 'step';
        lastStep.innerHTML =
          `<span class="round">第 ${e.round} 轮</span> ${esc(TOOL_LABEL[e.tool] || e.tool)} ` +
          `<span class="args">${esc(describeArgs(e.args))}</span> <span class="hits">…</span>`;
        steps.append(lastStep);
      } else if (e.type === 'tool_result' && lastStep) {
        lastStep.querySelector('.hits').textContent =
          e.hits ? `→ ${e.hits} 条` : '→ 无命中';
      } else if (e.type === 'note') {
        const n = document.createElement('div');
        n.className = 'step note';
        n.textContent = e.text;
        steps.append(n);
      } else if (e.type === 'answer') {
        answer.innerHTML = linkifySessions(e.text || '（无内容）');
        answer.querySelectorAll('.sess-link').forEach((el) =>
          el.onclick = (ev) => { ev.preventDefault(); openSession(+el.dataset.id, 0); });
      }
    }
  }
}

// ---------- 会话回读 ----------

const PAGE = 100;
let cur = { id: 0, from: 0, total: 0, target: 0 };

async function openSession(id, targetSeq = 0) {
  // 命中位置在哪一页，就从哪一页开始加载
  const from = Math.max(0, Math.floor(targetSeq / PAGE) * PAGE);
  cur = { id, from, total: 0, target: targetSeq };
  $('search-pane').hidden = true;
  $('session-pane').hidden = false;
  await loadPage();
}

async function loadPage() {
  $('messages').innerHTML = '<p class="hint">载入中…</p>';
  const v = await api(`/api/session/${cur.id}?from=${cur.from}&limit=${PAGE}`);
  cur.total = v.total;

  $('session-meta').innerHTML = `
    <div class="proj">${esc(v.project_path || '（未知项目）')}</div>
    <div class="time">${fmtRange(v.started_at, v.ended_at)} · ${v.total} 条消息 ·
      ${v.source === 'codex' ? 'Codex' : 'Claude'}${v.agent_label ? ' · 子代理 ' + esc(v.agent_label) : ''}
      ${v.alive ? '' : ' · <span class="err">原始文件已不存在</span>'}</div>
    ${v.summary ? `<div class="summary">${esc(v.summary)}</div>` : ''}
    <div class="path" title="原始文件绝对路径">${esc(v.file_path)}</div>`;

  $('messages').innerHTML = (v.messages || []).map((m) => `
    <div class="msg ${esc(m.kind)}${m.seq === cur.target ? ' target' : ''}" id="seq-${m.seq}">
      <div class="msg-head">
        <span class="role">${esc(KIND_LABEL[m.kind] || m.kind)}</span>
        ${m.tool_name ? `<span class="tool">${esc(m.tool_name)}</span>` : ''}
        <span class="time">${fmtTime(m.ts)}</span>
        <span class="seq">#${m.seq}</span>
        ${m.truncated ? `<span class="trunc" title="原始 ${fmtBytes(m.raw_bytes)}，索引时已截断">已截断</span>` : ''}
      </div>
      <pre>${esc(m.body)}</pre>
    </div>`).join('') || '<p class="hint">这一页没有内容。</p>';

  const hasPrev = cur.from > 0;
  const hasNext = cur.from + PAGE < cur.total;
  $('pager').innerHTML =
    `${hasPrev ? '<button id="pprev">上一页</button>' : ''}
     <span>${cur.from + 1}–${Math.min(cur.from + PAGE, cur.total)} / ${cur.total}</span>
     ${hasNext ? '<button id="pnext">下一页</button>' : ''}`;
  if ($('pprev')) $('pprev').onclick = () => { cur.from -= PAGE; loadPage(); };
  if ($('pnext')) $('pnext').onclick = () => { cur.from += PAGE; loadPage(); };

  const t = document.getElementById('seq-' + cur.target);
  if (t) t.scrollIntoView({ block: 'center' });
}

// ---------- 启动 ----------

let statsTimer = null;

async function loadStats() {
  try {
    const s = await api('/api/stats');
    $('stats').textContent =
      `${s.sessions} 个会话 · ${s.blocks} 个内容块 · 索引库 ${fmtBytes(s.db_bytes)}`;
    renderSummaryBar(s);
  } catch { $('stats').textContent = '索引状态不可用'; }
}

// 摘要是跨夜任务（实测全量约 19 小时），进度与暂停/继续都要看得见摸得着。
function renderSummaryBar(s) {
  const bar = $('summary-bar');
  if (!s.llm_ready) {
    bar.hidden = false;
    $('summary-progress').textContent =
      `摘要未启用（本地 LLM 不可用）${s.summarized ? ` · 已有 ${s.summarized} 条` : ''}`;
    $('summary-toggle').hidden = true;
    return;
  }
  const p = s.summary || {};
  const total = (p.done || 0) + (p.pending || 0) + (p.running || 0) + (p.failed || 0);
  bar.hidden = false;
  $('summary-progress').textContent = total
    ? `摘要 ${p.done || 0}/${total}` +
      (p.pending ? ` · 待处理 ${p.pending}` : '') +
      (p.failed ? ` · 失败 ${p.failed}` : '') +
      (s.summary_paused ? ' · 已暂停' : '')
    : '摘要队列为空';
  const btn = $('summary-toggle');
  btn.hidden = false;
  btn.textContent = s.summary_paused ? '继续' : '暂停';
  btn.onclick = async () => {
    btn.disabled = true;
    try {
      await fetch('/api/summary/' + (s.summary_paused ? 'resume' : 'pause'), { method: 'POST' });
      await loadStats();
    } finally { btn.disabled = false; }
  };
}

async function loadProjects() {
  try {
    const ps = await api('/api/projects');
    for (const p of ps || []) {
      const o = document.createElement('option');
      o.value = p.project_path;
      o.textContent = `${p.project_path} (${p.sessions})`;
      $('project').appendChild(o);
    }
  } catch { /* 项目列表拿不到不影响检索 */ }
}

$('search-form').onsubmit = (e) => { e.preventDefault(); doSearch(0); };
// 过滤条件两个视图共用（需求 9.4）
['source', 'kind', 'project', 'from', 'to'].forEach((id) =>
  $(id).onchange = () => refreshCurrentView());
$('tab-search').onclick = () => switchView('search');
$('tab-timeline').onclick = () => switchView('timeline');
$('tab-chat').onclick = () => switchView('chat');
$('chat-form').onsubmit = async (e) => {
  e.preventDefault();
  const q = $('chat-q').value.trim();
  if (!q) return;
  $('chat-q').value = '';
  $('chat-send').disabled = true;
  try { await askChat(q); } finally { $('chat-send').disabled = false; }
};
loadChatStatus();
$('timeline').hidden = true;
$('back').onclick = () => {
  $('session-pane').hidden = true;
  $('search-pane').hidden = false;
};

loadStats();
// 摘要在后台持续推进，进度定期刷新（轮询即可，不值得为它上 WebSocket）
statsTimer = setInterval(loadStats, 10000);
loadProjects();
doSearch(0);
