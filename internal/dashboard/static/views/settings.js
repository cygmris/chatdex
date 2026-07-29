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

    root.querySelectorAll('[data-key]').forEach((el) => {
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
