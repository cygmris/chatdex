#!/usr/bin/env python3
"""用本地 Ollama 的 bge-m3 嵌入 corpus.json 里的文档块。断点续跑。

走 /v1/embeddings（OpenAI 兼容路径）而不是 /api/embed：那正是 agentmemory
的 openai 嵌入 provider 会走的那条路（OPENAI_EMBEDDING_BASE_URL 指过来），
所以这条链路本身也顺带验了。
"""
import json, os, sys, time, urllib.request
import numpy as np

SP = os.path.dirname(os.path.abspath(__file__))
CHUNK = 750
OUT_V, OUT_M = f'{SP}/emb.npy', f'{SP}/emb_meta.json'

c = json.load(open(f'{SP}/corpus.json'))
chunks = []
for sid, doc in c['docs'].items():
    for i in range(0, len(doc), CHUNK):
        piece = doc[i:i + CHUNK].strip()
        if len(piece) >= 50:
            chunks.append((int(sid), piece))
print(f"共 {len(chunks)} 块", flush=True)

done = 0
vecs = []
if os.path.exists(OUT_M):
    done = json.load(open(OUT_M))['done']
    vecs = list(np.load(OUT_V))
    print(f"续跑，已有 {done}", flush=True)


def emb(batch):
    r = urllib.request.Request("http://127.0.0.1:11434/v1/embeddings",
        data=json.dumps({"model": "bge-m3", "input": batch}).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer local"})
    return [x['embedding'] for x in json.loads(urllib.request.urlopen(r, timeout=600).read())['data']]


t0 = time.time()
B = 16
while done < len(chunks):
    batch = [t for _, t in chunks[done:done + B]]
    for attempt in range(3):
        try:
            vecs.extend(emb(batch)); break
        except Exception as e:
            print(f"  重试 {attempt+1}: {e}", flush=True); time.sleep(5)
    else:
        sys.exit("嵌入连续失败三次，停")
    done += len(batch)
    a = np.asarray(vecs, dtype=np.float32)
    np.save(OUT_V, a)
    json.dump({'done': done, 'sids': [s for s, _ in chunks[:done]]}, open(OUT_M, 'w'))
    el = time.time() - t0
    print(f"  {done}/{len(chunks)}  {el:.0f}s  剩约 {(len(chunks)-done)*el/max(1,done-0)/60:.0f} 分", flush=True)
print("完成", flush=True)
