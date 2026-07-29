/* 会话回读。
 *
 * 不是一个「视图」——它是从检索/时间线/摘要点进来的覆盖层，
 * 关掉后回到原来的视图，过滤条件与滚动位置都还在。
 */

(() => {
  const PAGE = 100;
  let cur = { id: 0, from: 0, total: 0, target: 0, back: 'search' };

  CD.openSession = async (id, targetSeq = 0) => {
    // 命中在哪一页就从哪一页开始加载
    const from = Math.max(0, Math.floor(targetSeq / PAGE) * PAGE);
    cur = { id, from, total: 0, target: targetSeq, back: CD.view };
    document.documentElement.dataset.reader = 'open';
    CD.$('filters').hidden = true;
    await load();
  };

  function close() {
    delete document.documentElement.dataset.reader;
    CD.switchView(cur.back);
  }

  async function load() {
    const root = CD.$('view');
    root.innerHTML = '<p class="hint">载入中…</p>';
    let v;
    try {
      v = await CD.api(`/api/session/${cur.id}?from=${cur.from}&limit=${PAGE}`);
    } catch (e) {
      root.innerHTML = `<p class="err">载入失败：${CD.esc(e.message)}</p>`;
      return;
    }
    cur.total = v.total;

    const hasPrev = cur.from > 0;
    const hasNext = cur.from + PAGE < cur.total;

    root.innerHTML = `
      <div class="reader-head">
        <button id="rd-back" class="ghost sm" type="button">← 返回</button>
        <div class="reader-meta">
          <div class="path mono">${CD.esc(v.project_path || '（未知项目）')}</div>
          <div class="muted">${CD.fmtRange(v.started_at, v.ended_at)} · ${v.total} 条 ·
            ${v.source === 'codex' ? 'Codex' : 'Claude'}${v.agent_label ? ' · 子代理 ' + CD.esc(v.agent_label) : ''}
            ${v.alive ? '' : ' · <span class="err-inline">原始文件已不存在</span>'}</div>
          ${v.summary ? `<div class="reader-sum">${CD.esc(v.summary)}</div>` : ''}
          <div class="reader-path mono" title="原始文件绝对路径">${CD.esc(v.file_path)}</div>
        </div>
      </div>
      <div class="msgs">${(v.messages || []).map(msg).join('') || '<p class="hint">这一页没有内容。</p>'}</div>
      <div class="pager">
        ${hasPrev ? '<button id="rd-prev" class="ghost sm" type="button">上一页</button>' : ''}
        <span>${cur.from + 1}–${Math.min(cur.from + PAGE, cur.total)} / ${cur.total}</span>
        ${hasNext ? '<button id="rd-next" class="ghost sm" type="button">下一页</button>' : ''}
      </div>`;

    CD.$('rd-back').onclick = close;
    const p = CD.$('rd-prev');
    const n = CD.$('rd-next');
    if (p) p.onclick = () => { cur.from -= PAGE; load(); };
    if (n) n.onclick = () => { cur.from += PAGE; load(); };

    const t = document.getElementById('seq-' + cur.target);
    if (t) t.scrollIntoView({ block: 'center' });
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
        <pre>${CD.esc(m.body)}</pre>
      </div>`;
  }
})();
