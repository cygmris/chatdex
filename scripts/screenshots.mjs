#!/usr/bin/env node
/**
 * scripts/screenshots.mjs — 从演示语料重拍 README 的全部截图（docs/images/*.png）。
 *
 * 用法：
 *   node scripts/screenshots.mjs --home <演示 HOME> [--cdp http://127.0.0.1:9223] [--only a.png,b.png]
 *
 * 前提（缺一不拍，启动时逐条报）：
 *   - node ≥ 22（内置 fetch / WebSocket）、go、sqlite3、restic 在 PATH 上
 *   - 一个已经开着 --remote-debugging-port 的 Chrome；本脚本只连不启，开自己的 tab
 *   - <HOME> 由 scripts/gen-demo-corpus.py 生成（带 .chatdex-demo-corpus 标记），
 *     已 `HOME=<HOME> chatdex index` 过且摘要已生成；其 .config/chatdex/config.json 配了端口与 restic 仓库
 *   - 黑名单文件 $CHATDEX_SANITIZE_LIST（默认 ~/.config/chatdex-sanitize/patterns.txt），与公开仓 pre-commit 共用
 *
 * 每张图落盘前扫一遍画面上的文字（黑名单 + 当前用户名 / 主机名 / HOME），命中即中止；
 * 落盘后解析 PNG 块，带文本块（tEXt / iTXt / zTXt）也中止。
 * 三张图要临时造状态（摘要失败、会话文件消失），都在 finally 里还原并用数字核对。
 *
 * 重造语料（需要本地 Ollama 生成摘要）：
 *   python3 scripts/gen-demo-corpus.py <HOME>
 *   写 <HOME>/.config/chatdex/config.json（ports.ui/api、backup.repo、backup.password_file、backup.sources）
 *   HOME=<HOME> chatdex index && HOME=<HOME> chatdex serve     # 等「生成进度」页全部完成再拍
 *
 * 本文件随整树进公开仓：不写任何真实用户名 / 主机名 / 邮箱 / 本机目录默认值。
 */
import { spawn, spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, unlinkSync, utimesSync, writeFileSync } from 'node:fs';
import { homedir, hostname, tmpdir, userInfo } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

/* ======================= 纯函数（可 import，无副作用） ======================= */

export const DEMO_MARKER = '.chatdex-demo-corpus';
export const TEXT_CHUNKS = ['tEXt', 'iTXt', 'zTXt'];

/** 黑名单文本 → RegExp[]。`#` 行与空行跳过；空列表或任一行编译失败都 throw——「命令坏了」不能与「干净」同形。 */
export function compilePatterns(text) {
  const out = [];
  text.split('\n').forEach((raw, i) => {
    const line = raw.trim();
    if (!line || line.startsWith('#')) return;
    try { out.push(new RegExp(line, 'i')); }
    catch (e) { throw new Error(`黑名单第 ${i + 1} 行编译失败：${e.message}`); }
  });
  if (!out.length) throw new Error('黑名单里没有任何模式');
  return out;
}

const escRe = (s) => s.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&');

/** 运行时推导的身份串：HOME 与主机名是字面量，用户名加字母数字边界（防 reasoning 这类子串误伤）。 */
export function identityPatterns({ user, host, home }) {
  const out = [];
  if (home) out.push(new RegExp(escRe(home), 'i'));
  if (user) out.push(new RegExp(`(?<![a-z0-9])${escRe(user)}(?![a-z0-9])`, 'i'));
  if (host) out.push(new RegExp(escRe(host), 'i'));
  return out;
}

/** 每个命中的模式一条 {index, sample}，按模式序号排。 */
export function forbiddenHits(text, patterns) {
  const hits = [];
  patterns.forEach((re, index) => {
    const m = re.exec(text);
    if (m) hits.push({ index, sample: m[0].slice(0, 80) });
  });
  return hits;
}

/** 列出 PNG 的块类型（不校验 CRC）。不是 PNG 就 throw。 */
export function pngChunkTypes(buf) {
  const sig = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
  if (buf.length < 8 || sig.some((b, i) => buf[i] !== b)) throw new Error('不是 PNG');
  const types = [];
  let off = 8;
  while (off + 8 <= buf.length) {
    const len = buf.readUInt32BE(off);
    const type = buf.toString('latin1', off + 4, off + 8);
    types.push(type);
    off += 12 + len;
    if (type === 'IEND') break;
  }
  return types;
}

