/* 自研下拉（combo.js）。
 *
 * 原生 <select> 不能输入即搜：项目实测 300 个、工具名 477 个，列全了翻不动，列前 N 个又选不到。
 * 与 range-picker.js 同一纪律：纯函数 CD.comboMath 可单测（node --test），组件 CD.combo 只管 DOM。
 * 不给宿主元素伪造 .value——读值的地方明改成 CD.combos[id].value。
 *
 * 选项形状：{ value, label, hint? }。value 为 '' 的那项是「全部」。
 */
(() => {
  globalThis.CD ??= {};
  const norm = (s) => String(s ?? '').trim().toLowerCase();

  const M = {
    norm,
    // 前缀命中排在子串命中前，同一档保持原顺序；label 与 value 两个字段都比。
    // 大小写不敏感；中文没有大小写，子串即可。
    filter(options, q) {
      const n = norm(q);
      if (!n) return options.slice();
      const pre = [], sub = [];
      for (const o of options) {
        const l = norm(o.label), v = norm(o.value);
        if (l.startsWith(n) || v.startsWith(n)) pre.push(o);
        else if (l.includes(n) || v.includes(n)) sub.push(o);
      }
      return pre.concat(sub);
    },
    // 高亮项移动：到头回绕；没有选项 -1；还没有高亮时 ↓ 到第一项、↑ 到末项
    move(i, delta, n) {
      if (n <= 0) return -1;
      if (i < 0) return delta > 0 ? 0 : n - 1;
      return (((i + delta) % n) + n) % n;
    },
    // 当前值的显示文案。值不在列表里（深链接先于 /api/projects 返回、配置里是旧模型名）
    // 显示值本身（经 fallback 缩短），绝不能显示成「全部」——那是个会误导人的假象。
    labelOf(options, value, fallback = (v) => v) {
      const hit = options.find((o) => o.value === value);
      if (hit) return hit.label;
      return value === '' ? '' : fallback(value);
    },
    // 允许自定义值：输入的文字不在列表里就加一项「用「q」」，Enter 即生效。
    // 有匹配时放在**末尾**——Enter 默认选的是最佳匹配（输 ba 得 Bash），字面值要 ↑ 一下才到；
    // 没有匹配时它是唯一一项，Enter 直接生效，手敲全名回车的老习惯不变。
    withCustom(options, q) {
      const t = String(q ?? '').trim();
      if (!t || options.some((o) => o.value === t)) return options;
      return [...options, { value: t, label: `用「${t}」`, custom: true }];
    },
  };
  CD.comboMath = M;
  if (typeof document === 'undefined') return;   // node --test 只要纯函数

  CD.combos = {};
  const opened = new Set();
  // 一个 document 级监听关掉所有打开的实例；浮层里没有再嵌套的浮层，「在不在宿主里」这一条够用
  document.addEventListener('mousedown', (e) => {
    for (const c of [...opened]) if (!c.host.contains(e.target)) c.close(false);
  });

  let seq = 0;
  CD.combo = {
    /* opts: { options, value, empty, prefix, search:'auto'|bool, custom, title, btnId, placeholder,
     *         labelFor(v), onOpen(c), onChange(v) } */
    mount(host, opts = {}) {
      const id = host.id || `combo-${++seq}`;
      const listId = `${id}-list`;
      const c = { host, opts, value: opts.value ?? '', options: opts.options ?? [], isOpen: false, q: '', active: -1, shown: [] };
      const searchable = () => opts.search === true || (opts.search !== false && c.options.length >= 8);
      const fallback = opts.labelFor || ((v) => v);

      host.classList.add('combo');
      host.innerHTML = `<button type="button" class="combo-btn"${opts.btnId ? ` id="${CD.esc(opts.btnId)}"` : ''}
          aria-haspopup="listbox" aria-expanded="false"${opts.title ? ` title="${CD.esc(opts.title)}"` : ''}></button>
        <div class="combo-pop" hidden></div>`;
      c.btn = host.querySelector('.combo-btn');
      c.pop = host.querySelector('.combo-pop');

      const paintBtn = () => {
        const l = M.labelOf(c.options, c.value, fallback);
        c.btn.textContent = (opts.prefix || '') + (l === '' ? (opts.empty || '') : l);
        c.btn.classList.toggle('on', c.value !== '');
      };
      const visible = () => {
        const list = M.filter(c.options, c.q);
        return opts.custom ? M.withCustom(list, c.q) : list;
      };
      // 只重画列表，不动搜索框：每敲一个字就重建输入框会丢焦点与光标
      const renderList = () => {
        const list = visible();
        c.shown = list;
        if (c.active >= list.length) c.active = list.length - 1;
        if (c.active < 0 && list.length) c.active = Math.max(0, list.findIndex((o) => o.value === c.value));
        c.list.innerHTML = list.map((o, i) =>
          `<li role="option" id="${listId}-${i}" data-value="${CD.esc(o.value)}" aria-selected="${o.value === c.value}"
             class="${i === c.active ? 'active' : ''}${o.custom ? ' custom' : ''}">
             <span class="combo-label">${CD.esc(o.label)}</span>${o.hint != null ? `<span class="combo-hint">${CD.esc(String(o.hint))}</span>` : ''}</li>`
        ).join('') || '<li class="combo-none" aria-disabled="true">没有匹配</li>';
        c.list.querySelectorAll('li[data-value]').forEach((li) => {
          li.onmousedown = (e) => e.preventDefault();   // 别让点击把焦点从搜索框抢走
          li.onclick = () => c.pick(li.dataset.value);
        });
        const act = c.list.querySelector('li.active');
        if (c.input) c.input.setAttribute('aria-activedescendant', act?.id || '');
        act?.scrollIntoView({ block: 'nearest' });
      };

      c.show = () => {
        if (c.isOpen) return;
        opts.onOpen?.(c);   // 工具名列表在这里第一次拉
        c.isOpen = true; c.q = ''; c.active = -1;
        const s = searchable();
        c.pop.innerHTML = `${s ? `<input class="combo-search" type="text" role="combobox" aria-autocomplete="list"
            aria-controls="${listId}" aria-expanded="true" placeholder="${CD.esc(opts.placeholder || '输入过滤…')}">` : ''}
          <ul class="combo-list" role="listbox" id="${listId}" tabindex="${s ? -1 : 0}"></ul>`;
        c.input = c.pop.querySelector('.combo-search');
        c.list = c.pop.querySelector('.combo-list');
        if (c.input) c.input.oninput = () => { c.q = c.input.value; c.active = 0; renderList(); };
        c.pop.hidden = false;
        c.btn.setAttribute('aria-expanded', 'true');
        renderList();
        // 右缘：浮层会出视口就右对齐到按钮（#tool 在过滤栏最右）
        c.pop.classList.remove('flip');
        if (c.btn.getBoundingClientRect().left + c.pop.offsetWidth > window.innerWidth - 12) c.pop.classList.add('flip');
        opened.add(c);
        (c.input || c.list).focus();
      };
      c.close = (focusBtn = true) => {
        if (!c.isOpen) return;
        c.isOpen = false;
        c.pop.hidden = true;
        c.btn.setAttribute('aria-expanded', 'false');
        opened.delete(c);
        if (focusBtn) c.btn.focus();
      };
      // set 是「回填」：改显示不触发 onChange。谁改了 CD.query 都要走这里（syncFilterInputs 会调）
      c.set = (v) => { c.value = v ?? ''; paintBtn(); if (c.isOpen) renderList(); };
      c.setOptions = (list) => { c.options = list || []; paintBtn(); if (c.isOpen) renderList(); };
      c.pick = (v) => {
        const changed = v !== c.value;
        c.set(v);
        c.close();
        if (changed) opts.onChange?.(v);
      };

      c.pop.addEventListener('keydown', (e) => {
        const n = c.shown.length;
        switch (e.key) {
          case 'ArrowDown': case 'ArrowUp':
            e.preventDefault(); c.active = M.move(c.active, e.key === 'ArrowDown' ? 1 : -1, n); renderList(); return;
          case 'Home': case 'End':
            e.preventDefault(); c.active = n ? (e.key === 'Home' ? 0 : n - 1) : -1; renderList(); return;
          case 'Enter':
            e.preventDefault(); if (n) c.pick(c.shown[Math.max(0, c.active)].value); return;
          case 'Escape':
            e.preventDefault(); c.close(); return;
          case 'Tab':
            c.close(false); return;
          default:
        }
      });
      c.btn.onclick = () => (c.isOpen ? c.close() : c.show());
      c.btn.onkeydown = (e) => { if (e.key === 'ArrowDown' && !c.isOpen) { e.preventDefault(); c.show(); } };

      paintBtn();
      CD.combos[id] = c;
      return c;
    },
  };
})();
