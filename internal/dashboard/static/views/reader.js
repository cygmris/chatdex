/* 会话回读。
 *
 * 不是一个「视图」——它是从检索/时间线/摘要点进来的覆盖层，
 * 关掉后回到原来的视图，过滤条件与滚动位置都还在。
 */

(() => {
  const PAGE = 100;
  // 默认渲染；选择记在 localStorage（属于偏好，不进 URL）
  let raw = localStorage.getItem('chatdex.raw') === '1';
  let cur = { id: 0, from: 0, total: 0, target: 0, back: 'search' };

  // fromRoute=true 表示这次打开是「还原 URL」而不是「用户点击」，
  // 此时不能再写历史，否则后退一次会停在同一个会话上
  CD.openSession = async (id, targetSeq = 0, fromRoute = false) => {
    // 命中在哪一页就从哪一页开始加载
    const from = Math.max(0, Math.floor(targetSeq / PAGE) * PAGE);
    cur = { id, from, total: 0, target: targetSeq, back: CD.view };
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
        <button id="rd-raw" class="ghost sm" type="button"
          title="渲染：按 Markdown 与 ANSI 显示；原文：显示索引时存下的原始字节">${raw ? '◧ 原文' : '◨ 渲染'}</button>
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
    CD.$('rd-raw').onclick = () => {
      raw = !raw;
      localStorage.setItem('chatdex.raw', raw ? '1' : '0');
      load();   // 重渲染这一页；分页位置与目标锚点都在 cur 里，不会丢
    };
    const p = CD.$('rd-prev');
    const n = CD.$('rd-next');
    if (p) p.onclick = () => { cur.from -= PAGE; load(); };
    if (n) n.onclick = () => { cur.from += PAGE; load(); };

    const t = document.getElementById('seq-' + cur.target);
    if (t) t.scrollIntoView({ block: 'center' });
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
      case 'tool_result':
        // 命令输出：可能带 ANSI 颜色，但不是 Markdown
        return `<pre>${CD.ansi(m.body)}</pre>`;
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
