/* 检索视图。
 *
 * 信息层级按定案稿重排：摘要是条目主标题（「这次在干嘛」），
 * 命中片段降为证据（「为什么匹配」），元信息压成一行。
 * R1 里这三者是等重的灰字，主次颠倒了。
 */

(() => {
  const PAGE = 20;
  let root = null;
  let offset = 0;

  function mount(el) {
    root = el;
    offset = 0;
    root.innerHTML = '<p class="hint">检索中…</p>';
    load();
  }

  function refresh() {
    offset = 0;
    load();
  }

  async function load() {
    if (!root) return;
    root.innerHTML = '<p class="hint">检索中…</p>';
    try {
      const res = await CD.api('/api/search?' + CD.queryParams({ limit: PAGE, offset }));
      render(res);
    } catch (e) {
      root.innerHTML = `<p class="err">检索失败：${CD.esc(e.message)}</p>`;
    }
  }

  function render(res) {
    const list = res.sessions || [];
    if (!list.length) {
      root.innerHTML = res.no_match
        ? '<p class="hint">无命中。换个说法试试——检索按关键词字面匹配，不会返回近似结果。</p>'
        : '<p class="hint">没有符合条件的会话。</p>';
      return;
    }

    root.innerHTML =
      list.map((s) => {
        // 主标题优先用户 /rename 的名字，其次摘要；都没有才退回片段
        const sub = CD.sessionSubtitle(s);
        const head = (s.title || s.summary)
          ? `<p class="sum${s.title ? ' named' : ''}">${CD.esc(CD.sessionTitle(s))}</p>` +
            (sub ? `<p class="sub-sum">${CD.esc(sub)}</p>` : '')
          : '';
        const best = s.best_kind
          ? `${CD.esc(CD.KIND_LABEL[s.best_kind] || s.best_kind)}${s.best_tool ? '·' + CD.esc(s.best_tool) : ''}`
          : '';
        return `
        <article class="hit" data-id="${s.id}" data-seq="${s.best_seq || 0}">
          ${head}
          <p class="snip">${CD.escHit(s.snippet)}</p>
          <div class="foot">
            <span class="badge">${s.source === 'codex' ? 'CODEX' : 'CLAUDE'}</span>
            ${s.is_sub ? '<span class="badge sub">子代理</span>' : ''}
            <span class="path">${CD.esc(s.project_path || '（未知项目）')}</span>
            <span>${CD.fmtRange(s.started_at, s.ended_at)}</span>
            <span>${s.msg_count} 条</span>
            ${s.hits ? `<span>命中 ${s.hits}</span>` : ''}
            ${best ? `<span>最佳：${best}</span>` : ''}
          </div>
        </article>`;
      }).join('') +
      `<div class="pager">
        ${offset > 0 ? '<button id="prev" class="ghost sm" type="button">上一页</button>' : ''}
        <span>${offset + 1}–${offset + list.length}</span>
        ${list.length === PAGE ? '<button id="more" class="ghost sm" type="button">下一页</button>' : ''}
      </div>`;

    root.querySelectorAll('.hit').forEach((el) =>
      CD.clickable(el, () => CD.openSession(+el.dataset.id, +el.dataset.seq)));
    const prev = CD.$('prev');
    const more = CD.$('more');
    if (prev) prev.onclick = () => { offset = Math.max(0, offset - PAGE); load(); };
    if (more) more.onclick = () => { offset += PAGE; load(); };
  }

  CD.register('search', { mount, refresh });
})();
