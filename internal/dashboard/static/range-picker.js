/* 过滤栏的「消息时间」范围选择器（R30）。
 *
 * 为什么自研：原生 <input type="date"> 的显示格式由**浏览器语言**决定（Chrome 给 mm/dd/yyyy），
 * lang 属性改不了；两个独立的框也表达不了「最近 7 天」。更要紧的是语义——这里筛的是
 * **消息时间**（后端 filters() 作用于 b.ts），不是会话时间，而原生控件上一个字都写不了。
 *
 * 上半 CD.rangeMath 是纯日期逻辑，不碰 DOM，可以在 node 里跑（range-picker_test.mjs）；
 * 下半 CD.range 是组件，只在有 document 时初始化。状态源始终是 CD.query.from / to
 * （'YYYY-MM-DD' 或空串），URL 与 API 的契约一字不改。
 */
globalThis.CD ??= {};
(() => {
  const pad = (n) => String(n).padStart(2, '0');

  const M = {
    // 本地年月日。**不用 toISOString**：那是 UTC——本机 UTC+7 凌晨点「今天」会得到昨天。
    localDay(d) {
      return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    },
    // 严格 YYYY-MM-DD 且日历上存在（2 月 30 日不算）；不合法返回 null。
    parseDay(s) {
      const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s || '');
      if (!m) return null;
      const y = +m[1], mo = +m[2], d = +m[3];
      const dt = new Date(y, mo - 1, d);
      if (dt.getFullYear() !== y || dt.getMonth() !== mo - 1 || dt.getDate() !== d) return null;
      return { y, m: mo, d };
    },
    toDate(day) {
      const p = M.parseDay(day);
      return p ? new Date(p.y, p.m - 1, p.d) : null;
    },
    shift(day, n) {
      const d = M.toDate(day);
      if (!d) return '';
      d.setDate(d.getDate() + n);
      return M.localDay(d);
    },
    // 预设全部以 now 的本地日历为准；'all' 清空。
    preset(name, now = new Date()) {
      const today = M.localDay(now);
      const y = now.getFullYear(), mo = now.getMonth();
      switch (name) {
        case 'today': return { from: today, to: today };
        case 'yesterday': { const d = M.shift(today, -1); return { from: d, to: d }; }
        case '7d': return { from: M.shift(today, -6), to: today };
        case '30d': return { from: M.shift(today, -29), to: today };
        case 'month': return { from: M.localDay(new Date(y, mo, 1)), to: today };
        case 'lastMonth': return { from: M.localDay(new Date(y, mo - 1, 1)), to: M.localDay(new Date(y, mo, 0)) };
        default: return { from: '', to: '' };
      }
    },
    // 先大后小自动交换（两点选天然会出现，不算错误）。YYYY-MM-DD 可以直接按字符串比。
    normalize(from, to) {
      from = from || ''; to = to || '';
      return from && to && from > to ? { from: to, to: from } : { from, to };
    },
    label(from, to) {
      if (!from && !to) return '全部';
      if (from && to) return from === to ? from : `${from} → ${to}`;
      return from ? `${from} →` : `→ ${to}`;
    },
    // 周一起始的 6×7 格；m 取 1–12。
    monthGrid(y, m) {
      const first = new Date(y, m - 1, 1);
      const offset = (first.getDay() + 6) % 7;
      return Array.from({ length: 42 }, (_, i) => {
        const d = new Date(y, m - 1, 1 - offset + i);
        return { day: M.localDay(d), inMonth: d.getMonth() === m - 1 };
      });
    },
  };
  CD.rangeMath = M;

  if (typeof document === 'undefined') return; // node 里只要纯函数

  const PRESETS = [
    ['today', '今天'], ['yesterday', '昨天'], ['7d', '最近 7 天'], ['30d', '最近 30 天'],
    ['month', '本月'], ['lastMonth', '上月'], ['all', '全部'],
  ];
  const R = { btn: null, pop: null, view: { y: 0, m: 0 }, anchor: '', focusDay: '', open: false };

  R.init = () => {
    R.btn = CD.$('range-btn');
    R.pop = CD.$('range-pop');
    if (!R.btn || !R.pop) return;
    R.btn.onclick = () => (R.open ? R.close() : R.show());
    R.btn.addEventListener('keydown', (e) => { if (e.key === 'Escape' && R.open) R.close(); });
    R.pop.addEventListener('keydown', R.onKey);
    // 点面板外关闭。面板里没有再嵌套的浮层，所以「在不在容器里」这一条判据够用。
    document.addEventListener('mousedown', (e) => {
      if (R.open && !CD.$('range').contains(e.target)) R.close(false);
    });
    R.sync();
  };

  // 从 CD.query 回填：按钮文案、面板所在月。谁改了 query 都要走这里（syncFilterInputs 会调）。
  R.sync = () => {
    if (!R.btn) return;
    const has = !!(CD.query.from || CD.query.to);
    R.btn.textContent = '消息时间：' + M.label(CD.query.from, CD.query.to);
    R.btn.classList.toggle('on', has);
    const base = M.toDate(CD.query.from) || M.toDate(CD.query.to) || new Date();
    R.view = { y: base.getFullYear(), m: base.getMonth() + 1 };
    if (R.open) R.render();
  };

  R.show = () => {
    R.open = true; R.anchor = ''; R.focusDay = '';
    R.pop.hidden = false;
    R.btn.setAttribute('aria-expanded', 'true');
    R.render();
    R.pop.querySelector('#range-from')?.focus();
  };
  R.close = (focusBtn = true) => {
    R.open = false; R.anchor = ''; R.focusDay = '';
    R.pop.hidden = true;
    R.btn.setAttribute('aria-expanded', 'false');
    if (focusBtn) R.btn.focus();
  };

  // 生效：与别处改筛选同一条链（setFilter → sync → 侧栏 → URL replace → 刷新）。
  R.apply = (from, to) => {
    CD.setFilter(M.normalize(from, to));
    CD.syncFilterInputs();
    CD.renderSide();
    CD.route.write({ replace: true });
    CD.refresh();
    R.close();
  };
  R.applyPreset = (name) => {
    const p = M.preset(name, new Date());
    R.apply(p.from, p.to);
  };

  R.pick = (day) => {
    if (!R.anchor) { R.anchor = day; R.focusDay = day; R.render(); return; }
    R.apply(R.anchor, day);
  };

  // 手输：合法才生效；非法标红并保留，不许静默忽略。
  R.typed = () => {
    const f = R.pop.querySelector('#range-from'), t = R.pop.querySelector('#range-to');
    let ok = true;
    for (const el of [f, t]) {
      const v = el.value.trim();
      const bad = v !== '' && !M.parseDay(v);
      el.classList.toggle('bad', bad);
      el.setAttribute('aria-invalid', bad ? 'true' : 'false');
      if (bad) ok = false;
    }
    if (ok) R.apply(f.value.trim(), t.value.trim());
  };

  R.onKey = (e) => {
    if (e.key === 'Escape') { e.preventDefault(); R.close(); return; }
    const t = e.target;
    if (!t.classList?.contains('d')) return;
    const step = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 }[e.key];
    let day = t.dataset.day;
    if (step != null) day = M.shift(day, step);
    else if (e.key === 'PageUp' || e.key === 'PageDown') {
      const d = M.toDate(day);
      d.setMonth(d.getMonth() + (e.key === 'PageUp' ? -1 : 1));
      day = M.localDay(d);
    } else return;
    e.preventDefault();
    const p = M.parseDay(day);
    R.view = { y: p.y, m: p.m };
    R.focusDay = day;
    R.render();
  };

  R.render = () => {
    const { y, m } = R.view;
    const today = M.localDay(new Date());
    const from = CD.query.from, to = CD.query.to;
    const sel = (d) => (from && to ? d >= from && d <= to : from ? d >= from : to ? d <= to : false);
    const focus = R.focusDay || (M.toDate(from) && M.parseDay(from).y === y && M.parseDay(from).m === m ? from : `${y}-${pad(m)}-01`);
    const cells = M.monthGrid(y, m).map(({ day, inMonth }) => {
      const cls = ['d', inMonth ? 'in' : 'out', day === today ? 'today' : '', day === from || day === to ? 'edge' : '',
        sel(day) ? 'sel' : '', day === R.anchor ? 'anchor' : ''].filter(Boolean).join(' ');
      return `<button type="button" class="${cls}" data-day="${CD.esc(day)}" tabindex="${day === focus ? 0 : -1}" aria-label="${CD.esc(day)}">${+day.slice(8)}</button>`;
    }).join('');
    R.pop.innerHTML = `
      <div class="range-presets">${PRESETS.map(([k, l]) => `<button type="button" class="ghost sm" data-preset="${k}">${CD.esc(l)}</button>`).join('')}</div>
      <div class="range-main">
        <div class="range-inputs">
          <input id="range-from" type="text" inputmode="numeric" placeholder="YYYY-MM-DD" value="${CD.esc(from)}" aria-label="起始日期">
          <span>→</span>
          <input id="range-to" type="text" inputmode="numeric" placeholder="YYYY-MM-DD" value="${CD.esc(to)}" aria-label="结束日期">
        </div>
        <div class="range-nav">
          <button type="button" class="ghost sm" data-nav="-1" aria-label="上个月">‹</button>
          <span class="range-ym">${y} 年 ${m} 月</span>
          <button type="button" class="ghost sm" data-nav="1" aria-label="下个月">›</button>
        </div>
        <div class="range-wd">${['一', '二', '三', '四', '五', '六', '日'].map((w) => `<span>${w}</span>`).join('')}</div>
        <div class="range-grid">${cells}</div>
        <div class="range-hint">${R.anchor ? `起点 ${CD.esc(R.anchor)}，再点一天作为终点` : '筛的是消息时间：只看这段日子里写的消息'}</div>
      </div>`;
    R.pop.querySelectorAll('[data-preset]').forEach((el) => (el.onclick = () => R.applyPreset(el.dataset.preset)));
    R.pop.querySelectorAll('[data-nav]').forEach((el) => (el.onclick = () => {
      const d = new Date(R.view.y, R.view.m - 1 + +el.dataset.nav, 1);
      R.view = { y: d.getFullYear(), m: d.getMonth() + 1 };
      R.focusDay = '';
      R.render();
    }));
    R.pop.querySelectorAll('.d').forEach((el) => (el.onclick = () => R.pick(el.dataset.day)));
    for (const id of ['range-from', 'range-to']) {
      const el = R.pop.querySelector('#' + id);
      el.onchange = R.typed;
      el.onkeydown = (e) => { if (e.key === 'Enter') { e.preventDefault(); R.typed(); } };
    }
    if (R.focusDay) R.pop.querySelector(`.d[data-day="${R.focusDay}"]`)?.focus();
  };

  CD.range = R;
})();
