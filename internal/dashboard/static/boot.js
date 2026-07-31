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

// 结果条目是 <article> 而不是 <button>（一条结果里有标题、片段、多行元信息，
// 塞进 button 语义和排版都不对）。于是这里补上键盘那一半：可聚焦 + 回车/空格触发。
// 只有鼠标能点的列表，等于键盘用户搜到了却打不开。
CD.clickable = (el, fn) => {
  el.tabIndex = 0;
  el.onclick = fn;
  el.onkeydown = (ev) => {
    if (ev.key === 'Enter' || ev.key === ' ') {
      ev.preventDefault();
      fn();
    }
  };
};

/* ---------------- 工具调用渲染 ---------------- */

/* tool_use 占全部内容块的 39.4%（274k / 696k），是回读时看得最多的一类，
 * 却一直按序列化 JSON 原样显示——转义引号满地、命令和说明挤一行。
 *
 * 实测正文形态（以 API 下发的文本为准，**不能读裸库**：库里存的是 CJK 单字切分
 * 后的形式，带 U+0001 分隔符，API 下发前会 Strip。照裸库判断会得出"31% 非法 JSON"
 * 这种错误结论）：
 *   合法 JSON 对象  91.2%   Bash / exec_command / Read / Edit / Write / 各 MCP 工具
 *   JS 源码          5.3%   exec（Codex 批量执行）
 *   patch 文本       3.5%   apply_patch
 *
 * 设计取舍：**一张映射表 + 三种骨架**，而不是每个工具一个渲染函数。
 * 前 15 个工具覆盖 88%，但尾巴很长（各种 MCP 工具）——逐个写既写不完，
 * 新工具出现时还会留下空白。改为声明"每个字段是什么角色",未知工具落通用键值表。
 */

// 字段角色：primary 主体 / sub 次要说明 / ctx 上下文 / diff 前后对照 /
//           body 大段内容(可折叠) / noise 不显示
// ⚠️ 只此一张表。新增工具改这里，渲染逻辑不动。
const TOOL_MAP = {
  Bash:          { shape: 'cmd',  primary: 'command',   sub: 'description', noise: ['timeout'] },
  exec_command:  { shape: 'cmd',  primary: 'cmd',       ctx: ['workdir'],
                   noise: ['max_output_tokens', 'yield_time_ms'] },
  write_stdin:   { shape: 'cmd',  primary: 'chars',     ctx: ['session_id'],
                   noise: ['max_output_tokens', 'yield_time_ms'] },
  Read:          { shape: 'file', primary: 'file_path', ctx: ['offset', 'limit'] },
  Write:         { shape: 'file', primary: 'file_path', body: 'content' },
  Edit:          { shape: 'file', primary: 'file_path', diff: ['old_string', 'new_string'],
                   ctx: ['replace_all'] },
  NotebookEdit:  { shape: 'file', primary: 'notebook_path', body: 'new_source' },
};

CD.toolCall = (name, body) => {
  const text = String(body ?? '');
  try {
    // patch 的特征串最明确，先判它
    if (text.startsWith('*** Begin Patch')) return renderPatch(text);

    let obj = null;
    try { obj = JSON.parse(text); } catch { /* 落到下面的非 JSON 分支 */ }

    if (obj && typeof obj === 'object' && !Array.isArray(obj)) {
      return renderShape(TOOL_MAP[name] || { shape: 'kv' }, obj);
    }
    // 不是 JSON 对象：exec 的 JS 源码走这条。不做语言探测——
    // "不是 JSON" 反推即可，猜语言只会猜错。
    return `<pre class="tc-code">${CD.esc(text)}</pre>`;
  } catch (e) {
    // 单条渲染失败不得影响同页其它消息（需求 Reliability）
    console.warn('工具调用渲染失败，退回原文', name, e);
    return `<pre>${CD.esc(text)}</pre>`;
  }
};

function renderShape(map, obj) {
  const used = new Set([map.primary, map.sub, map.body, ...(map.ctx || []), ...(map.diff || []),
                        ...(map.noise || [])].filter(Boolean));
  const rest = Object.keys(obj).filter((k) => !used.has(k) && obj[k] !== undefined);

  if (map.shape === 'cmd') return renderCmd(map, obj, rest);
  if (map.shape === 'file') return renderFile(map, obj, rest);
  return `<div class="tc">${kvList(obj, Object.keys(obj))}</div>`;
}

