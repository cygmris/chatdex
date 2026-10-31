#!/usr/bin/env python3
"""构建「词汇鸿沟」测试集。

地面真值的来源：chatdex 的 LLM 摘要是对会话的**转述**，所以
「出现在摘要里、但正文里一个字都没有」的词，**按构造**就是词汇鸿沟
——而正确答案（会话 id）是已知的。

三条防自欺的约束：
1. 检索域 = 有摘要的那批会话，两个引擎完全相同（否则 hit@k 不可比）。
2. **被检索的文档是正文，不是摘要**。把摘要也灌进去，查询就是自己找自己。
3. 通用词用**文档频率**滤掉（设计/分析/实现/确认…），它们不构成鸿沟。
"""
import sqlite3, os, re, json, collections, random
import jieba, jieba.posseg as pseg

DB = os.path.expanduser('~/.local/share/chatdex/index.db')
OUT = os.path.dirname(os.path.abspath(__file__))
SEP = '\x01'
random.seed(20260915)

con = sqlite3.connect(f'file:{DB}?mode=ro', uri=True)
con.text_factory = str

sess = con.execute("""
    SELECT id, source, alive, COALESCE(title,''), summary
      FROM sessions WHERE summary IS NOT NULL AND TRIM(summary) <> ''
""").fetchall()
print(f"有摘要的会话 {len(sess)}（检索域）")

# ---- 正文：一次性拉出来，按 session 聚合 ----
bodies = collections.defaultdict(list)
tot_chars = 0
for sid, body in con.execute("""
    SELECT b.session_id, b.body FROM blocks b JOIN sessions s ON s.id=b.session_id
     WHERE s.summary IS NOT NULL AND TRIM(s.summary) <> ''
       AND b.kind IN ('user','assistant','reasoning')"""):
    t = body.replace(SEP, '')
    bodies[sid].append(t)
    tot_chars += len(t)
bodies = {k: "\n".join(v) for k, v in bodies.items()}
print(f"正文总字符 {tot_chars/1e6:.1f} M，覆盖 {len(bodies)} 个会话")

CJK = re.compile(r'[一-鿿]{2,}|[A-Za-z][A-Za-z0-9_.\-]{2,}')
STOP = set('的了是在和与及或对把被从到为以而其这那有不也都就还很再又更最不过因为所以但是如果可以需要进行通过使用以及问题方案实现设计分析确认完成修复添加删除修改调整优化支持功能代码文件用户我们他们内容情况方式方法过程结果说明记录发现处理相关部分主要一个一些这个那个当前目前现在已经可能应该必须建议尝试检查验证测试运行执行返回获取生成创建更新版本系统服务接口数据信息配置参数状态默认'.strip())

# 只留实词：名词/专名/动名词/外文词。
# 不加这条，鸿沟词会**按构造**偏向转述用的形容词与泛动词（通用/静态/兼容），
# 而对照词偏向从正文抄来的标识符 —— 两组可区分度不对等，比的就不是检索能力了。
KEEP_POS = {'n','nr','ns','nt','nz','ng','vn','eng','j','l'}

def terms(text):
    out = []
    for m in CJK.finditer(text):
        s = m.group(0)
        if re.match(r'^[A-Za-z]', s):
            if len(s) >= 3: out.append(s)
            continue
        for w, flag in pseg.cut(s):
            if len(w) >= 2 and w not in STOP and flag in KEEP_POS:
                out.append(w)
    return out

# ---- 文档频率：用全部摘要算，滤掉「哪篇摘要都有」的通用词 ----
df = collections.Counter()
summ_terms = {}
for sid, src, alive, title, summ in sess:
    ts = set(terms(summ))
    summ_terms[sid] = ts
    df.update(ts)
N = len(sess)
DF_CAP = 0.02 * N          # 出现在 >2% 摘要里的词判为通用词
DF_MIN = 3                 # 只出现 1~2 次的多半是分词碎片，不是真词
print(f"摘要词表 {len(df)}，DF 上限 {DF_CAP:.0f} 篇（{N} 篇摘要）")

gap_cases, ctrl_cases = [], []
for sid, src, alive, title, summ in sess:
    body = bodies.get(sid, '')
    if len(body) < 2000:            # 太短的会话正文不足以构成检索目标
        continue
    gaps  = [t for t in summ_terms[sid] if DF_MIN <= df[t] <= DF_CAP and t not in body]
    insid = [t for t in summ_terms[sid] if DF_MIN <= df[t] <= DF_CAP and t in body]
    # 查询取 2~3 个词，按稀有度排（df 小的更有判别力）
    gaps.sort(key=lambda t: df[t]); insid.sort(key=lambda t: df[t])
    if len(gaps) >= 3:
        gap_cases.append(dict(sid=sid, source=src, alive=alive, title=title,
                              q=" ".join(gaps[:3]), kind="gap"))
    if len(insid) >= 3:
        ctrl_cases.append(dict(sid=sid, source=src, alive=alive, title=title,
                               q=" ".join(insid[:3]), kind="control"))

random.shuffle(gap_cases); random.shuffle(ctrl_cases)
gap_cases = gap_cases[:200]
ctrl_cases = ctrl_cases[:60]
print(f"\n鸿沟查询 {len(gap_cases)}  阳性对照 {len(ctrl_cases)}")

json.dump(dict(domain=[s[0] for s in sess], cases=gap_cases + ctrl_cases),
          open(os.path.join(OUT, 'gapset.json'), 'w'), ensure_ascii=False, indent=1)
print("→ gapset.json")
print("\n鸿沟样例：")
for c in gap_cases[:8]:
    print(f"  [{c['sid']:>5}] {c['q']}   ← {c['title'][:28]}")
print("\n对照样例：")
for c in ctrl_cases[:4]:
    print(f"  [{c['sid']:>5}] {c['q']}")