export function assertDemoHome(dir) {
  if (!existsSync(join(dir, DEMO_MARKER))) {
    throw new Error(`${dir} 没有 ${DEMO_MARKER} 标记，拒绝当作演示 HOME（它会被删文件、改索引库）`);
  }
  return dir;
}

const HITS = "document.querySelectorAll('article.hit').length > 0";
const TARGET = (seq) => `(() => { const el = document.getElementById('seq-${seq}'); if (!el) return false;` +
  ` const r = el.getBoundingClientRect(); return r.top >= 0 && r.top < innerHeight; })()`;
const CHAT_Q = '限流为什么选令牌桶而不是滑动窗口?';

/**
 * 14 张图。`params` 里以 `$` 开头的值在运行时换成查出来的会话 id（见 resolveIds）。
 * `ready` 是页面里求值的表达式；`act` 在就绪后、截图前执行；`sidebar` 只有 mini 那张用。
 * `state` 指明需要哪个状态包装。
 */
export const SHOTS = [
  { file: 'search.png', view: 'search', params: { q: '限流' }, ready: HITS, note: 'q=限流' },
  { file: 'search-filters.png', view: 'search', params: { q: 'go test', kind: 'tool_use', tool: 'Bash' }, ready: HITS,
    note: 'q=go test · 仅工具调用 · 工具=Bash' },
  { file: 'sidebar-mini.png', view: 'search', params: { q: '限流' }, ready: HITS + " && document.documentElement.dataset.sidebar === 'mini'",
    sidebar: 'mini', note: 'q=限流，左栏收起' },
  { file: 'timeline.png', view: 'timeline', params: {}, ready: "document.querySelectorAll('section.group').length > 0", note: '时间线首页' },
  { file: 'digest.png', view: 'digest', params: {}, ready: HITS, note: '摘要列表首页' },
  { file: 'settings.png', view: 'settings', params: {}, ready: "document.querySelectorAll('[data-key]').length > 0", note: '设置页' },
  { file: 'reader.png', view: 'search', params: { q: '限流', id: '$bucket', seq: 5 }, ready: TARGET(5),
    note: '回读：令牌桶会话定位到 #5（Write bucket.go）' },
  { file: 'reader-highlight.png', view: 'search', params: { q: '限流', id: '$bucket', seq: 17 }, ready: TARGET(17),
    act: 'mermaid', note: '回读：#17 的 mermaid 已渲染' },
  { file: 'output-highlight.png', view: 'search', params: { q: 'highlightSnippet', id: '$snippet', seq: 9 }, ready: TARGET(9),
    note: '回读：sed 读文件的输出按 Go 高亮' },
  { file: 'subagents.png', view: 'search', params: { q: '评估', id: '$eval' }, ready: "!!document.getElementById('rd-kids')",
    act: 'kids', note: '回读：主会话展开 5 个子代理' },
  { file: 'chat.png', view: 'chat', params: { project: '/home/dev/work/api-gateway' },
    ready: "!!document.getElementById('chat-q') && !document.getElementById('chat-send').disabled",
    act: 'ask', timeout: 180000, note: `问一问：范围 api-gateway，「${CHAT_Q}」` },
  { file: 'progress.png', view: 'progress', params: {}, ready: "document.body.innerText.includes('失败 2 条')",
    state: 'failedQueue', note: '生成进度：两条失败（临时造的）' },
  { file: 'backup.png', view: 'backup', params: {}, ready: "!!document.getElementById('bk-cov')", act: 'coverage',
    state: 'deletedSessions', note: '备份：两个会话源没了但备份里有（临时造的）' },
  { file: 'archived.png', view: 'search', params: { q: 'certbot', id: '$certbot' },
    ready: "(document.getElementById('rd-arch') || {}).textContent === '从备份读原件'", act: 'archived',
    state: 'deletedSessions', note: '回读：源文件已删，从备份读原件（临时造的）' },
];

/* ======================= 以下是驱动，只在直接运行时执行 ======================= */

