import json,os,glob,random,sqlite3,re,time
random.seed(7)
D='<scratchpad>'
files=glob.glob(os.path.expanduser('~/.claude/projects/*/*.jsonl'))
random.shuffle(files); sample=files[:60]
CAP=4096
CJK=re.compile(r'[㐀-䶿一-鿿豈-﫿぀-ヿ가-힯]')
def norm(s):
    return CJK.sub(lambda m:' '+m.group(0)+' ', s)
docs=[]
for f in sample:
    try:
        for line in open(f,encoding='utf-8',errors='replace'):
            try: d=json.loads(line)
            except: continue
            m=d.get('message') or {}
            c=m.get('content')
            if isinstance(c,str): docs.append(c[:CAP])
            elif isinstance(c,list):
                for b in c:
                    if not isinstance(b,dict): continue
                    t=b.get('type')
                    if t=='text': docs.append((b.get('text') or '')[:CAP])
                    elif t=='tool_use': docs.append(json.dumps(b.get('input') or {},ensure_ascii=False)[:CAP])
                    elif t=='tool_result':
                        cc=b.get('content')
                        s=cc if isinstance(cc,str) else json.dumps(cc,ensure_ascii=False)
                        docs.append(s[:CAP])
    except Exception: pass
raw=sum(len(x) for x in docs)
print("docs",len(docs),"text MB",round(raw/1e6,1),flush=True)

def build(name, ddl, xform):
    p=os.path.join(D,name+'.db')
    if os.path.exists(p): os.remove(p)
    con=sqlite3.connect(p); con.execute("PRAGMA journal_mode=OFF")
    con.execute(ddl)
    t=time.time()
    con.executemany("INSERT INTO t(body) VALUES (?)", ((xform(x),) for x in docs))
    con.commit()
    dt=time.time()-t
    sz=os.path.getsize(p)
    print(f"{name:26s} size={sz/1e6:8.1f}MB  x{sz/raw:.2f} of text  build={dt:.0f}s",flush=True)
    return con
try:
    c1=build('trigram',"CREATE VIRTUAL TABLE t USING fts5(body, tokenize='trigram')", lambda x:x)
    for q in ['"指纹浏览器"','"camoufox"','"rsync"']:
        t=time.time(); n=c1.execute("SELECT count(*) FROM t WHERE t MATCH ?",(q,)).fetchone()[0]
        print("  trigram",q,n,f"{(time.time()-t)*1000:.0f}ms",flush=True)
except Exception as e: print("trigram failed",e,flush=True)
c2=build('uni61cjk',"CREATE VIRTUAL TABLE t USING fts5(body, tokenize=\"unicode61 remove_diacritics 2\")", norm)
for q in ['"指 纹 浏 览 器"','"camoufox"','"rsync"']:
    t=time.time(); n=c2.execute("SELECT count(*) FROM t WHERE t MATCH ?",(q,)).fetchone()[0]
    print("  uni61cjk",q,n,f"{(time.time()-t)*1000:.0f}ms",flush=True)
