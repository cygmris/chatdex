/* 问一问：本地 LLM 多轮调检索工具。
 *
 * 「展示它实际搜了什么」这一层不能简化掉——那是使用者判断它
 * 有没有搜对方向的唯一途径（需求 10.7 是 R1 定的）。
 */

(() => {
  let root = null;
  let ready = false;

  const TOOL_LABEL = { search_sessions: '检索', get_session: '读会话', list_projects: '看项目列表' };

  function describeArgs(a = {}) {
    const parts = [];
    if (a.query) parts.push(`「${a.query}」`);
    if (a.kind) parts.push(`类型=${a.kind}`);
    if (a.tool_name) parts.push(`工具=${a.tool_name}`);
    if (a.source) parts.push(`来源=${a.source}`);
    if (a.project) parts.push(`项目=${a.project}`);
    if (a.session_id) parts.push(`会话 #${a.session_id}`);
    if (a.from_seq) parts.push(`从第 ${a.from_seq} 条`);
    return parts.join(' · ');
  }

  async function mount(el) {
    root = el;
    root.innerHTML = `
      <div class="chat">
        <p id="chat-off" class="hint" hidden></p>
        <form id="chat-form" class="chat-form">
          <input id="chat-q" type="text" placeholder="用大白话问，如：我记得做过一个增量备份的项目，在哪个会话？">
          <button id="chat-send" type="submit">问</button>
        </form>
        <div id="chat-log"></div>
      </div>`;

    CD.$('chat-form').onsubmit = async (e) => {
      e.preventDefault();
      const q = CD.$('chat-q').value.trim();
      if (!q) return;
      CD.$('chat-q').value = '';
      CD.$('chat-send').disabled = true;
      try { await ask(q); } finally { CD.$('chat-send').disabled = false; }
    };

    await status();
  }

  async function status() {
    try {
      const s = await CD.api('/api/chat/status');
      ready = !!s.available;
      CD.$('chat-form').hidden = !ready;
      CD.$('chat-off').hidden = ready;
      if (!ready) {
        CD.$('chat-off').textContent =
          `聊天不可用：${s.reason || '本地 LLM 未就绪'}。检索、时间线与摘要不受影响。`;
      }
    } catch {
      ready = false;
      CD.$('chat-form').hidden = true;
      CD.$('chat-off').hidden = false;
      CD.$('chat-off').textContent = '聊天状态未知（服务未响应）。';
    }
  }

  async function ask(question) {
    const log = CD.$('chat-log');
    const turn = document.createElement('div');
    turn.className = 'turn';
    turn.innerHTML = `<div class="q">${CD.esc(question)}</div><div class="steps"></div><div class="a md"></div>`;
    log.prepend(turn);
    const steps = turn.querySelector('.steps');
    const answer = turn.querySelector('.a');

    const resp = await fetch('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ question }),
    });
    if (!resp.ok) {
      answer.innerHTML = `<span class="err-inline">${CD.esc(await resp.text())}</span>`;
      return;
    }

    const reader = resp.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    let lastStep = null;

    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      const chunks = buf.split('\n\n');
      buf = chunks.pop();
      for (const c of chunks) {
        const line = c.split('\n').find((l) => l.startsWith('data: '));
        if (!line) continue;
        let e;
        try { e = JSON.parse(line.slice(6)); } catch { continue; }

        if (e.type === 'tool') {
          lastStep = document.createElement('div');
          lastStep.className = 'step';
          lastStep.innerHTML =
            `<span class="round">第 ${e.round} 轮</span> ${CD.esc(TOOL_LABEL[e.tool] || e.tool)} ` +
            `<span class="args">${CD.esc(describeArgs(e.args))}</span> <span class="hits">…</span>`;
          steps.append(lastStep);
        } else if (e.type === 'tool_result' && lastStep) {
          lastStep.querySelector('.hits').textContent = e.hits ? `→ ${e.hits} 条` : '→ 无命中';
        } else if (e.type === 'note') {
          const n = document.createElement('div');
          n.className = 'step note';
          n.textContent = e.text;
          steps.append(n);
        } else if (e.type === 'answer') {
          answer.innerHTML = CD.md(e.text || '（无内容）');
          CD.linkifySessions(answer);
          answer.querySelectorAll('.sess-link').forEach((el) =>
            (el.onclick = (ev) => { ev.preventDefault(); CD.openSession(+el.dataset.id, 0); }));
        }
      }
    }
  }

  CD.register('chat', { mount, refresh: status });
})();