const REPO = dirname(dirname(fileURLToPath(import.meta.url)));
const log = (...a) => console.error(new Date().toISOString().slice(11, 19), ...a);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function parseArgs(argv) {
  const o = { cdp: 'http://127.0.0.1:9223', only: null, home: '' };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--home') o.home = argv[++i];
    else if (a === '--cdp') o.cdp = argv[++i];
    else if (a === '--only') o.only = new Set(argv[++i].split(','));
    else throw new Error(`不认识的参数 ${a}`);
  }
  if (!o.home) throw new Error('缺 --home <演示 HOME>');
  return o;
}

function sh(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: 'utf8', ...opts });
  if (r.error) throw r.error;
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(' ')} 退出 ${r.status}\n${r.stderr}`);
  return r.stdout;
}

/* ---------- CDP（抄自 claude-browser skill 的 cdp-drive.mjs，含它踩过的坑） ---------- */

let seq = 0;
const DEBUG = !!process.env.SHOTS_DEBUG;
function send(s, method, params = {}) {
  const id = ++seq;
  const t0 = Date.now();
  return new Promise((res, rej) => {
    const to = setTimeout(() => rej(new Error(`${method} 超时(30s)`)), 30000);
    const h = (ev) => {
      const m = JSON.parse(ev.data);
      if (m.id !== id) return;
      clearTimeout(to);
      s.ws.removeEventListener('message', h);
      if (DEBUG) log(`  cdp ${method} ${Date.now() - t0} ms${m.error ? ' ERR ' + m.error.message : ''}`);
      m.error ? rej(new Error(`${method}: ${m.error.message}`)) : res(m.result);
    };
    s.ws.addEventListener('message', h);
    s.ws.send(JSON.stringify({ id, method, params }));
  });
}

async function openTab(base, width, height) {
  // 只开空 tab，不传 url（新版 Chrome 会忽略它，页面停在 about:blank 还读不了 localStorage）
  const r = await fetch(`${base}/json/new`, { method: 'PUT' });
  if (!r.ok) throw new Error(`开 tab 失败 ${r.status}`);
  const tab = await r.json();
  const ws = await new Promise((res, rej) => {
    const w = new WebSocket(tab.webSocketDebuggerUrl);
    w.onopen = () => res(w);
    w.onerror = () => rej(new Error('CDP WebSocket 连不上'));
  });
  const s = { ws, tab, base };
  if (DEBUG) {
    ws.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data);
      if (m.method && !/^Runtime\.(executionContextCreated|consoleAPICalled)$/.test(m.method)) log(`  cdp event ${m.method}`);
    });
    ws.addEventListener('close', (ev) => log(`  cdp ws closed ${ev.code} ${ev.reason}`));
  }
  await send(s, 'Runtime.enable');
  await send(s, 'Page.enable');
  await send(s, 'Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
  return s;
}

async function evalIn(s, expr) {
  const r = await send(s, 'Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) {
    throw new Error('页面报错: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text || '').slice(0, 300));
  }
  return r.result.value;
}

/** 轮询直到 expr 为真。等数据，不等骨架。 */
async function until(s, expr, what, ms = 30000) {
  const t0 = Date.now();
  let last;
  while (Date.now() - t0 < ms) {
    try { if (await evalIn(s, expr)) return; } catch (e) { last = e.message; }
    await sleep(300);
  }
  throw new Error(`等不到「${what}」（${ms} ms）` + (last ? `；最后一次求值报错: ${last}` : ''));
}

const goto = (s, url) => send(s, 'Page.navigate', { url });
const closeTab = async (s) => { try { s.ws.close(); } catch { /* 已断 */ } await fetch(`${s.base}/json/close/${s.tab.id}`).catch(() => {}); };

/** 画面上的文字：innerText 之外还要 placeholder / title / aria-label / value——检索框的提示语每一帧都可见，innerText 取不到。 */
const PAGE_TEXT = `(() => {
  const parts = [document.body.innerText];
  for (const el of document.querySelectorAll('[placeholder],[title],[aria-label],input,textarea')) {
    for (const a of ['placeholder', 'title', 'aria-label']) { const v = el.getAttribute(a); if (v) parts.push(v); }
    if ('value' in el && typeof el.value === 'string' && el.value) parts.push(el.value);
  }
  return parts.join('\\n');
})()`;

/* ---------- 演示服务 ---------- */

class Demo {
  constructor(home) {
    this.home = home;
    const cfg = JSON.parse(readFileSync(join(home, '.config', 'chatdex', 'config.json'), 'utf8'));
    if (!cfg.ports?.ui || !cfg.ports?.api) throw new Error('演示 HOME 的 config.json 没配 ports.ui / ports.api');
    this.ui = `http://127.0.0.1:${cfg.ports.ui}`;
    this.api = `http://127.0.0.1:${cfg.ports.api}`;
    this.db = join(home, '.local', 'share', 'chatdex', 'index.db');
    if (!existsSync(this.db)) throw new Error(`没有索引库 ${this.db}，先 HOME=<home> chatdex index`);
  }
  sql(q) {
    const out = sh('sqlite3', ['-json', this.db, q]);
    return out.trim() ? JSON.parse(out) : [];
  }
  async json(path, init) {
    const r = await fetch(this.api + path, init);
    if (!r.ok) throw new Error(`${init?.method || 'GET'} ${path} → ${r.status} ${await r.text()}`);
    return r.json();
  }
  async healthy() { try { return (await this.json('/api/health')).ok === true; } catch { return false; } }
  async sessions() { return (await this.json('/api/stats')).sessions; }
  async start(bin, logPath) {
    if (await this.healthy()) throw new Error(`${this.api} 已经有服务在跑，先停掉它`);
    const fd = (await import('node:fs')).openSync(logPath, 'a');
    this.child = spawn(bin, ['serve'], { env: { ...process.env, HOME: this.home }, stdio: ['ignore', fd, fd] });
    const t0 = Date.now();
    while (Date.now() - t0 < 60000) {
      if (this.child.exitCode !== null) throw new Error(`服务退出 ${this.child.exitCode}，看 ${logPath}`);
      if (await this.healthy()) { log(`服务就绪 ${this.ui}`); return; }
      await sleep(300);
    }
    throw new Error(`60 s 内等不到 ${this.api}/api/health`);
  }
  async stop() {
    if (!this.child || this.child.exitCode !== null) return;
    this.child.kill('SIGTERM');
    const t0 = Date.now();
    while (this.child.exitCode === null && Date.now() - t0 < 10000) await sleep(100);
    if (this.child.exitCode === null) this.child.kill('SIGKILL');
  }
  /** 摘要页与顶栏进度都会露出「还差几条」，所以拍之前、还原之后都要等到没有缺摘要的会话且队列空。 */
  async summariesComplete(ms) {
    await this.waitFor('全部会话都有摘要且队列空', async () => {
      const missing = this.sql('select count(*) as n from sessions where msg_count > 0 and summary is null')[0].n;
      const p = (await this.json('/api/summary/progress')).counts || {};
      return (missing === 0 && (p.pending || 0) + (p.running || 0) === 0) || { missing, ...p };
    }, ms);
  }
  async waitFor(what, probe, ms) {
    const t0 = Date.now();
    let last;
    while (Date.now() - t0 < ms) {
      try { last = await probe(); if (last === true) return; } catch (e) { last = e.message; }
      await sleep(1000);
    }
    throw new Error(`${ms / 1000} s 内等不到「${what}」，最后一次：${JSON.stringify(last)}`);
  }
}

