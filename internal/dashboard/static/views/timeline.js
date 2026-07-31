/* 时间线：按项目聚合，与检索并列而非取代（需求 9.1 是 R1 定的，这里只换皮）。 */

(() => {
  let root = null;

  async function load() {
    if (!root) return;
    root.innerHTML = '<p class="hint">载入中…</p>';
    try {
      const gs = await CD.api('/api/timeline?' + CD.queryParams({ limit: 200 }));
      render(gs || []);
    } catch (e) {
      root.innerHTML = `<p class="err">时间线载入失败：${CD.esc(e.message)}</p>`;
    }
  }

  function render(gs) {
    if (!gs.length) {
      root.innerHTML = '<p class="hint">没有符合条件的会话。</p>';
      return;
    }
    root.innerHTML = gs.map((g) => `
      <section class="group">
        <h2 class="group-head">
          <span class="mono">${CD.esc(g.project_path)}</span>
          <span class="muted">${g.total} 个会话${g.total > g.sessions.length ? `（展开 ${g.sessions.length}）` : ''}</span>
        </h2>
        ${g.sessions.map((s) => `
          <button class="tl" type="button" data-id="${s.id}">
            <span class="badge">${s.source === 'codex' ? 'CODEX' : 'CLAUDE'}</span>
            ${s.agent_label ? '<span class="badge">子代理</span>' : ''}
            <span class="tl-time mono">${CD.fmtRange(s.started_at, s.ended_at)}</span>
            <span class="tl-n mono">${s.msg_count} 条</span>
            <span class="tl-label${s.title ? ' named' : s.has_summary ? ' from-summary' : ''}">${CD.esc(s.label || '（无内容）')}</span>
          </button>`).join('')}
      </section>`).join('');

    root.querySelectorAll('.tl').forEach((el) =>
      CD.clickable(el, () => CD.openSession(+el.dataset.id, 0)));
  }

  CD.register('timeline', {
    mount(el) { root = el; load(); },
    refresh: load,
  });
})();
