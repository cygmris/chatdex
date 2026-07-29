/* chatdex 前端骨架：主题、左栏、视图路由、共用工具。
 *
 * 视图各自一个文件，通过 CD.register(name, {mount, refresh}) 挂进来。
 * 这里不引任何框架——需求写死了「无构建链」，多几个 <script src> 就够了。
 */

const CD = (window.CD = {});

/* ---------------- 共用工具 ---------------- */

CD.$ = (id) => document.getElementById(id);

// 会话正文是任意文本，里面完全可能有 <script>：一切进 DOM 的内容都先过这里
CD.esc = (s) =>
  String(s ?? '').replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

// 后端用两个控制字符标记命中位置。**先整体转义、再换成 <mark>**——
// 顺序反了就等于把历史会话里的任意 HTML 注进页面。
CD.HIT_OPEN = '\u0002';
CD.HIT_CLOSE = '\u0003';
CD.escHit = (s) =>
  CD.esc(s).split(CD.HIT_OPEN).join('<mark>').split(CD.HIT_CLOSE).join('</mark>');

CD.fmtTime = (u) =>
  u ? new Date(u * 1000).toLocaleString('zh-CN', { hour12: false }) : '—';

CD.fmtRange = (a, b) => {
  if (!a && !b) return '—';
  const d = (u) => new Date(u * 1000);
  const sameDay = a && b && d(a).toDateString() === d(b).toDateString();
  return sameDay
    ? `${CD.fmtTime(a)} → ${d(b).toLocaleTimeString('zh-CN', { hour12: false })}`
    : `${CD.fmtTime(a)} → ${CD.fmtTime(b)}`;
};