/** 会话 id 不写死：语料固定种子生成，查不到说明语料不对，报错比拍错图好。 */
function resolveIds(demo) {
  const one = (name, q) => {
    const rows = demo.sql(q);
    if (rows.length !== 1) throw new Error(`查会话「${name}」得到 ${rows.length} 行（要 1 行）：${q}`);
    return rows[0].id;
  };
  return {
    bucket: one('令牌桶', "select distinct session_id as id from blocks where kind='tool_use' and body like '%internal/ratelimit/bucket.go%'"),
    snippet: one('highlightSnippet', "select distinct session_id as id from blocks where kind='tool_result' and body like '%func highlightSnippet%'"),
    eval: one('限流完整评估', "select id from sessions where parent_uid='' and project_path like '%api-gateway' and exists(select 1 from sessions c where c.parent_uid = sessions.session_uid)"),
    certbot: one('certbot', "select distinct session_id as id from blocks where kind='tool_use' and body like '%certbot%' and session_id in (select id from sessions where project_path='/home/dev/dotfiles')"),
  };
}

/* ---------- 两个状态包装：造状态、finally 还原、数字核对 ---------- */

async function withFailedQueue(demo, fn) {
  const rows = demo.sql("select id from sessions where parent_uid <> '' and title in ('压测','文档')");
  if (rows.length !== 2) throw new Error(`要两条子代理会话（压测 / 文档），查到 ${rows.length}`);
  const ids = rows.map((r) => r.id).join(',');
  const saved = demo.sql(`select * from summary_queue where session_id in (${ids})`);
  const unit = (demo.sql('select max(updated_at) as m from summary_queue')[0]?.m || 0) > 1e12 ? 1000 : 1;
  const now = Math.floor(Date.now() / 1000) * unit;
  const err = '模型返回空摘要（done_reason=length，推理占光了 num_predict）';
  demo.sql(rows.map((r) => `insert or replace into summary_queue(session_id,priority,state,attempts,err,updated_at) values (${r.id},1,'failed',3,'${err}',${now});`).join(''));
  try {
    await demo.waitFor('进度接口 failed_total=2', async () => (await demo.json('/api/summary/progress')).failed_total === 2, 15000);
    await fn();
  } finally {
    demo.sql(`delete from summary_queue where session_id in (${ids});` + saved.map((r) =>
      `insert into summary_queue(session_id,priority,state,attempts,err,updated_at) values (${r.session_id},${r.priority},'${r.state}',${r.attempts},'${String(r.err).replace(/'/g, "''")}',${r.updated_at});`).join(''));
    await demo.waitFor('failed_total 回 0', async () => (await demo.json('/api/summary/progress')).failed_total === 0, 15000);
    log('summary_queue 已还原');
  }
}