// 值可能是数字/布尔/对象，一律转成可读字符串再转义
function val(v) {
  if (v === null || v === undefined) return '';
  return typeof v === 'object' ? JSON.stringify(v) : String(v);
}

function kvList(obj, keys) {
  const rows = keys.filter((k) => val(obj[k]) !== '').map((k) => {
    const v = val(obj[k]);
    // 单个长参数会把整页顶开，超阈值折叠
    const body = v.length > 200
      ? `<details><summary>${v.length} 字符</summary><pre>${CD.esc(v)}</pre></details>`
      : `<span>${CD.esc(v)}</span>`;
    return `<dt>${CD.esc(k)}</dt><dd>${body}</dd>`;
  });
  return rows.length ? `<dl class="tc-kv">${rows.join('')}</dl>` : '';
}

/* 命令型（Bash / exec_command / write_stdin，合计 52.1%）。
 *
 * 要点是"复制出来能直接跑"：
 *   - 命令是 JSON 字符串解出来的，转义天然还原（\" → "），不用手工处理
 *   - 保留原始换行,多行命令不压成一行
 *   - $ 提示符用 ::before 画,**不进 DOM 文本**,选中复制不会带走它
 *   - 长行自己横滚,页面本身不横滚
 */
function renderCmd(map, obj, rest) {
  const cmd = val(obj[map.primary]);
  const parts = [];
  if (cmd) parts.push(`<div class="tc-cmd"><code>${CD.esc(cmd)}</code></div>`);
  const sub = map.sub ? val(obj[map.sub]) : '';
  if (sub) parts.push(`<div class="tc-sub">${CD.esc(sub)}</div>`);
  const ctx = (map.ctx || []).filter((k) => val(obj[k]) !== '')
    .map((k) => `<span><i>${CD.esc(k)}</i> ${CD.esc(val(obj[k]))}</span>`);
  if (ctx.length) parts.push(`<div class="tc-ctx">${ctx.join('')}</div>`);
  // 映射表没覆盖到的键仍然显示,不静默丢信息
  if (rest && rest.length) parts.push(kvList(obj, rest));
  return `<div class="tc">${parts.join('')}</div>`;
}
/* 文件型（Read / Write / Edit / NotebookEdit，合计 23.2%）。
 *
 * Edit 的 old_string / new_string 是两大段文本,平铺出来根本看不出改了什么,
 * 所以做成前后对照。Write 的 content 可能是整个文件,必须可折叠——
 * 不折叠的话一条 Write 就能把整页顶开。
 */
function renderFile(map, obj, rest) {
  const parts = [];
  const path = val(obj[map.primary]);
  if (path) parts.push(`<div class="tc-path">${CD.esc(path)}</div>`);

  const ctx = (map.ctx || []).filter((k) => val(obj[k]) !== '')
    .map((k) => `<span><i>${CD.esc(k)}</i> ${CD.esc(val(obj[k]))}</span>`);
  if (ctx.length) parts.push(`<div class="tc-ctx">${ctx.join('')}</div>`);

  if (map.diff) {
    const [ok, nk] = map.diff;
    const o = val(obj[ok]), n = val(obj[nk]);
    if (o || n) {
      parts.push(`<div class="tc-ba">
        <div class="tc-b"><span class="tc-tag">改前</span><pre>${CD.esc(o)}</pre></div>
        <div class="tc-a"><span class="tc-tag">改后</span><pre>${CD.esc(n)}</pre></div>
      </div>`);
    }
  }

  if (map.body) {
    const c = val(obj[map.body]);
    if (c) {
      // 短内容直接展开,长的折叠——阈值取一屏左右
      const long = c.length > 600;
      parts.push(long
        ? `<details class="tc-file"><summary>内容 ${c.length} 字符</summary><pre>${CD.esc(c)}</pre></details>`
        : `<pre class="tc-file-open">${CD.esc(c)}</pre>`);
    }
  }

  if (rest && rest.length) parts.push(kvList(obj, rest));
  return `<div class="tc">${parts.join('')}</div>`;
}
/* apply_patch（3.5%）。正文是 patch 文本,不是 JSON:
 *   *** Begin Patch / *** Add File: xxx / +新增行 / -删除行
 *
 * 逐行看首字符就够,**不引 diff 解析库**（需求 3.4）。
 * ⚠️ 先 CD.esc 整行、再包 span——顺序反了等于把 patch 内容里的任意 HTML
 *    注进页面。与 escHit / CD.ansi 同一条纪律。
 */
