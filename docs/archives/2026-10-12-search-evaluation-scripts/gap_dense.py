#!/usr/bin/env python3
"""稠密臂：bge-m3 向量检索，**只看正文**（绝不嵌摘要——那会让查询找到自己）。

会话得分取其块的**最大**余弦，与 chatdex 的 MIN(bm25)（取最佳块）同构。
"""
import json, os, urllib.request, numpy as np, sys

SP = os.path.dirname(os.path.abspath(__file__))
c = json.load(open(f'{SP}/corpus.json'))
meta = json.load(open(f'{SP}/emb_meta.json'))
V = np.load(f'{SP}/emb.npy')
sids = np.array(meta['sids'][:len(V)])
V = V / np.linalg.norm(V, axis=1, keepdims=True)
print(f"向量 {V.shape}，覆盖 {len(set(sids.tolist()))} 个会话", file=sys.stderr)

def emb(batch):
    r = urllib.request.Request("http://127.0.0.1:11434/v1/embeddings",
        data=json.dumps({"model": "bge-m3", "input": batch}).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer local"})
    return [x['embedding'] for x in json.loads(urllib.request.urlopen(r, timeout=600).read())['data']]

cases = c['gap'] + c['ctrl']
qs = [x['q'] for x in cases]
Q = []
for i in range(0, len(qs), 16):
    Q.extend(emb(qs[i:i+16]))
    print(f"  查询 {min(i+16,len(qs))}/{len(qs)}", file=sys.stderr, flush=True)
Q = np.asarray(Q, dtype=np.float32)
Q = Q / np.linalg.norm(Q, axis=1, keepdims=True)

uniq = np.array(sorted(set(sids.tolist())))
out = {}
for i, case in enumerate(cases):
    sim = V @ Q[i]
    best = {}
    for s, v in zip(sids.tolist(), sim.tolist()):
        if v > best.get(s, -2): best[s] = v          # 会话得分 = 最佳块
    order = sorted(best, key=lambda s: -best[s])
    out[case['q']] = dict(sid=case['sid'], kind=case['kind'],
                          rank=(order.index(case['sid'])+1) if case['sid'] in order else 0,
                          n=len(order))
json.dump(out, open(f'{SP}/res_dense.json','w'), ensure_ascii=False, indent=1)
print("→ res_dense.json")