async function withDeletedSessions(demo, ids, keepDir, fn) {
  const before = await demo.sessions();
  log('先备份一次', JSON.stringify(await demo.json('/api/backup/run', { method: 'POST' })).slice(0, 120));
  const pick = demo.sql(`select id, file_path from sessions where project_path='/home/dev/dotfiles' and alive=1 order by (id=${ids.certbot}) desc, id limit 2`);
  if (pick.length !== 2 || pick[0].id !== ids.certbot) throw new Error('要 dotfiles 项目两条活会话且含 certbot 那条');
  mkdirSync(keepDir, { recursive: true });
  for (const p of pick) { copyFileSync(p.file_path, join(keepDir, basename(p.file_path))); unlinkSync(p.file_path); }
  log(`删了 ${pick.length} 个会话文件（副本在 ${keepDir}）`);
  try {
    await demo.waitFor(`重扫后会话数 ${before - 2}`, async () => (await demo.sessions()) === before - 2, 90000);
    await demo.waitFor('覆盖率：源没了但备份里有 = 2', async () => {
      const c = await demo.json('/api/backup/coverage');
      return (c.rescued_total === 2 && c.lost_total === 0) || c;
    }, 120000);
    await fn();
  } finally {
    const t = new Date();
    for (const p of pick) { copyFileSync(join(keepDir, basename(p.file_path)), p.file_path); utimesSync(p.file_path, t, t); }
    await demo.waitFor(`会话数回 ${before}`, async () => (await demo.sessions()) === before, 90000);
    await demo.waitFor('覆盖率：源没了但备份里有 回 0', async () => {
      const c = await demo.json('/api/backup/coverage');
      return (c.rescued_total === 0 && c.lost_total === 0) || c;
    }, 120000);
    // 拷回的文件 mtime 变了，会被当作改动重新解析，摘要列被清空；worker 下一轮 EnqueueMissing 才补队。
    // 所以不能等「队列为空」——补队之前它本来就是空的——要等「没有缺摘要的会话」。
    await demo.summariesComplete(600000);
    log('会话文件已拷回并 touch，摘要已补齐');
  }
}

