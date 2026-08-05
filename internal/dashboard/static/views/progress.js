/* 摘要生成进度。
 *
 * 这个页面存在的理由很具体：摘要队列曾经**卡死两天而无人发现**——
 * 一条会话生成不了、被无限重新入队、把其余 92 条饿死，而顶栏那行
 * 「3157/3250」看起来完全正常。失败计数当时恒为 0，因为失败状态
 * 活不过一轮循环就被重置了。
 *
 * 所以这里的重点不是"好看的进度条"，是**把失败摊开**：哪几条、为什么、
 * 点一下能重试。
 */

(() => {
  let root = null;
  let timer = null;

  async function load() {
    if (!root) return;
    let d;
    try {
      d = await CD.api('/api/summary/progress');
    } catch (e) {
      root.innerHTML = `<p class="err">载入失败：${CD.esc(e.message)}</p>`;
      return;
    }
    render(d);
  }

  function render(d) {
    const c = d.counts || {};
    const total = (c.done || 0) + (c.pending || 0) + (c.running || 0) + (c.failed || 0);
    const pct = total ? Math.round(((c.done || 0) / total) * 100) : 0;

    root.innerHTML = `
      <div class="pg-wrap">
        <div class="pg-bar" role="progressbar" aria-valuenow="${pct}" aria-valuemin="0" aria-valuemax="100">
          <div class="pg-fill" style="width:${pct}%"></div>
        </div>
        <div class="pg-nums">
          ${num('已完成', c.done)}${num('待处理', c.pending)}${num('进行中', c.running)}
          ${num('失败', c.failed, c.failed ? 'bad' : '')}
        </div>
        <div class="pg-meta">${meta(d, c)}</div>
        ${failures(d)}
      </div>`;

    const t = CD.$('pg-toggle');
    if (t) {
      t.onclick = async () => {
        t.disabled = true;
        try {
          await fetch('/api/summary/' + (d.paused ? 'resume' : 'pause'), { method: 'POST' });
          await load();
        } finally { t.disabled = false; }
      };
    }
    root.querySelectorAll('[data-retry]').forEach((b) => {
      b.onclick = async () => {
        b.disabled = true;
        const id = b.dataset.retry;
        await fetch('/api/summary/retry', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(id === 'all' ? { all: true } : { session_id: +id }),
        });
        await load();
      };
    });
    root.querySelectorAll('[data-open]').forEach((a) => {
      a.onclick = (e) => { e.preventDefault(); CD.openSession(+a.dataset.open, 0); };
    });
  }

  const num = (label, v, cls = '') =>
    `<div class="pg-n ${cls}"><b>${v || 0}</b><span>${CD.esc(label)}</span></div>`;

  function meta(d, c) {
    const bits = [];

    // 暂停 / 继续
    bits.push(`<span>${d.paused ? '已暂停' : '生成中'}
      <button id="pg-toggle" class="ghost sm" type="button">${d.paused ? '继续' : '暂停'}</button></span>`);

    // LLM
    if (d.llm_ready === false) bits.push('<span class="bad">本地 LLM 不可用，生成暂停中（索引与检索照常）</span>');

    // 时间窗口
    const w = d.window;
    if (w) {
      if (w.invalid) {
        bits.push(`<span class="bad">时间窗口配置有误（${CD.esc(w.invalid)}），当前按「不限」运行</span>`);
      } else if (!w.conf) {
        bits.push('<span>时间窗口：不限</span>');
      } else if (w.in) {
        bits.push(`<span>时间窗口 ${CD.esc(w.conf)}：<b>正在窗口内</b></span>`);
      } else {
        bits.push(`<span>时间窗口 ${CD.esc(w.conf)}：窗口外，下次 ${CD.fmtTime(w.next_at)} 开始</span>`);
      }
      if (w.enabled === false) bits.push('<span class="bad">摘要生成已在设置里关闭</span>');
    }

    // 吞吐与预计完成
    const r = d.recent || {};
    bits.push(`<span>近一小时完成 ${r.last_hour || 0} 条 · 近一天 ${r.last_24h || 0} 条</span>`);
    // eta_seconds 为 null 表示**算不出来**（还没有吞吐样本），不是「马上完成」。
    // 把 null 显示成 0 分钟是这个项目反复清理的那类错值。
    if (d.eta_seconds == null) {
      bits.push(`<span>预计完成：${(c.pending || 0) + (c.running || 0) ? '暂无法估算' : '已全部完成'}</span>`);
    } else {
      bits.push(`<span>预计还需 <b>${CD.esc(dur(d.eta_seconds))}</b></span>`);
    }
    return bits.map((b) => `<div>${b}</div>`).join('');
  }

  function dur(sec) {
    if (sec < 90) return `${sec} 秒`;
    if (sec < 5400) return `${Math.round(sec / 60)} 分钟`;
    const h = Math.floor(sec / 3600);
    const m = Math.round((sec % 3600) / 60);
    return m ? `${h} 小时 ${m} 分钟` : `${h} 小时`;
  }

  function failures(d) {
    const items = d.failures || [];
    if (!items.length) return '';
    const more = d.failed_total > items.length
      ? `<p class="hint">共 ${d.failed_total} 条失败，这里显示最近 ${items.length} 条。</p>` : '';
    return `
      <div class="pg-fail">
        <div class="pg-fail-head">
          <b>失败 ${d.failed_total} 条</b>
          <button class="ghost sm" type="button" data-retry="all">全部重试</button>
        </div>
        ${items.map((f) => `
          <div class="pg-f">
            <a href="#" data-open="${f.session_id}">${CD.esc(CD.sessionTitle(f))}</a>
            <span class="err-inline">${CD.esc(f.err)}</span>
            <span class="muted sm">试了 ${f.attempts} 次 · ${CD.fmtTime(f.updated_at)}</span>
            <button class="ghost sm" type="button" data-retry="${f.session_id}">重试</button>
          </div>`).join('')}
        ${more}
      </div>`;
  }

  CD.register('progress', {
    mount(el) {
      root = el;
      load();
      // 复用既有的 10 秒节奏，不另起一套心跳
      timer = setInterval(load, 10000);
    },
    unmount() {
      if (timer) clearInterval(timer);
      timer = null;
      root = null;
    },
  });
})();
