/* 设置页。
 *
 * 表单完全由 /api/config 下发的元信息渲染——前端不再写一份字段清单，
 * 否则加了配置项忘了同步，界面上就少一格，而少的那一格不会有任何东西报错。
 */

(() => {
  let root = null;
  let fields = [];
  let values = {};
  let dirty = {};

  async function load() {
    root.innerHTML = '<p class="hint">载入中…</p>';
    let data;
    try {
      data = await CD.api('/api/config');
    } catch (e) {
      root.innerHTML = `<p class="err">读取配置失败：${CD.esc(e.message)}</p>`;
      return;
    }
    fields = data.fields || [];
    values = data.values || {};
    dirty = {};
    render(data);
  }

  function render(data) {
    const groups = [];
    for (const f of fields) {
      let g = groups.find((x) => x.name === f.group);
      if (!g) groups.push((g = { name: f.group, items: [] }));
      g.items.push(f);
    }

    root.innerHTML = `
      <div class="settings">
        <p class="hint set-lede">
          配置文件：<span class="mono">${CD.esc(data.path)}</span>。
          只有改过的项会写进去——默认值不固化，将来调整默认值能自动跟随。
          ${data.model_note ? `<br><span class="set-note">${CD.esc(data.model_note)}</span>` : ''}
        </p>
        ${groups.map((g) => `
          <section class="set-group">
            <h2>${CD.esc(g.name)}</h2>
            ${g.items.map(field).join('')}
          </section>`).join('')}
        <div class="set-actions">
          <button id="set-save" type="button">保存</button>
          <button id="set-reset" class="ghost" type="button">放弃修改</button>
          <span id="set-msg" class="muted"></span>
        </div>
      </div>`;

    bindSources();
    root.querySelectorAll('[data-key]:not([data-kind="sources-part"])').forEach((el) => {
      el.oninput = el.onchange = () => {
        dirty[el.dataset.key] = el.type === 'checkbox' ? el.checked
          : (el.dataset.kind === 'int' || el.dataset.kind === 'bytes') ? Number(el.value)
          : el.value;
        CD.$('set-msg').textContent = '有未保存的修改';
      };
    });
    CD.$('set-save').onclick = save;
    CD.$('set-reset').onclick = load;
  }

  /* 备份源列表编辑器。
   *
   * 与其它字段不同，它的值是对象数组。收集脏值时不能走通用那条路径
   * （那条按 input 的 value 取标量），所以这里自己维护并整体写回 dirty。
   */
  function renderSources(id, key, list) {
    const rows = list.map((s, i) => `
      <div class="src-row" data-i="${i}">
        <input type="checkbox" data-kind="sources-part" data-key="${key}" ${s.enabled ? 'checked' : ''}
               title="是否备份这个目录">
        <input type="text" data-kind="sources-part" data-key="${key}" value="${CD.esc(s.path || '')}"
               placeholder="目录绝对路径">
        <button type="button" class="ghost sm src-del" title="移除这一行">×</button>
      </div>`).join('');
    return `<div class="src-list" id="${id}" data-srckey="${key}">${rows}
      <button type="button" class="ghost sm src-add">+ 添加目录</button></div>`;
  }

  function bindSources() {
    root.querySelectorAll('.src-list').forEach((box) => {
      const key = box.dataset.srckey;
      const collect = () => {
        const out = [...box.querySelectorAll('.src-row')].map((r) => ({
          enabled: r.querySelector('input[type=checkbox]').checked,
          path: r.querySelector('input[type=text]').value.trim(),
        })).filter((s) => s.path !== '');
        dirty[key] = out;
        CD.$('set-msg').textContent = '有未保存的修改';
      };
      box.querySelectorAll('input').forEach((el) => { el.oninput = el.onchange = collect; });
      box.querySelectorAll('.src-del').forEach((b) => {
        b.onclick = () => { b.closest('.src-row').remove(); collect(); };
      });
      const add = box.querySelector('.src-add');
      if (add) {
        add.onclick = () => {
          const row = document.createElement('div');
          row.className = 'src-row';
          row.innerHTML = `<input type="checkbox" checked><input type="text" placeholder="目录绝对路径">`
            + `<button type="button" class="ghost sm src-del">×</button>`;
          box.insertBefore(row, add);
          row.querySelectorAll('input').forEach((el) => { el.oninput = el.onchange = collect; });
          row.querySelector('.src-del').onclick = () => { row.remove(); collect(); };
          row.querySelector('input[type=text]').focus();
        };
      }
    });
  }

  function field(f) {
    const v = valueOf(f.key);
    const id = 'set-' + f.key.replace(/\./g, '-');
    let input;
    if (f.kind === 'bool') {
      input = `<input type="checkbox" id="${id}" data-key="${f.key}" data-kind="bool" ${v ? 'checked' : ''}>`;
    } else if (f.kind === 'enum' && (f.options || []).length) {
      input = `<select id="${id}" data-key="${f.key}" data-kind="enum">
        ${f.options.map((o) => `<option value="${CD.esc(o)}"${o === v ? ' selected' : ''}>${CD.esc(o)}</option>`).join('')}
      </select>`;
    } else if (f.kind === 'enum') {
      // 模型列表拿不到时退化为文本输入，而不是给一个空下拉（需求 4.7）
      input = `<input type="text" id="${id}" data-key="${f.key}" data-kind="string" value="${CD.esc(v)}">`;
    } else if (f.kind === 'sources') {
      // 备份源是「路径 + 勾选」的列表，不是一个标量。
      // 单独一个分支而不是塞进文本框：勾选/取消是这里最主要的操作，
      // 让人去编辑一段 JSON 字符串是把界面的活推给用户。
      input = renderSources(id, f.key, Array.isArray(v) ? v : []);
    } else {
      const type = f.kind === 'int' || f.kind === 'bytes' ? 'number' : 'text';
      input = `<input type="${type}" id="${id}" data-key="${f.key}" data-kind="${f.kind}" value="${CD.esc(v)}"
        ${f.min !== undefined ? `min="${f.min}"` : ''} ${f.max ? `max="${f.max}"` : ''}>`;
    }
    return `
      <div class="set-row${f.hot ? '' : ' cold'}">
        <label for="${id}">${CD.esc(f.label)}${f.hot ? '' : '<span class="set-cold">需重启</span>'}</label>
        <div class="set-input">${input}</div>
        <div class="set-help">${CD.esc(f.help)}${f.note ? `<div class="set-note">${CD.esc(f.note)}</div>` : ''}</div>
      </div>`;
  }

  function valueOf(key) {
    return key.split('.').reduce((o, k) => (o == null ? undefined : o[k]), values) ?? '';
  }

  async function save() {
    const msg = CD.$('set-msg');
    const btn = CD.$('set-save');
    btn.disabled = true;
    msg.textContent = '保存中…';
    root.querySelectorAll('.set-row.bad').forEach((el) => el.classList.remove('bad'));

    // 只把改过的键组装成嵌套结构提交
    const body = {};
    for (const [k, v] of Object.entries(dirty)) {
      const parts = k.split('.');
      let cur = body;
      while (parts.length > 1) {
        const p = parts.shift();
        cur = cur[p] ??= {};
      }
      cur[parts[0]] = v;
    }

    try {
      const resp = await fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (resp.status === 422) {
        const { errors } = await resp.json();
        for (const e of errors || []) {
          const el = root.querySelector(`[data-key="${e.key}"]`);
          if (el) {
            el.closest('.set-row')?.classList.add('bad');
            const help = el.closest('.set-row')?.querySelector('.set-help');
            if (help) help.innerHTML += `<div class="set-bad">${CD.esc(e.msg)}</div>`;
          }
        }
        msg.textContent = `有 ${(errors || []).length} 项不合法，已在下方标出`;
        return;
      }
      if (!resp.ok) {
        msg.textContent = '保存失败：' + CD.esc(await resp.text());
        return;
      }
      // 主题指派立刻应用
      if (dirty['ui.light_theme'] || dirty['ui.dark_theme']) {
        CD.theme.setPick({
          light: dirty['ui.light_theme'] || CD.theme.pick.light,
          dark: dirty['ui.dark_theme'] || CD.theme.pick.dark,
        });
        // 外观类配置保存后立即生效，不必重启（Hot: true）
        if ('ui.highlight' in dirty) CD.setHighlight(dirty['ui.highlight']);
        if ('ui.mermaid_auto' in dirty) CD.mermaidAuto = !!dirty['ui.mermaid_auto'];
      }
      const needRestart = Object.keys(dirty).some(
        (k) => !fields.find((f) => f.key === k)?.hot);
      msg.textContent = needRestart
        ? '已保存。其中有需重启才生效的项：systemctl --user restart chatdex'
        : '已保存并立即生效。';
      await load();
    } finally {
      btn.disabled = false;
    }
  }

  CD.register('settings', {
    mount(el) { root = el; load(); },
    refresh: load,
  });
})();