CD.fmtBytes = (n) => {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`;
};

CD.KIND_LABEL = {
  user: '我', assistant: '助手', tool_use: '工具调用',
  tool_result: '工具结果', summary: '摘要',
};

CD.api = async (path, opts) => {
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error(`${path} → ${r.status}`);
  return r.json();
};

/* ---------------- 主题 ---------------- */
/*
 * 两层，别混为一谈：
 *   mode  = light | dark | auto   —— 顶栏按钮切的是这个
 *   pick  = {light: 'desk'|'paper', dark: 'editor'|'term'} —— 设置页选的是这个
 * 「跟随系统」不是第五套主题，是 mode 的一个取值。
 */

const MODES = ['light', 'dark', 'auto'];
const MODE_LABEL = { light: '☀ 亮', dark: '◑ 暗', auto: '◐ 跟随' };
const VALID = { light: ['desk', 'paper'], dark: ['editor', 'term'] };

CD.theme = {
  mode: localStorage.getItem('chatdex.mode') || 'auto',
  pick: readPick(),

  prefersDark: () => window.matchMedia('(prefers-color-scheme: dark)').matches,

  isDark() {
    return this.mode === 'dark' || (this.mode === 'auto' && this.prefersDark());
  },

  // 当前该用哪套主题；配置里写了不存在的名字就回退到默认，不白屏
  resolve() {
    const dark = this.isDark();
    const want = dark ? this.pick.dark : this.pick.light;
    const allowed = dark ? VALID.dark : VALID.light;
    if (allowed.includes(want)) return want;
    if (want) console.warn(`未知主题 ${want}，回退到默认`);
    return dark ? 'editor' : 'desk';
  },

  apply() {
    const d = document.documentElement;
    d.dataset.mode = this.mode;
    d.dataset.theme = this.resolve();
    const btn = CD.$('theme-btn');
    if (btn) btn.textContent = MODE_LABEL[this.mode];
    localStorage.setItem('chatdex.mode', this.mode);
    localStorage.setItem('chatdex.themes', JSON.stringify(this.pick));
  },

  cycle() {
    this.mode = MODES[(MODES.indexOf(this.mode) + 1) % MODES.length];
    this.apply();
  },

  // 设置页保存主题指派后调用
  setPick(pick) {
    this.pick = { ...this.pick, ...pick };
    this.apply();
  },
};

function readPick() {
  try {
    const p = JSON.parse(localStorage.getItem('chatdex.themes') || '{}');
    return { light: p.light || 'desk', dark: p.dark || 'editor' };
  } catch {
    return { light: 'desk', dark: 'editor' };
  }
}

// 跟随系统必须是实时的：系统偏好变了，页面要跟着变（需求 1.3）
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
  if (CD.theme.mode === 'auto') CD.theme.apply();
});

/* ---------------- 过滤条件（三个视图共用一份） ---------------- */

CD.query = { q: '', source: '', kind: '', tool: '', project: '', from: '', to: '' };

CD.queryParams = (extra = {}) => {
  const p = new URLSearchParams();
  const set = (k, v) => { if (v) p.set(k, v); };
  const q = CD.query;
  set('q', q.q); set('source', q.source); set('kind', q.kind);
  set('tool', q.tool); set('project', q.project);
  const day = (v, endOfDay) => {
    if (!v) return '';
    return Math.floor(new Date(v + (endOfDay ? 'T23:59:59' : 'T00:00:00')).getTime() / 1000);
  };
  set('from', day(q.from, false));
  set('to', day(q.to, true));
  for (const [k, v] of Object.entries(extra)) set(k, v);
  return p;
};

// 把 CD.query 同步到表单控件上（左栏点项目/时间后要反映到顶部）
CD.syncFilterInputs = () => {
  for (const id of ['source', 'kind', 'tool', 'from', 'to']) {
    const el = CD.$(id);
    if (el) el.value = CD.query[id] || '';
  }
  const qi = CD.$('q');
  if (qi) qi.value = CD.query.q || '';
};

/* ---------------- 视图路由 ---------------- */

CD.views = {};
CD.register = (name, view) => { CD.views[name] = view; };

CD.view = localStorage.getItem('chatdex.view') || 'search';

CD.switchView = (name) => {
  if (!CD.views[name]) return;
  CD.view = name;
  localStorage.setItem('chatdex.view', name);
  document.documentElement.dataset.view = name;
  CD.renderSide();
  const root = CD.$('view');
  root.innerHTML = '';
  // 过滤条只对检索/时间线/摘要有意义
  CD.$('filters').hidden = !['search', 'timeline', 'digest'].includes(name);
  CD.views[name].mount(root);
};

CD.refresh = () => CD.views[CD.view]?.refresh?.();

/* ---------------- 左栏 ---------------- */

const NAV = [
  { id: 'search', label: '检索', mini: '检' },
  { id: 'timeline', label: '时间线', mini: '时' },
  { id: 'digest', label: '摘要', mini: '摘' },
  { id: 'chat', label: '问一问', mini: '问' },
  { id: 'settings', label: '设置', mini: '设' },
];

CD.sidebar = {
  mini: localStorage.getItem('chatdex.sidebar') === 'mini',
  toggle() {
    this.mini = !this.mini;
    localStorage.setItem('chatdex.sidebar', this.mini ? 'mini' : 'wide');
    document.documentElement.dataset.sidebar = this.mini ? 'mini' : 'wide';
    CD.renderSide();
  },
};

CD.projects = [];
CD.summaryCount = 0;

CD.renderSide = () => {
  const side = CD.$('side');
  const mini = CD.sidebar.mini;

  if (mini) {
    // 46px 下自画一套图标很难不像贴图；中文单字在 12px 仍可辨且零歧义
    side.innerHTML =
      `<button class="side-tg" type="button" title="展开左栏">»</button>` +
      NAV.map((n) =>
        `<button class="side-mini${CD.view === n.id ? ' on' : ''}" data-view="${n.id}" title="${n.label}">${n.mini}</button>`
      ).join('');
  } else {
    const proj = CD.projects.slice(0, 8).map((p) => {
      const short = p.project_path.replace(/^\/home\/[^/]+\/(workflow|workspace)\//, '');
      return `<button class="side-nav${CD.query.project === p.project_path ? ' on' : ''}" data-project="${CD.esc(p.project_path)}" title="${CD.esc(p.project_path)}">
        <span class="t">${CD.esc(short)}</span><span class="n">${p.sessions}</span></button>`;
    }).join('');

    side.innerHTML = `
      <div class="side-head">
        <span class="brand">chatdex</span>
        <button class="side-tg" type="button" title="收起左栏">«</button>
      </div>
      <div class="side-sect">视图</div>
      ${NAV.map((n) => `<button class="side-nav${CD.view === n.id ? ' on' : ''}" data-view="${n.id}">
          <span class="t">${n.label}</span>${n.id === 'digest' && CD.summaryCount ? `<span class="n">${CD.summaryCount}</span>` : ''}
        </button>`).join('')}
      <div class="side-sect">项目</div>
      ${proj || '<div class="side-empty">（还没有项目）</div>'}
      <div class="side-sect">时间</div>
      <button class="side-nav" data-days="1"><span class="t">今天</span></button>
      <button class="side-nav" data-days="7"><span class="t">最近 7 天</span></button>
      ${CD.query.project || CD.query.from ? '<button class="side-nav clear" data-clear="1"><span class="t">清除项目/时间</span></button>' : ''}`;
  }

  side.querySelectorAll('.side-tg').forEach((el) => (el.onclick = () => CD.sidebar.toggle()));
  side.querySelectorAll('[data-view]').forEach((el) =>
    (el.onclick = () => CD.switchView(el.dataset.view)));
  side.querySelectorAll('[data-project]').forEach((el) =>
    (el.onclick = () => {
      // 左栏与顶部过滤条共用同一份 query，不各存一份
      CD.query.project = CD.query.project === el.dataset.project ? '' : el.dataset.project;
      CD.syncFilterInputs();
      CD.renderSide();
      CD.refresh();
    }));
  side.querySelectorAll('[data-days]').forEach((el) =>
    (el.onclick = () => {
      const d = new Date();
      d.setDate(d.getDate() - (+el.dataset.days - 1));
      CD.query.from = d.toISOString().slice(0, 10);
      CD.query.to = '';
      CD.syncFilterInputs();
      CD.renderSide();
      CD.refresh();
    }));
  side.querySelectorAll('[data-clear]').forEach((el) =>
    (el.onclick = () => {
      CD.query.project = '';
      CD.query.from = '';
      CD.query.to = '';
      CD.syncFilterInputs();
      CD.renderSide();
      CD.refresh();
    }));
};

/* ---------------- 顶栏状态与摘要进度 ---------------- */

async function loadStats() {
  try {
    const s = await CD.api('/api/stats');
    CD.$('stats').textContent =
      `${s.sessions} 会话 · ${s.blocks} 块 · ${CD.fmtBytes(s.db_bytes)}`;
    CD.summaryCount = s.summarized || 0;
    renderSummaryBar(s);
  } catch {
    CD.$('stats').textContent = '索引状态不可用';
  }
}

function renderSummaryBar(s) {
  const bar = CD.$('summary-bar');
  const btn = CD.$('summary-toggle');
  if (!s.llm_ready) {
    bar.hidden = false;
    CD.$('summary-progress').textContent = '摘要未启用';
    btn.hidden = true;
    return;
  }
  const p = s.summary || {};
  const total = (p.done || 0) + (p.pending || 0) + (p.running || 0) + (p.failed || 0);
  const pending = (p.pending || 0) + (p.running || 0);
  // 跑完了就别一直占着顶栏
  bar.hidden = !pending && !p.failed;
  if (bar.hidden) return;
  CD.$('summary-progress').textContent =
    `摘要 ${p.done || 0}/${total}` + (p.failed ? ` · 失败 ${p.failed}` : '') +
    (s.summary_paused ? ' · 已暂停' : '');
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
    CD.projects = (await CD.api('/api/projects')) || [];
    CD.renderSide();
  } catch { /* 项目列表拿不到不影响检索 */ }
}

// 服务端的主题指派可能与本地镜像不一致（比如在别的浏览器改过），回来后校正
async function loadThemePick() {
  try {
    const cfg = await CD.api('/api/config');
    const ui = cfg?.values?.ui;
    if (ui) CD.theme.setPick({ light: ui.light_theme, dark: ui.dark_theme });
  } catch { /* 端点还没上线（任务 10 之前）或取不到，用本地镜像 */ }
}

/* ---------------- 启动 ---------------- */

CD.theme.apply();
document.documentElement.dataset.sidebar = CD.sidebar.mini ? 'mini' : 'wide';

CD.$('theme-btn').onclick = () => CD.theme.cycle();

CD.$('search-form').onsubmit = (e) => {
  e.preventDefault();
  CD.query.q = CD.$('q').value.trim();
  if (CD.view === 'chat' || CD.view === 'settings') CD.switchView('search');
  else CD.refresh();
};

for (const id of ['source', 'kind', 'tool', 'from', 'to']) {
  CD.$(id).onchange = () => {
    CD.query[id] = CD.$(id).value;
    CD.refresh();
  };
}
CD.$('filters-clear').onclick = () => {
  CD.query = { q: '', source: '', kind: '', tool: '', project: '', from: '', to: '' };
  CD.syncFilterInputs();
  CD.renderSide();
  CD.refresh();
};

CD.boot = () => {
  CD.renderSide();
  CD.switchView(CD.views[CD.view] ? CD.view : 'search');
  loadStats();
  loadProjects();
  loadThemePick();
  setInterval(loadStats, 10000);
};
