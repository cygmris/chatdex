/* 摘要视图。
 *
 * ⛔ 这里**没有任何检索代码**——它就是 kind=summary 的既有查询（需求 3.2 明文）。
 * 摘要是本项目信息密度最高的一层数据（3000+ 条，每条一句话说清「这次在干嘛」），
 * 但在 R1 里它只是检索结果的附属行，没有入口能直接翻。
 */

(() => {
  const PAGE = 30;
  let root = null;
  let offset = 0;

  async function load() {
    if (!root) return;
    root.innerHTML = '<p class="hint">载入中…</p>';
    try {
      // 强制 kind=summary：视图内的搜索也只在摘要文本里匹配
      const res = await CD.api('/api/search?' +
        CD.queryParams({ kind: 'summary', limit: PAGE, offset }));
      render(res);
    } catch (e) {
      root.innerHTML = `<p class="err">载入失败：${CD.esc(e.message)}</p>`;
    }
  }

  function render(res) {
    const list = res.sessions || [];
    if (!list.length) {
      root.innerHTML = res.no_match
        ? '<p class="hint">摘要里没有匹配的词。摘要用概念词重写原文，可以试试更概括的说法。</p>'
        : '<p class="hint">还没有摘要。它由本地 LLM 在后台生成，进度见右上角。</p>';
      return;
    }

    root.innerHTML =
      // 条数来自 /api/stats，可能还没回来（视图先于它挂载）——
      // 留个坑给 loadStats 回填，而不是在这里显示一个假的 0
      `<p class="hint digest-lede">共 <span id="dg-count">${CD.summaryCount || '…'}</span> 条摘要。${CD.query.q ? '当前只在摘要文本内匹配。' : '按时间倒序。'}</p>` +
      list.map((s) => `
        <article class="hit" data-id="${s.id}">
          <p class="sum${s.title ? ' named' : ''}">${
            CD.query.q ? CD.escHit(s.snippet) : CD.esc(CD.sessionTitle(s))}</p>${
            !CD.query.q && CD.sessionSubtitle(s) ? `<p class="sub-sum">${CD.esc(CD.sessionSubtitle(s))}</p>` : ''}
          <div class="foot">
            <span class="badge">${s.source === 'codex' ? 'CODEX' : 'CLAUDE'}</span>
            ${s.agent_label ? '<span class="badge">子代理</span>' : ''}
            <span class="path">${CD.esc(s.project_path || '（未知项目）')}</span>
            <span>${CD.fmtRange(s.started_at, s.ended_at)}</span>
            <span>${s.msg_count} 条</span>
            ${s.summary_model
              ? `<span title="摘要由本地模型生成，可信度取决于用了哪个模型、什么时候写的">${CD.esc(s.summary_model)} · ${CD.fmtTime(s.summary_at)}</span>`
              : ''}
          </div>
        </article>`).join('') +
      `<div class="pager">
        ${offset > 0 ? '<button id="dg-prev" class="ghost sm" type="button">上一页</button>' : ''}
        <span>${offset + 1}–${offset + list.length}</span>
        ${list.length === PAGE ? '<button id="dg-more" class="ghost sm" type="button">下一页</button>' : ''}
      </div>`;

    root.querySelectorAll('.hit').forEach((el) =>
      CD.clickable(el, () => CD.openSession(+el.dataset.id, 0)));
    const p = CD.$('dg-prev');
    const m = CD.$('dg-more');
    if (p) p.onclick = () => { offset = Math.max(0, offset - PAGE); load(); };
    if (m) m.onclick = () => { offset += PAGE; load(); };
  }

  CD.register('digest', {
    mount(el) { root = el; offset = 0; load(); },
    refresh() { offset = 0; load(); },
  });
})();