function renderPatch(text) {
  const rows = String(text).split('\n').map((line) => {
    const e = CD.esc(line);
    if (line.startsWith('*** ')) return `<span class="p-head">${e}</span>`;
    if (line.startsWith('+')) return `<span class="p-add">${e}</span>`;
    if (line.startsWith('-')) return `<span class="p-del">${e}</span>`;
    if (line.startsWith('@@')) return `<span class="p-hunk">${e}</span>`;
    return e;
  });
  return `<pre class="tc-patch">${rows.join('\n')}</pre>`;
}

/* ---------------- ANSI 转义序列 ---------------- */

/* 实测真实语料 673 286 块里只有 1 274 块（0.19%）含 ANSI 转义。
 * 这个比例不值得引入终端模拟器，也不值得引一个 MPL-2.0 的依赖——
 * 我们只要 SGR 颜色这一小块，三十行够了。
 *
 * ⚠️ 顺序：**先整体转义、再解析 SGR**。反了就等于把工具输出里的任意 HTML
 * 注进页面——与 escHit 同一条纪律。
 */
const SGR_FG = {
  30: 'black', 31: 'red', 32: 'green', 33: 'yellow',
  34: 'blue', 35: 'magenta', 36: 'cyan', 37: 'white',
  90: 'black', 91: 'red', 92: 'green', 93: 'yellow',
  94: 'blue', 95: 'magenta', 96: 'cyan', 97: 'white',
};
// CSI 序列：ESC [ 参数 结尾字母。m 是 SGR（我们处理），其余（光标移动、
// 清屏、清行…）静默丢弃——把它们当正文显示只会是乱码。
const CSI_RE = /\x1b\[([0-9;?]*)([A-Za-z])/g;

CD.ansi = (src) => {
  const text = String(src ?? '');
  if (!text.includes('\x1b[')) return CD.esc(text);  // 99.8% 的内容走这条捷径

  let out = '';
  let last = 0;
  let cls = null, bold = false;
  let openCls = null, openBold = false, isOpen = false;

  // 有文本要输出时才开 span：连着写 ESC[1m ESC[91m 的话，
  // 立即开会留下一个空的 <span class="a-b"></span>
  const flush = (upto) => {
    if (upto <= last) return;
    const chunk = CD.esc(text.slice(last, upto));
    if (isOpen && (openCls !== cls || openBold !== bold)) { out += '</span>'; isOpen = false; }
    if (!isOpen && (cls || bold)) {
      const names = [];
      if (cls) names.push('a-' + cls);
      if (bold) names.push('a-b');
      out += `<span class="${names.join(' ')}">`;
      isOpen = true; openCls = cls; openBold = bold;
    }
    out += chunk;
  };

  CSI_RE.lastIndex = 0;
  for (let m; (m = CSI_RE.exec(text)); ) {
    flush(m.index);
    last = m.index + m[0].length;
    if (m[2] !== 'm') continue;                       // 非 SGR：吃掉，不输出

    for (const raw of (m[1] || '0').split(';')) {
      const n = +raw || 0;
      if (n === 0 || n === 39) { cls = null; bold = false; }
      else if (n === 1) bold = true;
      else if (n === 22) bold = false;
      else if (SGR_FG[n]) cls = SGR_FG[n];
      // 背景色与其余属性忽略：终端底色搬到网页上只会和主题打架
    }
  }
  flush(text.length);
  if (isOpen) out += '</span>';   // 输出没重置就结束了也要收尾，不能吐出半个标签
  return out;
};

/* ---------------- Markdown 渲染 ---------------- */

/* 会话内容是**敌意输入**：里面有抓过的网页、cat 过的文件、工具吐出的任意字节。
 *
 * marked 单用是不安全的。实测它的裸输出：
 *   <script>alert(1)</script>            原样透传
 *   <img src=x onerror=alert(2)>         原样透传
 *   [x](javascript:alert(3))             变成 <a href="javascript:alert(3)">
 *   <iframe src=//evil></iframe>         原样透传
 * 所以消毒不是「加固」，是管线里不可省的一环。
 */
const MD_URI = /^(?:https?:|mailto:|[^a-z]|[a-z+.\-]+(?:[^a-z+.\-:]|$))/i;

CD.md = (src) => {
  // 散文里也会混进 ANSI——实测 140 个 user 块含转义序列，多是 Claude Code
  // 自己的界面文本（如 ESC[2mCompacted…ESC[22m）被一并存了下来。
  // 这类装饰在散文里没有意义，**剥掉**而不是上色：上色要先产出 HTML，
  // 而 HTML 再进 Markdown 解析器就乱套了。
  const text = String(src ?? '').replace(CSI_RE, '');
  if (!text.trim()) return '';
  try {
    const html = window.marked.parse(text, { breaks: true, gfm: true });
    return window.DOMPurify.sanitize(html, {
      // 收紧到 http/https/mailto：javascript: 与 data: 一律出局
      ALLOWED_URI_REGEXP: MD_URI,
      ADD_ATTR: ['target', 'rel'],
    });
  } catch (e) {
    // 宁可难看也不能吞内容（需求 Reliability）
    console.warn('Markdown 渲染失败，退回原文', e);
    return `<pre class="md-fallback">${CD.esc(text)}</pre>`;
  }
};

// 外链一律新窗口打开且切断 opener 引用。用钩子而不是渲染后正则替换——
// 对已消毒的 HTML 串做字符串替换是会打断标签的。
if (window.DOMPurify) {
  window.DOMPurify.addHook('afterSanitizeAttributes', (node) => {
    if (node.tagName === 'A' && node.getAttribute('href')) {
      node.setAttribute('target', '_blank');
      node.setAttribute('rel', 'noopener noreferrer');
    }
  });
}

// 模型的写法五花八门：「会话 12」「会话 id: 12」「会话 ID：12」「#12」都要能点
const SESS_RE = /(?:会话\s*(?:id)?\s*[:：]?\s*#?|#)(\d+)/gi;

/* 答案现在是 Markdown 渲染出来的 HTML，**不能再对 HTML 串做正则替换**——
 * 那会打断标签（比如把 <a href="...#12"> 里的 #12 也换掉）。
 * 改成消毒之后遍历文本节点，只在文本里替换，标签一概不碰。 */
CD.linkifySessions = (root) => {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const targets = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    // 已经在链接里的不再处理（免得套娃）；代码里的不处理（代码是字面量，
    // 里面的 #123 可能是路径片段或颜色值，不是会话号）
    if (n.parentElement.closest('a, code, pre')) continue;
    SESS_RE.lastIndex = 0;
    if (SESS_RE.test(n.nodeValue)) targets.push(n);
  }
  for (const node of targets) {
    const frag = document.createDocumentFragment();
    let last = 0;
    SESS_RE.lastIndex = 0;
    for (let m; (m = SESS_RE.exec(node.nodeValue)); ) {
      if (m.index > last) frag.append(node.nodeValue.slice(last, m.index));
      const a = document.createElement('a');
      a.href = '#';
      a.className = 'sess-link';
      a.dataset.id = m[1];
      a.textContent = m[0];
      frag.append(a);
      last = m.index + m[0].length;
    }
    if (last < node.nodeValue.length) frag.append(node.nodeValue.slice(last));
    node.replaceWith(frag);
  }
}

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

CD.view = 'search';

/* URL 是「在看什么」的唯一来源。
 *
 * 之前存在 localStorage 里，代价是：链接分享不出去、后退键直接退出应用、
 * 两个标签页会互相覆盖。localStorage 只留**偏好**（明暗、主题、左栏两态），
 * 位置状态一律走 URL。
 *
 * 用查询串而非路径（/session/17）：dashboard 的根处理器是 http.FileServer，
 * 未知路径实测返回 404，要支持路径式路由就得在服务端加 SPA 兜底。
 * 查询串则服务端一行都不用改。
 */
const ROUTE_KEYS = ['q', 'source', 'kind', 'tool', 'project', 'from', 'to'];

CD.route = {
  // location.search → {view, query, id, seq}
  read() {
    const p = new URLSearchParams(location.search);
    const query = {};
    for (const k of ROUTE_KEYS) query[k] = p.get(k) || '';
    const view = p.get('view') || '';
    return {
      // 认不出的视图名落到检索，而不是白屏
      view: CD.views[view] ? view : 'search',
      query,
      id: +p.get('id') || 0,
      seq: +p.get('seq') || 0,
    };
  },

  // 当前状态 → URL。空值不写进去：?view=search 比
  // ?view=search&q=&kind=&tool=&from=&to= 可读得多，也好分享。
  href(extra = {}) {
    const p = new URLSearchParams();
    p.set('view', CD.view);
    for (const k of ROUTE_KEYS) if (CD.query[k]) p.set(k, CD.query[k]);
    for (const [k, v] of Object.entries(extra)) if (v) p.set(k, v);
    return location.pathname + '?' + p.toString();
  },

  // replace=true 用于「频繁变化」的输入（如改过滤条），
  // 否则调五次过滤就要按五次后退才能退出去
  write(opts = {}) {
    const url = this.href(opts.extra);
    if (opts.replace) history.replaceState(null, '', url);
    else history.pushState(null, '', url);
  },

  // 把状态灌进全局并重挂视图。**不写历史**——它既服务于首次加载，
  // 也服务于 popstate，后者再 push 就会和浏览器自己的历史打架。
  //
  // 回读不是一个视图而是覆盖层，所以 URL 里是「底层视图 + id/seq」
  // （?view=digest&id=17）。这样「关掉回读回到哪」这件事由 URL 自己表达，
  // 不用另存一份 back 状态。
  apply(st) {
    Object.assign(CD.query, st.query);

    if (st.id) {
      // 回读覆盖层会接管 #view，底层视图**不要**渲染。
      // 实测过：两个异步加载会互相覆盖——检索的结果回来得晚，
      // 就把已经渲染好的回读内容顶掉了，页面看着像没打开回读。
      // 只设视图名（供关掉回读时知道该回哪）与左栏高亮，不挂内容。
      CD.view = CD.views[st.view] ? st.view : 'search';
      document.documentElement.dataset.view = CD.view;
      CD.renderSide();
      CD.syncFilterInputs();
      CD.openSession(st.id, st.seq, true);
      return;
    }

    CD.mountView(st.view);
    CD.syncFilterInputs();
  },
};

// 只换视图、不碰 URL。switchView 是「用户点了导航」，会写历史；
// mountView 是「按状态渲染」，谁写历史由调用方决定。
CD.mountView = (name) => {
  if (!CD.views[name]) name = 'search';
  CD.view = name;
  document.documentElement.dataset.view = name;
  CD.renderSide();
  const root = CD.$('view');
  root.innerHTML = '';
  // 过滤条只对检索/时间线/摘要有意义
  CD.$('filters').hidden = !['search', 'timeline', 'digest'].includes(name);
  CD.views[name].mount(root);
};

CD.switchView = (name) => {
  if (!CD.views[name]) return;
  CD.mountView(name);
  CD.route.write();
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
      CD.route.write();
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
      CD.route.write();
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
    // 回填而不是重新渲染：摘要在后台持续生成，每 10 秒重挂一次视图
    // 会把正在看的列表滚回顶部
    if (CD.summaryCount !== (s.summarized || 0)) {
      CD.summaryCount = s.summarized || 0;
      const c = CD.$('dg-count');
      if (c) c.textContent = CD.summaryCount;
      CD.renderSide();
    }
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
  // 提交检索是「去了别的地方」，后退键应该能回到上一次检索
  if (CD.view === 'chat' || CD.view === 'settings') CD.switchView('search');
  else { CD.route.write(); CD.refresh(); }
};

for (const id of ['source', 'kind', 'tool', 'from', 'to']) {
  CD.$(id).onchange = () => {
    CD.query[id] = CD.$(id).value;
    // 调过滤用 replace：连调五次不该要按五次后退才能退出去（需求 1.8）
    CD.route.write({ replace: true });
    CD.refresh();
  };
}
CD.$('filters-clear').onclick = () => {
  CD.query = { q: '', source: '', kind: '', tool: '', project: '', from: '', to: '' };
  CD.syncFilterInputs();
  CD.renderSide();
  CD.route.write({ replace: true });
  CD.refresh();
};

CD.boot = () => {
  CD.renderSide();
  // 从 URL 还原：直接访问一个带完整参数的链接（别人发来的/新标签页）
  // 要能重现同样的结果（需求 1.5）
  CD.route.apply(CD.route.read());
  loadStats();
  loadProjects();
  loadThemePick();
  setInterval(loadStats, 10000);
};

// 后退/前进：只还原，不写历史——popstate 里再 push 会和浏览器自己的历史打架
window.addEventListener('popstate', () => CD.route.apply(CD.route.read()));

// 自己挂而不是让 index.html 写 <script>CD.boot()</script>：
// 各视图脚本都是同步脚本，DOMContentLoaded 时必已注册完毕，
// 于是页面里能保持只有防闪烁那一段内联脚本（有测试守着这一点）。
document.addEventListener('DOMContentLoaded', CD.boot);
