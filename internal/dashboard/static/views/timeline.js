/* 时间线：按项目聚合，与检索并列而非取代（需求 9.1 是 R1 定的）。
 *
 * 分页单位是**项目**，不是会话。旧实现按会话取最新 200 条再分组，
 * 实测 118 个项目里只看得到 6 个 —— 而界面上没有任何迹象说还有 112 个。
 */

(() => {
  let root = null;

  // 一页多少个项目。20 是「一屏能扫完、又不至于翻太多次」的折中：
  // 本机 118 个项目 = 6 页。
  const PER_PAGE = 20;

  const curPage = () => Math.max(1, +CD.query.page || 1);

  async function load() {
    if (!root) return;
    root.innerHTML = '<p class="hint">载入中…</p>';
    const page = curPage();
    try {
      const res = await CD.api('/api/timeline?' + CD.queryParams({
        limit: PER_PAGE, offset: (page - 1) * PER_PAGE,
      }));
      render(res || {});
    } catch (e) {
      root.innerHTML = `<p class="err">时间线载入失败：${CD.esc(e.message)}</p>`;
    }
  }

  function goto(page) {
    // page=1 不写进 URL：?view=timeline 比 ?view=timeline&page=1 好读也好分享
    CD.query.page = page > 1 ? String(page) : '';
    CD.route.write();
    load();
  }

  // 页码条。
  //
  // 启禁判据是「**这个按钮要去的那一页，是不是一个和当前不同的合法页**」，
  // 不是「这个按钮的方向是前还是后」。差别在越界那一页上会要命：
  // 从 ?page=99 进来（改了 URL、或者筛选变窄之前存的链接），按方向判会把
  // 「末页」也禁掉 —— 而那时候「末页」恰恰是**往回**走的那个按钮，
  // 于是人被困在一个空页上，除了手改 URL 没有出路。实测踩到过。
  function pager(page, pages, total, shown) {
    if (total === 0) return '';
    const btn = (target, label) => {
      const dead = target === page || target < 1 || target > pages;
      return `<button type="button" class="ghost sm" data-page="${target}"${dead ? ' disabled' : ''}>${label}</button>`;
    };
    const range = shown > 0
      ? `第 ${(page - 1) * PER_PAGE + 1}–${(page - 1) * PER_PAGE + shown} 个`
      : '这一页是空的';
    return `
      <nav class="tl-pager">
        <span class="muted">共 ${total} 个项目 · ${range}</span>
        <span class="tl-pager-btns">
          ${btn(1, '首页')}${btn(page - 1, '上一页')}
          <span class="mono">${page} / ${pages}</span>
          ${btn(page + 1, '下一页')}${btn(pages, '末页')}
        </span>
      </nav>`;
  }

  function render(res) {
    const gs = res.groups || [];
    const total = res.project_total || 0;
    const pages = Math.max(1, Math.ceil(total / PER_PAGE));
    const page = curPage();

    if (!gs.length) {
      // 「翻过头了」与「筛完就是没有」在列表上完全同形，必须靠 project_total 分开说。
      root.innerHTML = total > 0
        ? `<p class="hint">这一页没有项目了 —— 一共 ${total} 个项目，共 ${pages} 页。</p>`
          + pager(page, pages, total, 0)
        : '<p class="hint">没有符合条件的会话。</p>';
      bindPager();
      return;
    }

    root.innerHTML = pager(page, pages, total, gs.length) + gs.map((g) => `
      <section class="group">
        <h2 class="group-head">
          <span class="mono">${CD.esc(g.project_path)}</span>
          <span class="muted">${g.total} 个会话${g.total > g.sessions.length ? `（展开 ${g.sessions.length}）` : ''}</span>
        </h2>
        ${g.sessions.map((s) => `
          <button class="tl" type="button" data-id="${s.id}" data-seq="${s.target_seq || 0}">
            <span class="badge">${CD.sourceLabel(s.source)}</span>
            ${s.is_sub ? '<span class="badge sub">子代理</span>' : ''}
            <span class="tl-time mono">${CD.fmtRange(s.started_at, s.ended_at)}</span>
            <span class="tl-n mono">${s.msg_count} 条</span>
            <span class="tl-label${s.title ? ' named' : s.has_summary ? ' from-summary' : ''}">${CD.esc(s.label || '（无内容）')}</span>
          </button>`).join('')}
      </section>`).join('') + pager(page, pages, total, gs.length);

    root.querySelectorAll('.tl').forEach((el) =>
      CD.clickable(el, () => CD.openSession(+el.dataset.id, +el.dataset.seq)));
    bindPager();
  }

  function bindPager() {
    root.querySelectorAll('[data-page]').forEach((el) =>
      (el.onclick = () => goto(+el.dataset.page)));
  }

  CD.register('timeline', {
    mount(el) { root = el; load(); },
    refresh: load,
  });
})();
