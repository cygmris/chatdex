/* 会话回读。
 *
 * 不是一个「视图」——它是从检索/时间线/摘要点进来的覆盖层，
 * 关掉后回到原来的视图，过滤条件与滚动位置都还在。
 */

(() => {
  const PAGE = 100;
  // 默认渲染；选择记在 localStorage（属于偏好，不进 URL）
  let raw = localStorage.getItem('chatdex.raw') === '1';
  let cur = { id: 0, from: 0, total: 0, target: 0, back: 'search', archived: false };
  // 本页的 tool_use_id → 调用消息；随每次 load 重建
  let calls = new Map();

  // fromRoute=true 表示这次打开是「还原 URL」而不是「用户点击」，
  // 此时不能再写历史，否则后退一次会停在同一个会话上
  CD.openSession = async (id, targetSeq = 0, fromRoute = false) => {
    // 命中在哪一页就从哪一页开始加载
    const from = Math.max(0, Math.floor(targetSeq / PAGE) * PAGE);
    cur = { id, from, total: 0, target: targetSeq, back: CD.view, archived: false };
    document.documentElement.dataset.reader = 'open';
    CD.$('filters').hidden = true;
    if (!fromRoute) CD.route.write({ extra: { id, seq: targetSeq } });
    await load();
  };

  function close() {
    delete document.documentElement.dataset.reader;
    // switchView 会写 URL，且 extra 里没有 id/seq，回读参数自然掉出去
    CD.switchView(cur.back);
  }

  async function load() {
    const root = CD.$('view');
    root.innerHTML = '<p class="hint">载入中…</p>';
    let v;
    const src = cur.archived ? '/archived' : '';
    try {
      v = await CD.api(`/api/session/${cur.id}${src}?from=${cur.from}&limit=${PAGE}`);
    } catch (e) {
      // 取回失败要说清楚，不能悄悄换成索引里那份——那等于「假装还在」，
      // 而索引里的工具结果是截断过的，用户看到的会是残缺内容却不知道。
      if (cur.archived) {
        root.innerHTML = `<p class="err">从备份取回失败：${CD.esc(e.message)}</p>
          <p><button id="rd-unarchive" class="ghost sm" type="button">看索引里的那份（工具结果可能被截断）</button></p>`;
        CD.$('rd-unarchive').onclick = () => { cur.archived = false; load(); };
        return;
      }
      root.innerHTML = `<p class="err">载入失败：${CD.esc(e.message)}</p>`;
      return;
    }
    cur.total = v.total;
    // 本页内 tool_use_id → 调用，供 tool_result 反查它是什么命令的输出。
    // Message 已经带 tool_use_id，同页就能配上，不必改后端多下发一份数据。
    calls = new Map();
    for (const m of v.messages || []) {
      if (m.kind === 'tool_use' && m.tool_use_id) calls.set(m.tool_use_id, m);
    }

    const hasPrev = cur.from > 0;
    const hasNext = cur.from + PAGE < cur.total;

    root.innerHTML = `
      <div class="reader-head">
        <button id="rd-back" class="ghost sm" type="button">← 返回</button>
        <button id="rd-raw" class="ghost sm" type="button"
          title="渲染：按 Markdown 与 ANSI 显示；原文：显示索引时存下的原始字节">${raw ? '◧ 原文' : '◨ 渲染'}</button>
        <div class="reader-meta">
          <div class="path mono">${CD.esc(v.project_path || '（未知项目）')}</div>
          <div class="muted">${CD.fmtRange(v.started_at, v.ended_at)} · ${v.total} 条 ·
            ${v.source === 'codex' ? 'Codex' : 'Claude'}${v.is_sub ? ' · 子代理' : ''}
            ${v.alive ? '' : ' · <span class="err-inline">原始文件已不存在</span>'}
            ${cur.archived ? ' · <span class="ok-inline">正在看备份里的原件</span>' : ''}</div>
          ${archivedBar(v)}
          ${v.title ? `<div class="reader-title">${CD.esc(v.title)}</div>` : ''}
          ${v.summary ? `<div class="reader-sum">${CD.esc(v.summary)}</div>` : ''}
          <div class="reader-path mono" title="原始文件绝对路径">${CD.esc(v.file_path)}</div>
          ${relLinks(v)}
        </div>
      </div>
      <div class="msgs">${(v.messages || []).map(msg).join('') || '<p class="hint">这一页没有内容。</p>'}</div>
      <div class="pager">
        ${hasPrev ? '<button id="rd-prev" class="ghost sm" type="button">上一页</button>' : ''}
        <span>${cur.from + 1}–${Math.min(cur.from + PAGE, cur.total)} / ${cur.total}</span>
        ${hasNext ? '<button id="rd-next" class="ghost sm" type="button">下一页</button>' : ''}
      </div>`;

    CD.$('rd-back').onclick = close;
    bindRelations(v);
    const arch = CD.$('rd-arch');
    if (arch) arch.onclick = toggleArchived;
    CD.$('rd-raw').onclick = () => {
      raw = !raw;
      localStorage.setItem('chatdex.raw', raw ? '1' : '0');
      load();   // 重渲染这一页；分页位置与目标锚点都在 cur 里，不会丢
    };
    const p = CD.$('rd-prev');
    const n = CD.$('rd-next');
    if (p) p.onclick = () => { cur.from -= PAGE; load(); };
    if (n) n.onclick = () => { cur.from += PAGE; load(); };

    // Markdown 里的围栏代码块着色（必须在 DOM 上做，不对 HTML 串做替换）
    root.querySelectorAll('.msg .md').forEach((el) => CD.enhance(el));

    const t = document.getElementById('seq-' + cur.target);
    if (t) t.scrollIntoView({ block: 'center' });
  }

  /* ---------------- 从备份读原件 ---------------- */

  /* 备份仓库路径，用来拼那条可复制的恢复命令。
   * 只在第一次进入归档模式时取一次；备份没配就一直是空的。 */
  let repo = null;

  /* 入口条：只在源文件已消失时出现。
   *
   * 索引对工具结果是**故意有损**的（超阈值截断、非文本清空），所以
   * 「看原件」对还活着的会话同样有价值——但那条路该直接读源文件，
   * 不该绕 restic，是另一件事（BACKLOG）。这里只管源文件没了的情况：
   * 那时索引是仅剩的一份，而它是残缺的。
   */
  function archivedBar(v) {
    if (v.alive && !cur.archived) return '';
    const cmd = repo && v.file_path
      ? `restic -r ${repo} restore latest --target / --include ${v.file_path}`
      : '';
    return `<div class="arch-bar">
      <button id="rd-arch" class="ghost sm" type="button">${
        cur.archived ? '← 看索引里的那份' : '从备份读原件'}</button>
      ${cur.archived && cmd
        ? `<span class="restore">要把文件恢复到磁盘，自己跑这条（chatdex 只读，不代劳）：
             <code class="mono">${CD.esc(cmd)}</code></span>`
        : ''}
    </div>`;
  }

  async function toggleArchived() {
    if (!cur.archived && repo === null) {
      // 拼恢复命令要用仓库路径；取不到就不显示那条命令，不挡住读取本身
      try { repo = (await CD.api('/api/backup/status')).repo || ''; } catch { repo = ''; }
    }
    cur.archived = !cur.archived;
    load();
  }

  /* ---------------- 父子关系 ---------------- */

  /* 主会话入口 + 子代理入口。
   *
   * 子代理列表**不在这里取**：3207 个会话里只有 61 个有子代理，
   * 为那 1.9% 让每次回读都多发一个请求不划算。先只画壳子，点开才拉。
   */
  function relLinks(v) {
    let out = '';
    if (v.parent) {
      out += `<div class="rel-up">← 属于主会话
        <a href="#" class="rel-parent" data-id="${v.parent.id}">${CD.esc(CD.sessionTitle(v.parent))}</a></div>`;
    } else if (v.is_sub) {
      // 实测 1554 个子代理的 parent_uid 100% 能对上，但索引可以被部分删除。
      // 这种情况下标身份、不给死链。
      out += '<div class="rel-up muted">子代理（主会话不在索引里）</div>';
    }
    if (v.child_count > 0) {
      out += `<div class="rel-down">
        <button type="button" class="ghost sm" id="rd-kids">▸ 子代理 (${v.child_count})</button>
        <div class="kid-list" id="rd-kid-list" hidden></div></div>`;
    }
    return out;
  }

  function bindRelations(v) {
    const up = document.querySelector('.rel-parent');
    if (up) {
      up.onclick = (e) => { e.preventDefault(); CD.openSession(+up.dataset.id, 0); };
    }
    const btn = CD.$('rd-kids');
    if (!btn) return;
    const box = CD.$('rd-kid-list');
    btn.onclick = async () => {
      if (!box.hidden) {                    // 收起
        box.hidden = true;
        btn.textContent = `▸ 子代理 (${v.child_count})`;
        return;
      }
      if (!box.dataset.done) {
        btn.disabled = true;
        try {
          const d = await CD.api(`/api/session/${v.id}/children`);
          box.innerHTML = renderKids(d);
          box.dataset.done = '1';
        } catch (e) {
          box.innerHTML = `<p class="err">载入失败：${CD.esc(e.message)}</p>`;
        } finally {
          btn.disabled = false;
        }
      }
      box.hidden = false;
      btn.textContent = `▾ 子代理 (${v.child_count})`;
      box.querySelectorAll('a[data-id]').forEach((a) => {
        a.onclick = (e) => { e.preventDefault(); CD.openSession(+a.dataset.id, 0); };
      });
    };
  }

  function renderKids(d) {
    const items = d.items || [];
    // 有主会话挂着 350 个子代理，列表只返回前 50 —— 必须说清楚看到的是一部分，
    // 否则「共 350 个」和眼前的 50 条对不上会让人以为数据错了。
    const more = d.total > items.length
      ? `<p class="hint">共 ${d.total} 个，这里显示前 ${items.length} 个。
         用顶部的「仅子代理」筛选可以逐个检索。</p>`
      : '';
    return items.map((k) => `
      <div class="kid">
        <a href="#" data-id="${k.id}">${CD.esc(CD.sessionTitle(k))}</a>
        <span class="muted sm">${CD.fmtTime(k.started_at)} · ${k.msg_count} 条</span>
      </div>`).join('') + more;
  }

  /* 按内容类型选渲染方式。
   *
   * 这是取证工具，「原文」不是可有可无的开关：索引时做过截断与注入剥离，
   * 有时需要看到存下来的究竟是什么字节。所以默认渲染、但一键可切回。
   */
  function body(m) {
    if (raw) return `<pre>${CD.esc(m.body)}</pre>`;
    switch (m.kind) {
      case 'assistant':
      case 'user':
      case 'summary':
        // 这三类是人/模型写的散文，Markdown 是它们的原生格式
        return `<div class="md">${CD.md(m.body)}</div>`;
      case 'tool_result': {
        // 命令输出：可能带 ANSI 颜色，但不是 Markdown。
        //
        // 其中读文件类命令的输出**就是源码**（实测占 exec/Bash 的 56.1%）。
        // 能从调用参数判出语言就按那个语言高亮；判不出一律保持原样——
        // 对输出做自动识别会把日志涂成花的，而 highlightAuto 实测把 JS 认成 php。
        const call = m.tool_use_id ? calls.get(m.tool_use_id) : null;
        // 配不上（分页边界上调用在上一页）就退回现状，不跨页请求
        const lang = call ? CD.resultLang(call.tool_name, call.body) : '';
        return CD.toolResult(m.body, lang);
      }
      case 'tool_use':
        // 第三种渲染：参数按结构显示。R3 时这里写的是"两种渲染都不合适、保持原样"——
        // "我这两个渲染器都不适用"推不出"原样最好"，正确结论是需要第三种。
        return `<div class="tc-wrap">${CD.toolCall(m.tool_name, m.body)}</div>`;
      default:
        return `<pre>${CD.esc(m.body)}</pre>`;
    }
  }

  function msg(m) {
    return `
      <div class="msg ${CD.esc(m.kind)}${m.seq === cur.target ? ' target' : ''}" id="seq-${m.seq}">
        <div class="msg-head">
          <span class="role">${CD.esc(CD.KIND_LABEL[m.kind] || m.kind)}</span>
          ${m.tool_name ? `<span class="tool">${CD.esc(m.tool_name)}</span>` : ''}
          <span>${CD.fmtTime(m.ts)}</span>
          <span>#${m.seq}</span>
          ${m.truncated ? `<span class="trunc" title="原始 ${CD.fmtBytes(m.raw_bytes)}，索引时已截断">已截断</span>` : ''}
        </div>
        ${body(m)}
      </div>`;
  }
})();