/* ---------- 主流程 ---------- */

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const outDir = join(REPO, 'docs', 'images');
  const home = assertDemoHome(args.home);
  const listPath = process.env.CHATDEX_SANITIZE_LIST || join(homedir(), '.config', 'chatdex-sanitize', 'patterns.txt');

  // 前提：缺什么说什么，一次说完
  const missing = [];
  for (const tool of ['go', 'sqlite3', 'restic']) { if (spawnSync(tool, ['--version'] , { stdio: 'ignore' }).error) missing.push(`PATH 上没有 ${tool}`); }
  if (!existsSync(listPath)) missing.push(`黑名单文件不存在：${listPath}（CHATDEX_SANITIZE_LIST 可指定）`);
  if (!(await fetch(`${args.cdp}/json/version`).then((r) => r.ok).catch(() => false))) missing.push(`CDP 不可达：${args.cdp}`);
  if (missing.length) throw new Error('前提不满足：\n  - ' + missing.join('\n  - '));
  const patterns = [...compilePatterns(readFileSync(listPath, 'utf8')),
    ...identityPatterns({ user: userInfo().username, host: hostname(), home: homedir() })];
  log(`禁用模式 ${patterns.length} 条`);

  const tmp = mkdtempSync(join(tmpdir(), 'chatdex-shots-'));
  const bin = join(tmp, 'chatdex');
  log('go build …');
  sh('go', ['build', '-o', bin, './cmd/chatdex'], { cwd: REPO });

  const demo = new Demo(home);
  const ids = resolveIds(demo);
  log('会话 id', JSON.stringify(ids));
  const shots = SHOTS.filter((s) => !args.only || args.only.has(s.file));
  if (!shots.length) throw new Error('--only 没选中任何图');
  const manifest = { taken_at: new Date().toISOString(), ui_digest: sh('bash', [join(REPO, 'scripts', 'ui-digest.sh')], { cwd: REPO }).trim(),
    viewport: { width: 1440, height: 900, dpr: 1 }, theme: { mode: 'light', light: 'desk' }, shots: [] };

  await demo.start(bin, join(tmp, 'serve.log'));
  await demo.summariesComplete(600000);
  let s = null;
  const freshTab = async () => {
    if (s) await closeTab(s);
    s = await openTab(args.cdp, 1440, 900);
    // 先导航到同源页，再写偏好（about:blank 上读写 localStorage 会抛）
    await goto(s, `${demo.ui}/?view=search`);
    await until(s, "document.readyState === 'complete' && !!document.getElementById('q')", '首页就绪');
    await evalIn(s, `localStorage.setItem('chatdex.mode', 'light');
      localStorage.setItem('chatdex.themes', JSON.stringify({ light: 'desk', dark: 'editor' }));
      localStorage.removeItem('chatdex.hideKinds'); localStorage.removeItem('chatdex.raw'); 1`);
  };
  await freshTab();
  try {

    const take = async (shot) => {
      await evalIn(s, `localStorage.setItem('chatdex.sidebar', '${shot.sidebar || 'wide'}'); 1`);
      const params = new URLSearchParams({ view: shot.view });
      for (const [k, v] of Object.entries(shot.params)) params.set(k, String(v).startsWith('$') ? String(ids[String(v).slice(1)]) : String(v));
      await goto(s, `${demo.ui}/?${params}`);
      await until(s, shot.ready, `${shot.file} 就绪`, shot.timeout || 30000);
      await act(shot);
      await sleep(600);
      const hits = forbiddenHits(await evalIn(s, PAGE_TEXT), patterns);
      if (hits.length) throw new Error(`${shot.file} 画面上命中禁用模式 #${hits.map((h) => h.index).join(', #')}，不拍`);
      const buf = Buffer.from((await send(s, 'Page.captureScreenshot', { format: 'png' })).data, 'base64');
      const bad = pngChunkTypes(buf).filter((t) => TEXT_CHUNKS.includes(t));
      if (bad.length) throw new Error(`${shot.file} 含文本块 ${bad.join(',')}，不落盘`);
      writeFileSync(join(outDir, shot.file), buf);
      manifest.shots.push({ file: shot.file, view: shot.view, state: shot.note });
      log('拍好', shot.file, `(${buf.length} B)`);
    };
    const act = async (shot) => {
      switch (shot.act) {
        case 'mermaid':
          await evalIn(s, `document.getElementById('seq-${shot.params.seq}').querySelector('.mmd-btn').click(); 1`);
          await until(s, `!!document.getElementById('seq-${shot.params.seq}').querySelector('.mmd-out svg')`, 'mermaid 渲染完成', 60000);
          return;
        case 'kids':
          await evalIn(s, "document.getElementById('rd-kids').click(); 1");
          await until(s, "document.getElementById('rd-kids').textContent.startsWith('▾')", '子代理列表展开');
          return;
        case 'ask': {
          // 7B 模型偶尔把工具调用当正文吐出来（「search_sessions {…}」），那不是 README 要展示的回答。
          // 合格的回答要有至少一轮真实检索、且正文里引用了会话号——那正是这一页要展示的能力。
          const good = "document.querySelectorAll('#chat-log .round').length >= 1 && document.querySelectorAll('#chat-log .a .sess-link').length >= 1";
          for (let attempt = 1; ; attempt++) {
            if (attempt > 1) { await goto(s, `${demo.ui}/?${new URLSearchParams({ view: shot.view, ...shot.params })}`); await until(s, shot.ready, '问一问重载'); }
            await evalIn(s, `document.getElementById('chat-q').value = ${JSON.stringify(CHAT_Q)}; document.getElementById('chat-form').requestSubmit(); 1`);
            await until(s, "!document.getElementById('chat-send').disabled && (document.querySelector('#chat-log .a')?.innerText || '').length > 0",
              'LLM 回答结束', shot.timeout);
            if (await evalIn(s, good)) return;
            if (attempt >= 3) throw new Error('问一问连续 3 次没给出引用会话的回答，不拍');
            log(`问一问第 ${attempt} 次回答不合格（没有检索轮次或没引用会话），重问`);
          }
        }
        case 'coverage':
          await evalIn(s, "const b = document.getElementById('bk-cov'); if (b.textContent.trim() === '核对一遍') b.click(); 1");
          await until(s, "document.getElementById('bk-cov')?.textContent.trim() === '重新核对' && document.body.innerText.includes('可从备份读回的')", '覆盖率四个数', 60000);
          return;
        case 'archived':
          await evalIn(s, "document.getElementById('rd-arch').click(); 1");
          await until(s, "(document.querySelector('.reader-meta .ok-inline')?.textContent || '').includes('正在看原件') && document.querySelectorAll('.msg').length > 0", '备份里的原件读出来');
          return;
        default:
      }
    };

    // 实测两次（同一台 Chrome、同一页面）Runtime.evaluate 连续超时整整 180 s，MCP 走同一浏览器复现不了，
    // 原因没查清。处置：卡住就换 tab 重拍这一张，只重一次，日志里留痕。
    const takeOnce = async (shot) => {
      try { await take(shot); } catch (e) {
        if (!/超时\(30s\)/.test(e.message)) throw e;
        log(`${shot.file}: CDP 卡住（${e.message}），换 tab 重拍一次`);
        await freshTab();
        await take(shot);
      }
    };
    const plain = shots.filter((x) => !x.state);
    for (const shot of plain) await takeOnce(shot);
    const failed = shots.filter((x) => x.state === 'failedQueue');
    if (failed.length) await withFailedQueue(demo, async () => { for (const shot of failed) await takeOnce(shot); });
    const deleted = shots.filter((x) => x.state === 'deletedSessions');
    if (deleted.length) await withDeletedSessions(demo, ids, join(tmp, 'keep'), async () => { for (const shot of deleted) await takeOnce(shot); });

    if (args.only) log('用了 --only，manifest 不写（半套图不冒充整套）');
    else {
      writeFileSync(join(outDir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
      log(`manifest 写好，ui_digest=${manifest.ui_digest}，${manifest.shots.length} 张`);
    }
  } finally {
    if (s) await closeTab(s);
    await demo.stop();
    const alive = demo.sql('select count(*) as n, sum(alive) as a from sessions')[0];
    log(`收尾：sessions ${alive.a}/${alive.n} alive；临时目录 ${tmp}`);
    if (alive.a === alive.n) rmSync(tmp, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((e) => { console.error('✖', e.message); process.exit(1); });
}
