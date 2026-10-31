#!/usr/bin/env python3
"""冻结检索域与文档：两个引擎读**完全同一份**文档。

为什么要采样：Ollama 落在 CPU 上（4070 因驱动版本不匹配没被用上），
实测 ~800 字符/秒，全量 54M 字符要 19 小时。缩到 400 个会话 × 7.5K 字符 ≈ 1 小时。
采样是**均匀跨全会话**取片段，不是只取开头——只取开头会让「结论在后半段」的会话系统性失分。
"""
import sqlite3, os, json, random, re

SP = os.path.dirname(os.path.abspath(__file__))
DB = os.path.expanduser('~/.local/share/chatdex/index.db')
SEP = '\x01'
N_GAP, N_CTRL, N_DOMAIN = 100, 30, 400
PER_SESSION, CHUNK = 7500, 750
random.seed(20260915)

gs = json.load(open(f'{SP}/gapset.json'))
gap = [c for c in gs['cases'] if c['kind'] == 'gap'][:N_GAP]
ctrl = [c for c in gs['cases'] if c['kind'] == 'control'][:N_CTRL]
targets = {c['sid'] for c in gap} | {c['sid'] for c in ctrl}
fillers = [s for s in gs['domain'] if s not in targets]
random.shuffle(fillers)
domain = sorted(targets | set(fillers[:max(0, N_DOMAIN - len(targets))]))
print(f"查询 {len(gap)} 鸿沟 + {len(ctrl)} 对照；检索域 {len(domain)} 个会话")

con = sqlite3.connect(f'file:{DB}?mode=ro', uri=True)
qmarks = ",".join("?" * len(domain))
raw = {}
for sid, body in con.execute(
        f"SELECT session_id, body FROM blocks WHERE session_id IN ({qmarks}) "
        f"AND kind IN ('user','assistant','reasoning') ORDER BY session_id, seq", domain):
    raw.setdefault(sid, []).append(body.replace(SEP, ''))

docs = {}
for sid, parts in raw.items():
    full = "\n".join(parts)
    if len(full) <= PER_SESSION:
        docs[sid] = full
        continue
    n = PER_SESSION // CHUNK                      # 取 n 个片段，起点在全文均匀分布
    step = (len(full) - CHUNK) / max(1, n - 1)
    docs[sid] = "\n".join(full[int(i * step):int(i * step) + CHUNK] for i in range(n))

kept = [s for s in domain if docs.get(s)]
tot = sum(len(v) for v in docs.values())
print(f"文档 {len(kept)} 份，合计 {tot/1e6:.2f} M 字符 → 预计嵌入 {tot/800/60:.0f} 分钟")
miss = [c['sid'] for c in gap + ctrl if not docs.get(c['sid'])]
print(f"目标会话无正文的：{len(miss)}")

json.dump(dict(gap=gap, ctrl=ctrl, domain=kept, docs={str(k): v for k, v in docs.items()}),
          open(f'{SP}/corpus.json', 'w'), ensure_ascii=False)
print("→ corpus.json")
