/* 备份。
 *
 * chatdex 不做 restic 的壳子——分工是清楚的：
 *   restic  管「存得住、存得安全」：去重、压缩、加密、完整性校验
 *   chatdex 管 restic 做不到的：知道该存什么、**存了没有**、消失的怎么读回来
 *
 * 所以这一页的重点不是复刻 restic 的界面，而是那个 restic 永远答不出的
 * 问题——「我索引过的那些会话，备份里到底有没有」。
 */

(() => {
  let root = null;
  let st = null;      // /api/backup/status
  let cov = null;     // /api/backup/coverage，按需拉（要跑 restic ls，秒级）
  let snaps = null;
  let last = null;    // 本次会话里最后一次手动备份的结果
  let busy = '';

  async function load() {
    if (!root) return;
    try {
      st = await CD.api('/api/backup/status');
    } catch (e) {
      st = { available: false, reason: e.message };
    }
    // 仓库没就绪就不去拉快照——那只会得到一个必然失败的请求
    if (st.repo_ready) {
      try { snaps = await CD.api('/api/backup/snapshots'); } catch { snaps = null; }
    }
    render();
  }

  function render() {
    if (!root) return;
    root.innerHTML = `<div class="bk-wrap">
      ${statusCard()}
      ${st.repo_ready ? actionCard() + coverageCard() + snapsCard() : ''}
      ${warnCard()}
    </div>`;
    bind();
  }

  /* 状态：可用 / 不可用 + 原因。与「问一问」在 LLM 不可用时的处理同构。 */
  function statusCard() {
    const ok = st.available;
    return `<section class="bk-card">
      <h2>状态 <span class="bk-dot ${ok ? 'on' : 'off'}"></span></h2>
      <dl class="bk-kv">
        <dt>restic</dt><dd>${st.version ? CD.esc(st.version) : '<span class="bad">没找到</span>'}</dd>
        <dt>仓库</dt><dd class="mono">${st.repo ? CD.esc(st.repo) : '<span class="bad">未配置</span>'}</dd>
      </dl>
      ${ok ? '' : `<p class="err">${CD.esc(st.reason || '备份不可用')}</p>`}
      ${!ok && st.version && st.repo && !st.repo_ready
        ? `<p><button id="bk-init" class="sm" type="button" ${busy === 'init' ? 'disabled' : ''}>${
            busy === 'init' ? '初始化中…' : '初始化仓库'}</button></p>`
        : ''}
      ${!st.repo || !st.version
        ? '<p class="hint">去<a href="?view=settings">设置</a>里填仓库路径、密码文件与 restic 路径。</p>'
        : ''}
    </section>`;
  }

  function actionCard() {
    return `<section class="bk-card">
      <h2>备份</h2>
      <p><button id="bk-run" type="button" ${busy === 'run' ? 'disabled' : ''}>${
        busy === 'run' ? '备份中…（大目录首次会久一点）' : '立即备份'}</button></p>
      <p class="hint">备哪些目录在<a href="?view=settings">设置</a>里勾选。restic 是内容寻址的，
        没变的内容不会重复占空间——实测无变化时再备一次只要 767 ms。</p>
      ${last ? lastResult() : ''}
      ${autoResult()}
    </section>`;
  }

  /* 自动备份（扫描后顺手那次）的结果。
   *
   * 必须显示出来：那条路径没人看着，失败了只写 slog 的话，一个每半小时
   * 失败一次的备份除了 journalctl 里没人看得见（需求 5.3 明确禁止只记日志）。
   */
  function autoResult() {
    const a = st.last_auto;
    if (!a) return '';
    const when = CD.fmtTime(Math.floor(new Date(a.at).getTime() / 1000));
    if (a.error) {
      return `<div class="bk-last warn"><strong>上次自动备份失败</strong>（${when}）：
        ${CD.esc(a.error)}</div>`;
    }
    return `<div class="bk-last ok">上次自动备份 ${when} · 新增 ${a.files_new} 个文件 ·
      写入 ${CD.fmtBytes(a.bytes_added)}</div>`;
  }

  function lastResult() {
    if (last.error) return `<p class="err">上次备份失败：${CD.esc(last.error)}</p>`;
    return `<div class="bk-last ${last.partial ? 'warn' : 'ok'}">
      ${last.partial ? '<strong>部分完成</strong>：有源数据读不了（权限或文件正在被改），其余都备好了。<br>' : ''}
      新增 ${last.files_new} 个文件 · 变更 ${last.files_changed} · 共 ${last.files_total} ·
      写入 ${CD.fmtBytes(last.bytes_added)} · 耗时 ${last.seconds.toFixed(1)} 秒
      <div class="mono muted">快照 ${CD.esc((last.snapshot_id || '').slice(0, 12))}</div>
    </div>`;
  }

  /* 覆盖率：这一页真正的理由。
   *
   * 要跑 restic ls（实测 7036 个文件约 1.7 秒），所以不随页面自动拉，
   * 点了才算——否则每次进这一页都白等一秒多。
   */
  function coverageCard() {
    return `<section class="bk-card">
      <h2>覆盖率</h2>
      <p class="hint">restic 只知道路径，不知道什么是会话。这里把索引里的每个会话
        与最新快照对一遍，回答那个 restic 答不出的问题。</p>
      <p><button id="bk-cov" class="sm" type="button" ${busy === 'cov' ? 'disabled' : ''}>${
        busy === 'cov' ? '核对中…' : (cov ? '重新核对' : '核对一遍')}</button></p>
      ${cov ? covResult() : ''}
    </section>`;
  }

  function covResult() {
    if (cov.error) return `<p class="err">核对失败：${CD.esc(cov.error)}</p>`;
    // 「没备但源还在」与「没备且源已没」是两件天差地别的事：前者去勾上就好，
    // 后者是永久丢失。只报一个「未覆盖」会把后者说成前者。
    const stillThere = cov.missing_total - cov.lost_total;
    return `<div class="bk-cov">
      ${covNum('已覆盖', cov.covered_total, 'ok')}
      ${covNum('没备（源还在）', stillThere, stillThere ? 'warn' : '')}
      ${covNum('永久丢失', cov.lost_total, cov.lost_total ? 'bad' : '')}
      ${covNum('源没了但备份里有', cov.rescued_total, cov.rescued_total ? 'ok' : '')}
    </div>
    ${stillThere ? '<p class="hint">「源还在」的那些去设置里把对应目录勾上，下次备份就带上了。</p>' : ''}
    ${cov.lost_total ? `<p class="hint">「永久丢失」= 源文件没了、备份里也没有。备份救不回
      已经消失的东西，只能从配好之后开始保护。</p>` : ''}
    ${cov.rescued_total ? `<p class="hint">「源没了但备份里有」的会话，在回读页可以直接
      <strong>从备份读原件</strong>。</p>` : ''}
    ${listOf('没备的', cov.missing)}${listOf('可从备份读回的', cov.rescued)}`;
  }

  function covNum(label, n, cls) {
    return `<div class="bk-num ${cls || ''}"><span class="n">${n}</span><span class="t">${label}</span></div>`;
  }

  /* 清单限量 50 条：条数是后端单独算的，这里不能拿 length 当总数（R8 栽过）。 */
  function listOf(label, items) {
    if (!items || !items.length) return '';
    return `<details class="bk-list"><summary>${label}（列出前 ${items.length} 条）</summary>
      <ul>${items.map((e) => `<li class="mono">${CD.esc(e.path)}${
        e.alive ? '' : ' <span class="muted">· 源已消失</span>'}</li>`).join('')}</ul></details>`;
  }

  function snapsCard() {
    if (!snaps || !snaps.length) {
      return '<section class="bk-card"><h2>快照</h2><p class="hint">还没有快照。</p></section>';
    }
    // restic snapshots 是权威记录，chatdex 不另存一份——倒序显示，最新在上
    const rows = snaps.slice().reverse().slice(0, 20).map((s) => `<tr>
      <td class="mono">${CD.esc(s.id.slice(0, 12))}</td>
      <td>${CD.fmtTime(Math.floor(new Date(s.time).getTime() / 1000))}</td>
      <td class="mono muted">${CD.esc((s.paths || []).join(' '))}</td>
    </tr>`).join('');
    return `<section class="bk-card">
      <h2>快照 <span class="muted">${snaps.length}</span></h2>
      <table class="bk-snaps"><tbody>${rows}</tbody></table>
      <p class="hint">快照由 restic 自己管，chatdex 不另存一份（存两份必然漂移）。
        要清理旧快照用 <code class="mono">restic forget</code>——删备份是危险操作，不放进界面。</p>
    </section>`;
  }

  function warnCard() {
    return `<section class="bk-card bk-warn">
      <h2>两件必须知道的事</h2>
      <ul>
        <li><strong>密码丢了，备份就打不开了。</strong>restic 的加密没有后门，
          chatdex 也没有——密码文件请自己另存一份到别的地方。</li>
        <li>chatdex <strong>只读</strong>，永远不写你的会话文件，也不代做恢复。
          要把文件恢复到磁盘，自己跑 <code class="mono">restic restore</code>。</li>
      </ul>
    </section>`;
  }

  function bind() {
    const init = CD.$('bk-init');
    if (init) init.onclick = () => act('init', () => CD.api('/api/backup/init', { method: 'POST' }));

    const run = CD.$('bk-run');
    if (run) run.onclick = () => act('run', async () => {
      try {
        last = await CD.api('/api/backup/run', { method: 'POST' });
      } catch (e) {
        last = { error: e.message };
      }
      // 备份后覆盖率必然变了，留着旧数字会撒谎
      cov = null;
    });

    const c = CD.$('bk-cov');
    if (c) c.onclick = () => act('cov', async () => {
      // 不用 alert：模态框会挡住页面，错误该显示在它该在的地方
      try { cov = await CD.api('/api/backup/coverage'); } catch (e) { cov = { error: e.message }; }
    });
  }

  /* 按钮在跑的时候要置灰并说明在做什么——备份可能要几十秒，
   * 没有这个反馈用户会以为没点上然后连点。 */
  async function act(what, fn) {
    busy = what;
    render();
    try { await fn(); } finally { busy = ''; }
    await load();
  }

  CD.register('backup', {
    mount(el) {
      root = el;
      root.innerHTML = '<p class="hint">载入中…</p>';
      load();
    },
    unmount() { root = null; },
  });
})();
