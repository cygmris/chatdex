#!/usr/bin/env python3
"""稀疏侧：在同一份采样文档上复刻 chatdex 的 FTS5 检索。

逐条对齐 chatdex 的真实实现（不是"差不多"）：
  · 索引形态 = NormalizeIndex：相邻 token 字符中只要有一个是 CJK 就插分隔符
  · 查询形态 = BuildMatch：每个空格分隔的词各成一个**短语**，词间 AND
  · 打分     = bm25(blocks_fts) 默认权重，会话得分取其块的 **MIN**（engine.go:230）
  · 分块粒度与稠密侧完全相同（750 字符），否则 bm25 的文档长度归一化就不可比
复刻的忠实度不靠"看起来一样"，而是拿同一批查询跑线上 chatdex 做交叉校验（见 gap_eval.py）。
"""
import sqlite3, json, os, unicodedata, sys

SP = os.path.dirname(os.path.abspath(__file__))
CHUNK = 750

CJK = [(0x3040,0x30ff),(0x3400,0x4dbf),(0x4e00,0x9fff),
       (0xac00,0xd7af),(0xf900,0xfaff),(0x20000,0x2ffff)]

def is_cjk(ch):
    o = ord(ch)
    return any(lo <= o <= hi for lo, hi in CJK)

def is_token(ch):
    return ch.isalpha() or ch.isdigit()

def split(s, sep):
    out, prev, first = [], '', True
    for ch in s:
        if ch == '\x01':
            continue
        if not first and is_token(prev) and is_token(ch) and (is_cjk(prev) or is_cjk(ch)):
            out.append(sep)
        out.append(ch)
        prev, first = ch, False
    return "".join(out)

normalize_index = lambda s: split(s, '\x01')
normalize_query = lambda s: split(s, ' ')

def build_match(text):
    ps = []
    for term in text.split():
        n = normalize_query(term).strip()
        if n:
            ps.append('"' + n.replace('"', '""') + '"')
    return " ".join(ps)

c = json.load(open(f'{SP}/corpus.json'))
db = sqlite3.connect(':memory:')
db.execute("CREATE VIRTUAL TABLE fts USING fts5(body)")
db.execute("CREATE TABLE map(rowid_ INTEGER PRIMARY KEY, sid INTEGER)")
INCLUDE_SUMMARY = "--with-summary" in sys.argv
rid = 0
if INCLUDE_SUMMARY:
    # chatdex 真实索引里 summary 是一种 block kind（库里 3386 个）。
    # 单独成一臂：它是 chatdex 已有的"转述层"，与正文检索能力是两回事，混在一起就说不清。
    for sid, summ in c.get('summaries', {}).items():
        if summ.strip():
            rid += 1
            db.execute("INSERT INTO fts(rowid, body) VALUES (?,?)", (rid, normalize_index(summ)))
            db.execute("INSERT INTO map VALUES (?,?)", (rid, int(sid)))
for sid, doc in c['docs'].items():
    for i in range(0, len(doc), CHUNK):
        piece = doc[i:i+CHUNK].strip()
        if len(piece) < 50:
            continue
        rid += 1
        db.execute("INSERT INTO fts(rowid, body) VALUES (?,?)", (rid, normalize_index(piece)))
        db.execute("INSERT INTO map VALUES (?,?)", (rid, int(sid)))
print(f"索引 {rid} 块 / {len(c['docs'])} 会话", file=sys.stderr)

def rank(q):
    m = build_match(q)
    # AS MATERIALIZED 不可省：否则优化器把 CTE 展平回外层，
    # bm25() 就落在"非直接查询 FTS 表"的语境里而报错（engine.go:27 同款）。
    rows = db.execute("""
        WITH f AS MATERIALIZED (
          SELECT rowid AS bid, bm25(fts) AS score FROM fts WHERE fts MATCH ?)
        SELECT m.sid, MIN(f.score) s FROM f JOIN map m ON m.rowid_ = f.bid
        GROUP BY m.sid ORDER BY s ASC""", (m,)).fetchall()
    return [r[0] for r in rows]

out = {}
for case in c['gap'] + c['ctrl']:
    r = rank(case['q'])
    out[case['q']] = dict(sid=case['sid'], kind=case['kind'],
                          rank=(r.index(case['sid']) + 1) if case['sid'] in r else 0,
                          n=len(r))
json.dump(out, open(f'{SP}/res_sparse_summ.json' if INCLUDE_SUMMARY else f'{SP}/res_sparse.json','w'), ensure_ascii=False, indent=1)
print("→ 写出")
