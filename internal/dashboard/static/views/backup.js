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
  let sugg = null;    // /api/backup/suggest，随页面一起拉（只做 os.Stat，实测 0.25 ms）
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
    // 建议不依赖 restic，就算备份没配好也该告诉你「有哪些东西该备」
    try { sugg = await CD.api('/api/backup/suggest'); } catch { sugg = null; }
    render();
  }

  function render() {
    if (!root) return;
    root.innerHTML = `<div class="bk-wrap">
      ${statusCard()}
      ${st.repo_ready ? actionCard() + coverageCard() : ''}
      ${suggestCard()}
      ${st.repo_ready ? snapsCard() : ''}
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

  /* 覆盖率是拿哪一刻的快照算的 —— 不说清楚，下面那四个数就会骗人。
   *
   * 覆盖率永远拿「最新快照」比对，而「最新」可能是一周前：那之后新建的
   * 会话既不在「已覆盖」也不在「未覆盖」，它们压根不在比对基准里，
   * 而界面上一片安好。实测撞到过——快照停在 08-10，页面在 08-17
   * 仍然报「已覆盖 3082」。
   *
   * 过期判定（stale）由后端给，前端不自己算：阈值只能有一处（R13）。
   */
  function covBasis() {
    if (!cov.snapshot_time) {
      return '<p class="hint">还没有任何快照 —— 点上面的「立即备份」跑第一次。</p>';
    }
    const when = CD.fmtTime(Math.floor(new Date(cov.snapshot_time).getTime() / 1000));
    if (!cov.stale) {
      return `<p class="hint">依据 ${when} 的快照（${CD.esc(cov.stale_for)}）。</p>`;
    }
    return `<div class="bk-stale">
      <strong>这份覆盖率已经过期了</strong> —— 依据的是 ${when} 的快照（${CD.esc(cov.stale_for)}）。
      <br>下面的数字只说明「截至那一刻」的情况：<strong>那之后新建的会话一条都没备</strong>，
      而它们既不算「已覆盖」也不算「未覆盖」，因为压根不在比对基准里。
      <br>去<a href="?view=settings">设置</a>确认「扫描后顺手备一次」是否开着，或点上面的「立即备份」。
    </div>`;
  }

  function covResult() {
    if (cov.error) return `<p class="err">核对失败：${CD.esc(cov.error)}</p>`;
    // 「没备但源还在」与「没备且源已没」是两件天差地别的事：前者去勾上就好，
    // 后者是永久丢失。只报一个「未覆盖」会把后者说成前者。
    const stillThere = cov.missing_total - cov.lost_total;
    return `${covBasis()}
    <div class="bk-cov">
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

  /* 「还该备什么」——覆盖率的另一半。
   *
   * 覆盖率回答「我索引过的**会话**备份里有没有」；这里回答
   * 「我这台机器上 **agent 的东西**备了没有」。restic 只看得见路径，
   * 它不知道 ~/.codex/memories 是什么——所以这件事只有 chatdex 能做。
   *
   * 触发它的是一次真实漏备：手工配了两个会话目录就以为齐了，
   * 而 Codex 的记忆（git 仓但通常无远端）一直在外面。
   */
  function suggestCard() {
    if (!sugg || !sugg.length) return '';
    const missing = sugg.filter((s) => !s.covered);
    const covered = sugg.length - missing.length;
    if (!missing.length) {
      return `<section class="bk-card">
        <h2>还该备什么</h2>
        <p class="hint">已知的 ${sugg.length} 项 agent 数据全都在备份源里了。</p>
      </section>`;
    }
    return `<section class="bk-card">
      <h2>还该备什么 <span class="muted">${missing.length} 项没备</span></h2>
      <p class="hint">restic 只知道路径，不知道什么是 agent 的数据。这里按已知的
        目录布局对了一遍——已覆盖 ${covered} 项，下面这些还在外面。</p>
      <ul class="bk-sugg">${missing.map(suggRow).join('')}</ul>
    </section>`;
  }

  function suggRow(s) {
    return `<li${s.secrets ? ' class="has-secret"' : ''}>
      <div class="p mono">${CD.esc(s.path)}</div>
      <div class="w">${CD.esc(s.what)}</div>
      ${s.secrets ? '<div class="sec">⚠️ 这个文件里通常有明文凭据 —— 备份是加密的，但要不要把它放进去请你自己决定</div>' : ''}
      <button class="ghost sm bk-add" type="button" data-path="${CD.esc(s.path)}">加进备份源</button>
    </li>`;
  }

  /* 一键加源：走既有的 PUT /api/config —— 配置只有一个写入口，
   * 不为这件事新增端点。 */
  async function addSource(path) {
    const cfg = await CD.api('/api/config');
    const cur = (cfg.fields || []).find((f) => f.key === 'backup.sources');
    const list = Array.isArray(cur && cur.value) ? cur.value.slice() : [];
    if (list.some((x) => x.path === path)) return;
    list.push({ path, enabled: true });
    await CD.api('/api/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ backup: { sources: list } }),
    });
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

    root.querySelectorAll('.bk-add').forEach((b) => {
      b.onclick = async () => {
        b.disabled = true;
        b.textContent = '加入中…';
        try {
          await addSource(b.dataset.path);
        } catch (e) {
          b.textContent = '加入失败：' + e.message;
          return;   // 不静默——加不进去要说出来
        }
        await load();
      };
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
