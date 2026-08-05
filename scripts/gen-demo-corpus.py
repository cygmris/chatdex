#!/usr/bin/env python3
"""为 README 截图生成一份完全虚构的会话语料。

README 里的每一张截图都用这份数据，**没有任何真实会话内容**。
生成器放进仓库，是为了让这句话可以被验证。

用法：
    python3 scripts/gen-demo-corpus.py <目标 HOME>
    HOME=<目标 HOME> chatdex index
    HOME=<目标 HOME> chatdex serve      # 端口在该 HOME 的 config.json 里配

设计要点（都是为了「看起来像真的干活留下的」）：

- **对话是写死的剧本，不是随机拼接。** 上一版把同两条命令原样重复 1–8 次、
  回复只有两句循环用，于是摘要里出现「配置请求限流，讨论使用滑动窗口代替
  令牌桶」这种同一句话出现三次的结果。现在每个主题是一段完整对话，
  每一轮有自己的话、自己的命令、自己的输出。
- **同一件事跨天有多集。** 真实语料里「周一发现问题、周三修、下周复查」
  很常见；标题不同、内容连续，这是真实感的主要来源。
- **助手回复里有 Markdown**：小标题、列表、行内代码、围栏代码块、表格、
  mermaid —— 渲染与高亮是这个项目的卖点，语料里得真的有东西可渲染。
- **工具输出是真实形态**：编译错误、测试先红后绿、`git log`、SQL 结果、
  `kubectl` 表格、火焰图统计。读文件类命令的输出就是源码（那正是
  「输出也上高亮」要展示的）。
"""
import json
import os
import random
import shutil
import sys
from datetime import datetime, timedelta

random.seed(20260805)

if len(sys.argv) < 2:
    sys.exit(__doc__)
HOME = os.path.abspath(sys.argv[1])

# 这个脚本会**删掉**目标目录再重建。参数名叫 HOME，手滑传 ~ 就没了，
# 所以只认两种目标：不存在的目录，或上一次由本脚本自己建的目录
# （靠这个标记文件认）。不用 ignore_errors——它会把「删不掉」也一起吞了。
MARKER = ".chatdex-demo-corpus"

if os.path.exists(HOME):
    if not os.path.isfile(os.path.join(HOME, MARKER)):
        sys.exit(
            f"拒绝写入 {HOME}\n"
            f"它已存在，且不是本脚本建的（没有 {MARKER} 标记文件）。\n"
            f"换一个不存在的目录，或先自行确认后删掉它。"
        )
    shutil.rmtree(HOME)
os.makedirs(HOME)
open(os.path.join(HOME, MARKER), "w").write(
    "本目录由 scripts/gen-demo-corpus.py 生成，重跑该脚本会整个删掉重建。\n")

PROJECTS = {
    "api-gateway": "/home/dev/work/api-gateway",
    "etl-pipeline": "/home/dev/work/etl-pipeline",
    "search-service": "/home/dev/work/search-service",
    "web-console": "/home/dev/work/web-console",
    "dotfiles": "/home/dev/dotfiles",
}

# 起始时间用 UTC 01:00：文件里写的是 UTC（真实 Claude 会话就是这样），
# 而截图机器在 UTC+7，01:00–11:00 UTC 正好显示成 08:00–18:00 的工作时间。
# 原先用 09:00，一半会话在界面上显示成凌晨两三点，一眼就假。
BASE = datetime(2026, 5, 4, 1, 0, 0)


def ts(d):
    return d.strftime("%Y-%m-%dT%H:%M:%S.000Z")


def turn(text, tool=None, cmd=None, out=None, name=None):
    """一轮：助手说一段话，可能顺手调一个工具。"""
    return {"text": text, "tool": tool, "cmd": cmd, "out": out, "name": name}


# ══════════════════════════════════════════════════════════════════
# 剧本。每条 = (项目, 标题, [轮次...])，可选 custom_title。
# ══════════════════════════════════════════════════════════════════

TOPICS = []


def topic(project, title, turns, custom_title=None, day=None, hour=None):
    TOPICS.append({
        "project": project, "title": title, "turns": turns,
        "custom_title": custom_title, "day": day, "hour": hour,
    })


# ── 限流：三集连续剧（发现 → 实现 → 上线复查）─────────────────────

topic("api-gateway", "网关被刷了，凌晨那波请求把 Postgres 连接池打满了", [
    turn("先看看那段时间的访问日志，确认是不是同一批来源。",
         "Bash", "awk '$4 ~ /03:0[0-9]/ {print $1}' /var/log/nginx/access.log | sort | uniq -c | sort -rn | head",
         "  84213 203.0.113.47\n  81904 203.0.113.51\n   9122 198.51.100.8\n    412 192.0.2.19\n    288 198.51.100.203"),
    turn("两个 IP 打了 16 万请求，占了 98%。看下打的是哪个接口。",
         "Bash", "awk '$1 ~ /203.0.113/ {print $7}' /var/log/nginx/access.log | sort | uniq -c | sort -rn | head -5",
         "  92004 /api/v1/search\n  74113 /api/v1/search\n     12 /healthz"),
    turn("全打在 `/api/v1/search` 上，而那个接口每次都要查库。确认下连接池当时的状态。",
         "Bash", "psql -h db -c \"select state, count(*) from pg_stat_activity group by state\"",
         "     state      | count \n----------------+-------\n active         |    97\n idle           |     3\n idle in transaction |  0\n(3 rows)"),
    turn("""97/100 全是 active，池子确实见底了。

**结论**：不是慢查询，是**请求量本身**没有闸门。三条路：

| 方案 | 挡在哪 | 代价 |
|---|---|---|
| nginx `limit_req` | 最外层 | 粒度只到 IP，业务维度分不开 |
| 网关中间件 | 鉴权之后 | 要自己实现，但能按 token / 路由分 |
| 数据库连接池排队 | 最内层 | 治标，请求还是会堆在网关 |

先在网关做，nginx 那层留给纯粹的 DDoS。"""),
])

topic("api-gateway", "给 api-gateway 加限流中间件，先按 IP 维度", [
    turn("先看现在的中间件链路怎么串的，限流要插在合适的位置。",
         "Bash", "rg -n 'r\\.Use' internal/server/router.go",
         "40:\tr.Use(middleware.RequestID)\n41:\tr.Use(middleware.RealIP)\n42:\tr.Use(middleware.Logger)\n43:\tr.Use(middleware.Recoverer)\n44:\tr.Use(auth.Middleware(cfg.JWT))"),
    turn("""限流要放在**鉴权之后**——匿名请求先被 401 挡掉，不该占限流器的桶。

算法选令牌桶而不是滑动窗口：滑动窗口要为每个 key 存时间戳队列，
IP 一多内存就吃紧；令牌桶每个 key 只要两个 `float64`。"""),
    turn("核心就这么点，`Allow` 每次按流逝的时间补桶：",
         "Write", "internal/ratelimit/bucket.go",
         "package ratelimit\n\nimport (\n\t\"sync\"\n\t\"time\"\n)\n\n// Bucket 是一个令牌桶。零值不可用，要走 NewBucket。\ntype Bucket struct {\n\tmu     sync.Mutex\n\ttokens float64\n\trate   float64 // 每秒补充的令牌数\n\tcap    float64 // 桶容量，决定能攒多大的突发\n\tlast   time.Time\n}\n\nfunc (b *Bucket) Allow(now time.Time) bool {\n\tb.mu.Lock()\n\tdefer b.mu.Unlock()\n\t// 按流逝时间补令牌，上限是桶容量\n\tb.tokens = min(b.cap, b.tokens+now.Sub(b.last).Seconds()*b.rate)\n\tb.last = now\n\tif b.tokens < 1 {\n\t\treturn false\n\t}\n\tb.tokens--\n\treturn true\n}"),
    turn("配置这样读进来：\n\n```yaml\nratelimit:\n  enabled: true\n  by: ip        # ip | token | route\n  rate: 20      # 每秒补充的令牌数\n  burst: 40     # 桶容量，允许多大的突发\n  exempt:       # 豁免的路由前缀\n    - /healthz\n    - /livez\n    - /metrics\n```",
         "Write", "config/ratelimit.yaml",
         "written 9 lines"),
    turn("跑一遍单测。",
         "Bash", "go test ./internal/ratelimit -v",
         "=== RUN   TestBucket_Allow\n--- PASS: TestBucket_Allow (0.00s)\n=== RUN   TestBucket_Refill\n    bucket_test.go:41: 补桶后应有 3 个令牌，实得 2.9999999\n--- FAIL: TestBucket_Refill (0.01s)\nFAIL\nFAIL\tapi-gateway/internal/ratelimit\t0.014s"),
    turn("浮点误差。断言改成比较到 0.01 就行，不必为这个引入定点数。",
         "Bash", "go test ./internal/ratelimit -count=1",
         "ok  \tapi-gateway/internal/ratelimit\t0.012s"),
    turn("""整条链路是这样，限流挡在鉴权之后、业务之前：

```mermaid
graph LR
  C[客户端] --> N[nginx]
  N --> A[鉴权]
  A -->|通过| R{限流}
  A -->|401| C
  R -->|放行| H[业务处理]
  R -->|429| C
  H --> D[(Postgres)]
```

429 直接在网关返回，不进业务，也就不会占用连接池。"""),
])

topic("api-gateway", "限流上线一周了，回头看下有没有误伤正常用户", [
    turn("先统计这一周 429 的分布。",
         "Bash", "jq -r 'select(.status==429) | .client_ip' /var/log/gateway/*.jsonl | sort | uniq -c | sort -rn | head",
         "  41882 203.0.113.47\n  38104 203.0.113.51\n     93 198.51.100.8\n      7 192.0.2.140\n      2 192.0.2.31"),
    turn("""99.8% 集中在那两个 IP，剩下的零星几个看下是谁。

`198.51.100.8` 有 93 次——不像刷的，像是某个客户端在轮询。"""),
    turn("查一下它打的什么。",
         "Bash", "jq -r 'select(.client_ip==\"198.51.100.8\") | \"\\(.path) \\(.user_agent)\"' /var/log/gateway/*.jsonl | sort | uniq -c",
         "     93 /api/v1/search  monitoring-probe/2.1"),
    turn("""是监控探针，被限流挡了 93 次。这是**误伤**——探针本来就该高频。

修法不是调大 rate（那等于对所有人放宽），是把它按 token 豁免。
`exempt` 现在只支持路由前缀，得加一条按 token 的：

```yaml
ratelimit:
  exempt_tokens:
    - svc-monitoring   # 监控探针，按需高频
```"""),
    turn("改完再跑一遍验证。",
         "Bash", "go test ./internal/ratelimit -run TestExempt -count=1 -v",
         "=== RUN   TestExemptByRoute\n--- PASS: TestExemptByRoute (0.00s)\n=== RUN   TestExemptByToken\n--- PASS: TestExemptByToken (0.00s)\nPASS\nok  \tapi-gateway/internal/ratelimit\t0.011s"),
], custom_title="限流上线复查")


# ── search-service：中文检索 ────────────────────────────────────────

topic("search-service", "中文检索一个字都搜不到，FTS5 好像把整句当成一个 token 了", [
    turn("先确认现象。",
         "Bash", "sqlite3 idx.db \"select count(*) from docs_fts where docs_fts match '限流'\"",
         "0"),
    turn("换成整句试试，看是不是分词的问题。",
         "Bash", "sqlite3 idx.db \"select count(*) from docs_fts where docs_fts match '配置请求限流'\"",
         "3"),
    turn("""确认了：整句能匹配、单词不行。`unicode61` 不认识中文的词边界，
所以「配置请求限流」被当成**一个** token。

两条路：

1. **trigram 分词器** —— SQLite 3.34+ 自带，改一行建表语句就行，
   但索引会膨胀到 4 倍多。
2. **入库前在字之间插分隔符** —— 把「限流」写成 `限 流`，
   查询同样处理，索引只涨 2 倍左右。

选 2。分隔符要挑一个正文里不可能出现的字符。"""),
    turn("用 U+0001，它是控制字符，正文里不会有。",
         "Write", "internal/search/normalize.go",
         "package search\n\nimport \"strings\"\n\n// SEP 是插在 CJK 字之间的分隔符。\n// 用 U+0001 而不是空格：空格在正文里到处都是，会把「北 京」这种\n// 本来就带空格的写法和我们插的分不开。\nconst SEP = '\\u0001'\n\nfunc NormalizeIndex(s string) string {\n\tvar b strings.Builder\n\tprev := false\n\tfor _, r := range s {\n\t\tcur := isCJK(r)\n\t\tif cur && prev {\n\t\t\tb.WriteRune(SEP)\n\t\t}\n\t\tb.WriteRune(r)\n\t\tprev = cur\n\t}\n\treturn b.String()\n}"),
    turn("重建索引再试。",
         "Bash", "sqlite3 idx.db < schema.sql && ./search-service reindex --quiet",
         "reindexed 3176 docs in 4.1s"),
    turn("跑一遍。",
         "Bash", "sqlite3 idx.db \"select count(*) from docs_fts where docs_fts match '限 流'\"",
         "41"),
    turn("41 条。索引从 12 MB 涨到 25 MB，2.1 倍，比 trigram 那条路省很多。"),
])

topic("search-service", "检索延迟 p99 到 900ms 了，看下慢在哪", [
    turn("先跑个 profile。",
         "Bash", "go test ./internal/search -bench BenchmarkQuery -cpuprofile cpu.out -benchtime 200x",
         "BenchmarkQuery-16    200    4821337 ns/op    1284 B/op    31 allocs/op\nPASS"),
    turn("看火焰图里最重的几个。",
         "Bash", "go tool pprof -top -nodecount=8 cpu.out",
         "Showing nodes accounting for 3.21s, 78.4% of 4.09s total\n      flat  flat%   sum%        cum   cum%\n     1.44s 35.2%  35.2%      1.44s 35.2%  search.highlightSnippet\n     0.61s 14.9%  50.1%      2.19s 53.5%  search.(*Engine).Query\n     0.42s 10.3%  60.4%      0.42s 10.3%  regexp.(*Regexp).FindAllIndex\n     0.31s  7.6%  68.0%      0.31s  7.6%  runtime.mallocgc\n     0.24s  5.9%  73.9%      0.24s  5.9%  strings.Builder.WriteString"),
    turn("""`highlightSnippet` 占了 35%，而它里面在**每次调用时编译正则**。

正则是固定的，编译一次就够——`regexp.MustCompile` 提到包级变量。"""),
    turn("看下现在这段。",
         "Bash", "sed -n '48,62p' internal/search/snippet.go",
         "func highlightSnippet(body, term string) string {\n\t// 每次调用都编译一遍——这就是热点\n\tre := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(term))\n\tvar out strings.Builder\n\tlast := 0\n\tfor _, m := range re.FindAllStringIndex(body, -1) {\n\t\tout.WriteString(body[last:m[0]])\n\t\tout.WriteString(hitOpen)\n\t\tout.WriteString(body[m[0]:m[1]])\n\t\tout.WriteString(hitClose)\n\t\tlast = m[1]\n\t}\n\tout.WriteString(body[last:])\n\treturn out.String()\n}"),
    turn("""term 是变的，所以不能简单提成包级变量——但可以缓存。
查询词的基数很小（同一个词会被反复搜），用一个带上限的 map 就行。"""),
    turn("改完重测。",
         "Bash", "go test ./internal/search -bench BenchmarkQuery -benchtime 200x",
         "BenchmarkQuery-16    200     412884 ns/op     288 B/op     6 allocs/op\nPASS"),
    turn("4.8ms → 0.41ms，**快了 11 倍**。线上 p99 观察一天再确认。"),
])

topic("search-service", "给检索加个「只看最近 7 天」的快捷过滤", [
    turn("现在的时间过滤是两个日期输入框，常用场景其实就那么几个。",
         "Read", "internal/search/filter.go",
         "type Filter struct {\n\tSince, Until int64  // unix 秒，0 表示不限\n\tProject      string\n\tKind         string\n}"),
    turn("加两个预设按钮就够了，不引入新的字段——预设只是把 Since 算好填进去。"),
    turn("", "Edit", "web/filters.js",
         "const PRESETS = {\n  today: () => startOfDay(new Date()),\n  week:  () => Date.now() / 1000 - 7 * 86400,\n};"),
    turn("跑一下前端测试。",
         "Bash", "npm test -- filters",
         "PASS  web/__tests__/filters.test.js\n  ✓ 今天：Since 落在当天 00:00 (3 ms)\n  ✓ 最近 7 天：Since = now - 7d (1 ms)\n  ✓ 预设与手填互斥 (2 ms)\n\nTests: 3 passed, 3 total"),
])


# ── etl-pipeline：数据管道 ─────────────────────────────────────────

topic("etl-pipeline", "ETL 偶发丢数据，怀疑是两个 worker 写同一个分区", [
    turn("先看日志里有没有痕迹。",
         "Bash", "grep -c 'lost update' /var/log/etl/worker-*.log",
         "/var/log/etl/worker-0.log:0\n/var/log/etl/worker-1.log:9\n/var/log/etl/worker-2.log:8\n/var/log/etl/worker-3.log:0"),
    turn("worker-1 和 worker-2 各 8、9 次。看下它们的 offset 有没有重叠。",
         "Bash", "jq -r 'select(.msg==\"claim\") | \"\\(.worker) \\(.partition) \\(.from)-\\(.to)\"' /var/log/etl/worker-{1,2}.log | sort | head -6",
         "worker-1 p3 184000-185000\nworker-1 p3 185000-186000\nworker-2 p3 184000-185000\nworker-2 p3 185000-186000\nworker-1 p7 210000-211000\nworker-2 p7 210000-211000"),
    turn("""重叠了——两个 worker 拿到**同一批** offset，各写各的，后写的覆盖前面。

根因在认领逻辑：`SELECT ... FOR UPDATE` 少了 `SKIP LOCKED`，
两个事务读到同一行都以为自己拿到了。"""),
    turn("看下现在的 SQL。",
         "Bash", "sed -n '30,44p' internal/etl/claim.go",
         "const claimSQL = `\nSELECT partition, from_offset, to_offset\nFROM etl_chunks\nWHERE status = 'pending'\nORDER BY created_at\nLIMIT $1\nFOR UPDATE`\n\nfunc (w *Worker) claim(ctx context.Context, n int) ([]Chunk, error) {\n\trows, err := w.tx.QueryContext(ctx, claimSQL, n)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tdefer rows.Close()"),
    turn("加上 `SKIP LOCKED`——拿不到锁的直接跳过下一批，而不是等在那里读同一行。",
         "Edit", "internal/etl/claim.go",
         "-FOR UPDATE`\n+FOR UPDATE SKIP LOCKED`"),
    turn("跑 200 轮并发压测复现看看。",
         "Bash", "go test ./internal/etl -run TestConcurrentClaim -count=200",
         "ok  \tetl-pipeline/internal/etl\t18.244s"),
    turn("200 轮零重叠。改之前同样的测试 12 轮就能复现一次。"),
])

topic("etl-pipeline", "Kafka 消费积压 18 万条，消费者一直在 rebalance", [
    turn("先看积压和组状态。",
         "Bash", "kafka-consumer-groups --bootstrap-server kafka:9092 --describe --group etl",
         "GROUP  TOPIC   PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG      CONSUMER-ID\netl    orders  0          1284203         1468406         184203   -\netl    orders  1          1290114         1301882         11768    consumer-3\netl    orders  2          1288907         1299441         10534    consumer-1"),
    turn("p0 的 consumer-id 是空的——没人在消费它。看下 rebalance 频率。",
         "Bash", "grep -c 'Rebalance' /var/log/etl/consumer.log",
         "441"),
    turn("""一天 441 次 rebalance，等于一直在换手。

典型原因是**单条处理时间超过 `max.poll.interval.ms`**：
消费者被判定为死了，踢出组，然后重新加入，如此循环。"""),
    turn("确认单条耗时。",
         "Bash", "jq -r 'select(.msg==\"handled\") | .ms' /var/log/etl/consumer.log | sort -n | tail -3",
         "298114\n301882\n304417"),
    turn("""单条最长 304 秒，而 `max.poll.interval.ms` 默认 300 秒——**刚好卡在边界上**。

两处都要改：

- `MaxPollInterval` 提到 10 分钟（给慢的那批留余量）
- 处理改成批量拉 + 异步提交，把单条耗时压下去

只改前者是治标——量再涨一点又会撞上。"""),
    turn("", "Edit", "internal/etl/consumer.go",
         "-\tcfg.Consumer.MaxPollInterval = 5 * time.Minute\n+\tcfg.Consumer.MaxPollInterval = 10 * time.Minute\n+\tcfg.Consumer.Fetch.Default = 4 << 20"),
    turn("重启后盯了 20 分钟。",
         "Bash", "kafka-consumer-groups --bootstrap-server kafka:9092 --describe --group etl | awk '{print $6}'",
         "LAG\n0\n0\n0"),
    turn("积压消完了，rebalance 也停了。"),
])

topic("etl-pipeline", "定时任务在夏令时切换那天跑了两次", [
    turn("先看那天的执行记录。",
         "Bash", "psql -c \"select run_at, status from etl_runs where run_at::date = '2026-03-29' order by run_at\"",
         "        run_at         | status \n-----------------------+--------\n 2026-03-29 01:30:00+01 | ok\n 2026-03-29 01:30:00+02 | ok\n(2 rows)"),
    turn("""同一个本地时间出现了两次——`01:30` 在夏令时切换那天存在两遍。

调度用的是**本地时间**，所以跑了两次。反过来在秋季切换那天，
`02:30` 那一刻根本不存在，任务会被跳过。

修法：调度一律用 UTC，只有展示给人看的时候才转成本地时区。"""),
    turn("", "Edit", "internal/etl/schedule.go",
         "-\tnext := cron.Next(time.Now())\n+\t// 调度用 UTC：本地时间在夏令时切换日会重复或缺失一小时\n+\tnext := cron.Next(time.Now().UTC())"),
    turn("补个测试把这天钉住。",
         "Bash", "go test ./internal/etl -run TestDST -v",
         "=== RUN   TestDST_SpringForward\n--- PASS: TestDST_SpringForward (0.00s)\n=== RUN   TestDST_FallBack\n--- PASS: TestDST_FallBack (0.00s)\nPASS\nok  \tetl-pipeline/internal/etl\t0.008s"),
])


# ── web-console：前端 ──────────────────────────────────────────────

topic("web-console", "首屏 4.2 秒，看下时间花在哪了", [
    turn("先跑一遍 lighthouse。",
         "Bash", "npx lighthouse http://localhost:3000 --only-categories=performance --quiet --chrome-flags='--headless'",
         "Performance: 34\n  First Contentful Paint    2.1 s\n  Largest Contentful Paint  4.2 s\n  Total Blocking Time       890 ms\n  Speed Index               3.8 s"),
    turn("看下 bundle 里什么最大。",
         "Bash", "npx vite-bundle-visualizer --json | jq -r '.[] | \"\\(.size/1024|floor)KB \\(.name)\"' | sort -rn | head -6",
         "1842KB node_modules/echarts\n 684KB node_modules/moment/locale\n 412KB node_modules/lodash\n 288KB src/pages\n  96KB node_modules/react-dom\n  41KB src/components"),
    turn("""三个都能砍：

| 依赖 | 现状 | 处理 |
|---|---|---|
| echarts | 1.8 MB，全量引入 | 只有两个页面用，改按需 import |
| moment/locale | 684 KB，打进了**全部**语言包 | 项目只用 zh-CN，用 `IgnorePlugin` 排掉其余 |
| lodash | 412 KB，全量 | 实际只用了 4 个函数，改成 `lodash-es` 具名导入 |"""),
    turn("先处理 moment 的语言包。",
         "Edit", "vite.config.ts",
         "+import { defineConfig } from 'vite'\n+\n+// moment 默认打进全部语言包（684 KB），只留 zh-CN\n+resolve: { alias: { 'moment/locale': 'moment/locale/zh-cn' } }"),
    turn("echarts 改成按需。",
         "Edit", "src/pages/Dashboard.tsx",
         "-import * as echarts from 'echarts'\n+import { init } from 'echarts/core'\n+import { LineChart, BarChart } from 'echarts/charts'\n+import { GridComponent, TooltipComponent } from 'echarts/components'"),
    turn("重新构建看看。",
         "Bash", "npm run build",
         "vite v5.4.2 building for production...\n✓ 312 modules transformed.\ndist/assets/index-8f2a1c3d.js   412.84 kB │ gzip: 128.11 kB\ndist/assets/vendor-1b9e4f77.js  284.19 kB │ gzip:  91.02 kB\n✓ built in 3.81s"),
    turn("再跑一次 lighthouse。",
         "Bash", "npx lighthouse http://localhost:3000 --only-categories=performance --quiet --chrome-flags='--headless'",
         "Performance: 91\n  First Contentful Paint    0.7 s\n  Largest Contentful Paint  1.3 s\n  Total Blocking Time       110 ms\n  Speed Index               1.1 s"),
    turn("34 → 91，LCP 从 4.2s 降到 1.3s。lodash 那条还没动，留着下次。"),
], custom_title="首屏优化")

topic("web-console", "表格滚动到第 3000 行就卡死了", [
    turn("看下现在怎么渲染的。",
         "Bash", "rg -n 'rows.map' src/components/DataTable.tsx",
         "84:      {rows.map((row) => (\n85:        <tr key={row.id}>"),
    turn("""全量渲染。3000 行 × 12 列 = 36000 个 DOM 节点，卡是必然的。

上虚拟滚动，只渲染视口内的那些。不引入新库——
`@tanstack/react-virtual` 已经在依赖里了（分页组件在用）。"""),
    turn("", "Edit", "src/components/DataTable.tsx",
         "+const virtualizer = useVirtualizer({\n+  count: rows.length,\n+  getScrollElement: () => parentRef.current,\n+  estimateSize: () => 36,\n+  overscan: 8,\n+})"),
    turn("测一下渲染耗时。",
         "Bash", "npm test -- DataTable --verbose",
         "PASS  src/components/__tests__/DataTable.test.tsx\n  ✓ 3000 行首屏只渲染视口内的行 (48 ms)\n  ✓ 滚动后回收视口外的行 (31 ms)\n  ✓ 行高不一致时仍能正确定位 (22 ms)\n\nTests: 3 passed, 3 total\nTime: 1.284 s"),
    turn("DOM 节点从 36000 降到约 280，滚动跟手了。"),
])

topic("web-console", "暗色模式下有几个地方对比度不够", [
    turn("跑一下对比度检查。",
         "Bash", "node scripts/check-contrast.mjs --theme dark",
         "✗ .muted        #6b7280 on #16181d   ratio 3.81  (需要 4.5)\n✗ .badge-warn   #a16207 on #16181d   ratio 3.12  (需要 4.5)\n✓ .fg           #e5e7eb on #16181d   ratio 12.44\n✓ .accent       #9db4ff on #16181d   ratio 8.02\n2 failed, 14 passed"),
    turn("""两处不达标。不是把颜色随便调亮——要在**同一个色相上**提明度，
不然暗色主题会变得花。

- `.muted` `#6b7280` → `#9ca3af`（同为 gray 系，提两档）
- `.badge-warn` `#a16207` → `#eab308`（同为 amber 系）"""),
    turn("", "Edit", "src/styles/theme.css",
         "-  --fg-dim: #6b7280;\n+  --fg-dim: #9ca3af;\n-  --warn: #a16207;\n+  --warn: #eab308;"),
    turn("再跑一遍。",
         "Bash", "node scripts/check-contrast.mjs --theme dark",
         "✓ .muted        #9ca3af on #16181d   ratio 6.94\n✓ .badge-warn   #eab308 on #16181d   ratio 8.71\n16 passed"),
])


# ── dotfiles：环境 ─────────────────────────────────────────────────

topic("dotfiles", "zsh 启动 1.6 秒，慢得受不了了", [
    turn("先量一下到底慢在哪。",
         "Bash", "zsh -i -c 'zmodload zsh/zprof; source ~/.zshrc; zprof' 2>/dev/null | head -8",
         "num  calls   time      self       name\n---------------------------------------------\n 1)    1    1214.32   1214.32   nvm_auto\n 2)    1     182.44    182.44   compinit\n 3)    1      94.11     94.11   _p9k_init\n 4)    2      31.08     15.54   __conda_setup\n 5)    1      12.44     12.44   _direnv_hook"),
    turn("""`nvm_auto` 一个人吃掉 1.2 秒，占了 76%。

nvm 的初始化脚本要遍历版本目录、读 `.nvmrc`，每次开终端都跑一遍——
而大多数终端我根本不用 node。改成**惰性加载**：第一次调用 `nvm`/`node`/`npm`
时才真正 source。"""),
    turn("", "Write", "~/.zsh/lazy-nvm.zsh",
         "# nvm 初始化要 1.2 秒，而多数终端根本用不到 node。\n# 第一次调用时才真正加载，之后把这层壳解掉。\nfor cmd in nvm node npm npx yarn pnpm; do\n  eval \"${cmd}() {\n    unset -f nvm node npm npx yarn pnpm 2>/dev/null\n    source \\\"\\$NVM_DIR/nvm.sh\\\"\n    ${cmd} \\\"\\$@\\\"\n  }\"\ndone"),
    turn("compinit 那 182ms 也能省——每天只重建一次缓存就够了。",
         "Edit", "~/.zshrc",
         "+# compinit 每次都校验补全文件（182ms），一天重建一次足够\n+autoload -Uz compinit\n+if [[ -n ~/.zcompdump(#qN.mh+24) ]]; then compinit; else compinit -C; fi"),
    turn("重开终端量一下。",
         "Bash", "for i in 1 2 3; do /usr/bin/time -f %e zsh -i -c exit; done",
         "0.19\n0.17\n0.18"),
    turn("1.6s → 0.18s。第一次敲 `node` 会多等 1.2 秒，之后就没了。"),
])

topic("dotfiles", "证书自动续期又挂了，这次是 DNS 校验超时", [
    turn("看下 certbot 的日志。",
         "Bash", "journalctl -u certbot-renew --since '2 days ago' --no-pager | tail -12",
         "certbot[8814]: Plugin legacy name certbot-dns-cloudflare:dns-cloudflare may be removed\ncertbot[8814]: Waiting 60 seconds for DNS changes to propagate\ncertbot[8814]: Challenge failed for domain console.example.com\ncertbot[8814]: dns-01 challenge for console.example.com\ncertbot[8814]: DNS problem: NXDOMAIN looking up TXT for _acme-challenge.console.example.com\nsystemd[1]: certbot-renew.service: Main process exited, code=exited, status=1"),
    turn("""等 60 秒不够——权威 DNS 到公共解析器的传播比这慢。

手动验一下现在传播到了没。"""),
    turn("", "Bash", "dig +short TXT _acme-challenge.console.example.com @1.1.1.1",
         "\"kR3nX8vQ2mB7pL1wY4tZ9cF6dA0sG5hJ\""),
    turn("现在有了，说明只是慢。等待时间提到 180 秒。",
         "Edit", "/etc/letsencrypt/cli.ini",
         "-dns-cloudflare-propagation-seconds = 60\n+dns-cloudflare-propagation-seconds = 180"),
    turn("干跑一次确认。",
         "Bash", "certbot renew --dry-run",
         "Simulating renewal of an existing certificate for console.example.com\nCongratulations, all simulated renewals succeeded:\n  /etc/letsencrypt/live/console.example.com/fullchain.pem (success)"),
    turn("另外加个到期告警——续期失败时至少要有人知道，而不是等浏览器报错。",
         "Write", "/etc/systemd/system/cert-expiry-check.service",
         "[Unit]\nDescription=证书到期检查（续期失败时的兜底告警）\n\n[Service]\nType=oneshot\nExecStart=/usr/local/bin/cert-expiry-check.sh --warn-days 14"),
])

topic("dotfiles", "把 nvim 的插件从 packer 换成 lazy.nvim", [
    turn("先看现在装了多少插件。",
         "Bash", "ls ~/.local/share/nvim/site/pack/packer/start/ | wc -l",
         "34"),
    turn("量一下启动耗时。",
         "Bash", "nvim --startuptime /tmp/st.log +q && tail -3 /tmp/st.log",
         "312.441  000.082  000.082: sourcing ~/.config/nvim/after/plugin/lsp.lua\n318.204  005.763: sourcing ~/.config/nvim/init.lua\n318.229  000.025: VimEnter autocommands"),
    turn("""318ms，34 个插件全部在启动时加载。

lazy.nvim 的好处正是这个：按事件/文件类型/命令延迟加载。
真正启动就需要的其实只有配色和状态栏。"""),
    turn("", "Write", "~/.config/nvim/lua/plugins/init.lua",
         "return {\n  -- 启动就要：配色 + 状态栏\n  { 'folke/tokyonight.nvim', lazy = false, priority = 1000 },\n  { 'nvim-lualine/lualine.nvim', event = 'VeryLazy' },\n\n  -- 打开文件才要\n  { 'nvim-treesitter/nvim-treesitter', event = { 'BufReadPost', 'BufNewFile' } },\n\n  -- 敲命令才要\n  { 'nvim-telescope/telescope.nvim', cmd = 'Telescope' },\n}"),
    turn("重新量。",
         "Bash", "nvim --startuptime /tmp/st2.log +q && tail -2 /tmp/st2.log",
         "42.118  003.221: sourcing ~/.config/nvim/init.lua\n42.140  000.022: VimEnter autocommands"),
    turn("318ms → 42ms。34 个插件里启动时只加载 2 个。"),
])


# ── 短会话：真实语料里占大多数的那类「两三轮就完事」──────────────

topic("api-gateway", "JWT 的 exp 校验是不是没算时钟偏移", [
    turn("", "Bash", "rg -n 'ExpiresAt|Leeway|ClockSkew' internal/auth/",
         "internal/auth/jwt.go:58:\tif claims.ExpiresAt.Before(time.Now()) {"),
    turn("""确实没有。多台机器之间有几百毫秒偏差是常态，
边界上的 token 会被随机拒掉。给 30 秒容差。"""),
    turn("", "Edit", "internal/auth/jwt.go",
         "-\tif claims.ExpiresAt.Before(time.Now()) {\n+\t// 多机之间时钟有偏差，边界上的 token 会被随机拒掉\n+\tif claims.ExpiresAt.Before(time.Now().Add(-clockSkew)) {"),
])

topic("api-gateway", "健康检查为什么会把服务打挂", [
    turn("", "Bash", "kubectl get pods -l app=gateway",
         "NAME                       READY   STATUS    RESTARTS   AGE\ngateway-7d9f8b6c4-x2klm    1/1     Running   14         3h\ngateway-7d9f8b6c4-mn4pq    1/1     Running   11         3h"),
    turn("3 小时重启 14 次。看下探针配的什么。",
         "Bash", "kubectl get deploy gateway -o jsonpath='{.spec.template.spec.containers[0].livenessProbe}' | jq",
         "{\n  \"httpGet\": { \"path\": \"/healthz\", \"port\": 8080 },\n  \"initialDelaySeconds\": 5,\n  \"periodSeconds\": 5,\n  \"timeoutSeconds\": 1\n}"),
    turn("""`/healthz` 会去 ping 数据库——数据库慢一点，探针超时，pod 被杀。
杀了之后流量转到别的 pod，那台也被压垮，连锁重启。

**liveness 只该看进程活着**（`/livez`，直接返回 200），
**readiness 才查依赖**（`/readyz`）。前者失败会重启，后者失败只是摘流量。"""),
    turn("", "Edit", "deploy/k8s/deployment.yaml",
         "         livenessProbe:\n           httpGet:\n-            path: /healthz\n+            path: /livez\n+        readinessProbe:\n+          httpGet:\n+            path: /readyz"),
])

topic("search-service", "SQLite 并发写报 database is locked", [
    turn("", "Bash", "sqlite3 idx.db 'pragma journal_mode'",
         "delete"),
    turn("""还是 rollback journal，写的时候会锁整个库。

WAL 模式下读写不互斥，单写多读的场景够用了。再配个 `busy_timeout`，
偶发的写冲突自己退避重试，不用抛给上层。"""),
    turn("", "Bash", "sqlite3 idx.db 'pragma journal_mode=WAL; pragma busy_timeout=5000'",
         "wal"),
])

topic("search-service", "全文索引重建要 40 分钟，能不能增量", [
    turn("", "Bash", "sqlite3 idx.db \"select count(*), sum(length(body))/1024/1024 from docs\"",
         "3176|549"),
    turn("""3176 篇 / 549 MB。全量重建慢是因为**每次都从头解析源文件**。

源文件只追加不改写，所以可以记住上次读到哪（size + mtime + offset），
下次从 offset 继续。真被改写了（size < offset 或 mtime 变了）再整篇重来。"""),
    turn("", "Write", "internal/index/watermark.go",
         "// Watermark 记住上次读到哪。\n//\n// size 与 mtime 一起判：只看 mtime 会漏掉「同一秒内追加」，\n// 只看 size 会漏掉「长度不变但内容改了」。\ntype Watermark struct {\n\tSize   int64\n\tMTime  int64\n\tOffset int64\n}"),
    turn("", "Bash", "./search-service index --quiet && ./search-service index --quiet",
         "首次：3176 篇 / 41m12s\n增量：0 篇变化 / 1.8s"),
])

topic("etl-pipeline", "日志里全是 base64，占了 80% 的体积", [
    turn("", "Bash", "du -sh /var/log/etl/ && grep -c 'base64' /var/log/etl/worker-0.log",
         "12G\t/var/log/etl/\n88214"),
    turn("""附件内容被整个打进了日志。这类东西**不该记正文，只该记元数据**——
大小、类型、校验和，够排查就行。"""),
    turn("", "Edit", "internal/etl/log.go",
         "-\tslog.Info(\"attachment\", \"payload\", b64)\n+\t// 附件正文不进日志：12 GB 里 80% 是这个，而排查只需要元数据\n+\tslog.Info(\"attachment\", \"bytes\", len(raw), \"mime\", mime, \"sha256\", sum[:12])"),
    turn("", "Bash", "du -sh /var/log/etl/",
         "2.1G\t/var/log/etl/"),
])

topic("etl-pipeline", "CI 里有个测试偶尔挂，本地怎么都复现不了", [
    turn("", "Bash", "go test ./internal/etl -run TestFlush -count=200",
         "--- FAIL: TestFlush (0.00s)\n    flush_test.go:62: 期望 3 条，实得 2 条\nFAIL\nok  \tetl-pipeline/internal/etl\t1.842s  (199/200 passed)"),
    turn("200 轮复现一次。加 `-race` 看看。",
         "Bash", "go test ./internal/etl -run TestFlush -count=50 -race",
         "==================\nWARNING: DATA RACE\nWrite at 0x00c0001a4090 by goroutine 12:\n  etl.(*Worker).Flush()\n      internal/etl/worker.go:88 +0x64\n\nPrevious read at 0x00c0001a4090 by goroutine 7:\n  etl.(*Worker).Len()\n      internal/etl/worker.go:71 +0x38\n=================="),
    turn("""`buf` 的读写没加锁。`Len()` 在另一个 goroutine 里读，
偶尔读到写了一半的状态。

这类问题**本地跑 100 遍可能都碰不上**，但 CI 的机器负载不同，调度一变就撞上。"""),
    turn("", "Edit", "internal/etl/worker.go",
         " func (w *Worker) Len() int {\n+\tw.mu.RLock()\n+\tdefer w.mu.RUnlock()\n \treturn len(w.buf)\n }"),
    turn("", "Bash", "go test ./internal/etl -run TestFlush -count=500 -race",
         "ok  \tetl-pipeline/internal/etl\t24.118s"),
])

topic("web-console", "npm audit 报了 3 个高危，但升级会破坏构建", [
    turn("", "Bash", "npm audit --audit-level=high",
         "# npm audit report\n\npostcss  <8.4.31\nSeverity: high\nline return parsing error - GHSA-7fh5-64p2-3v2j\nfix available via `npm audit fix --force`\nWill install vite@4.5.14, which is a breaking change\n\n3 high severity vulnerabilities"),
    turn("""`--force` 会把 vite 从 5 降到 4，不能这么干。

postcss 是 vite 的传递依赖，用 `overrides` 单独顶上去就行，
不用动 vite 本身。"""),
    turn("", "Edit", "package.json",
         "+  \"overrides\": {\n+    \"postcss\": \"^8.4.31\"\n+  }"),
    turn("", "Bash", "npm install && npm audit --audit-level=high && npm run build",
         "found 0 vulnerabilities\n\nvite v5.4.2 building for production...\n✓ 312 modules transformed.\n✓ built in 3.74s"),
])

topic("web-console", "表单提交后没反应，也没有报错", [
    turn("", "Bash", "rg -n 'onSubmit' src/pages/Settings.tsx",
         "112:      <form onSubmit={handleSubmit}>"),
    turn("", "Bash", "sed -n '78,92p' src/pages/Settings.tsx",
         "  const handleSubmit = async (e: FormEvent) => {\n    e.preventDefault()\n    try {\n      await api.post('/api/settings', form)\n      toast.success('已保存')\n    } catch {\n      // TODO\n    }\n  }"),
    turn("""`catch` 是空的——请求失败时什么都不做，用户看到的就是「点了没反应」。

这类**吞掉错误**比抛出来更糟：抛出来至少控制台有痕迹。"""),
    turn("", "Edit", "src/pages/Settings.tsx",
         "-    } catch {\n-      // TODO\n-    }\n+    } catch (err) {\n+      toast.error(err instanceof Error ? err.message : '保存失败')\n+    }"),
])

topic("dotfiles", "git 提交时想自动跑 gofmt，但不要拦住 WIP 提交", [
    turn("", "Write", ".git/hooks/pre-commit",
         "#!/bin/sh\n# 只格式化已暂存的 .go 文件；WIP 提交用 --no-verify 跳过\ngofmt -l -w $(git diff --cached --name-only --diff-filter=ACM | grep '\\.go$')\ngit add $(git diff --cached --name-only --diff-filter=ACM | grep '\\.go$')"),
    turn("试一下。",
         "Bash", "git commit -m 'test' --dry-run",
         "gofmt: internal/etl/worker.go\n1 file reformatted"),
])

topic("dotfiles", "tmux 里 nvim 的配色不对，颜色发灰", [
    turn("", "Bash", "echo $TERM && tmux display -p '#{client_termname}'",
         "xterm-256color\nscreen-256color"),
    turn("""tmux 内部报的是 `screen-256color`，不支持真彩色，
所以 24 位颜色被降级成 256 色。

要开 `terminal-overrides`，把 `Tc`（truecolor）能力显式告诉它。"""),
    turn("", "Edit", "~/.tmux.conf",
         "+set -g default-terminal 'tmux-256color'\n+set -ga terminal-overrides ',*256col*:Tc'"),
    turn("", "Bash", "tmux kill-server; tmux new -d; tmux display -p '#{client_termname}'",
         "tmux-256color"),
])

topic("api-gateway", "把 API 文档从手写 markdown 换成 OpenAPI 生成", [
    turn("", "Bash", "rg -c 'swagger:route|@Router' internal/server/",
         "internal/server/handlers.go:0"),
    turn("一条注解都没有，得从头加。先看有多少个路由。",
         "Bash", "rg -c 'r\\.(Get|Post|Put|Delete)' internal/server/router.go",
         "23"),
    turn("""23 个。手写注解容易漏，而漏了不会有任何提示——
所以要在 CI 里加一条：**路由数与文档条目数必须相等**。"""),
    turn("", "Write", "scripts/check-openapi.sh",
         "#!/bin/sh\n# 路由数与 OpenAPI 里的条目数必须相等。\n# 漏写注解不会报错，只会静默少一条文档——所以要显式对数。\nroutes=$(rg -c 'r\\.(Get|Post|Put|Delete)' internal/server/router.go)\ndocs=$(yq '.paths | to_entries | map(.value | keys) | flatten | length' docs/openapi.yaml)\n[ \"$routes\" = \"$docs\" ] || { echo \"路由 $routes 条，文档 $docs 条，对不上\"; exit 1; }"),
    turn("", "Bash", "sh scripts/check-openapi.sh",
         "路由 23 条，文档 19 条，对不上"),
    turn("补上漏的 4 条。"),
])

topic("search-service", "想给检索做个压测，看能扛多少 QPS", [
    turn("", "Write", "load/search.js",
         "import http from 'k6/http'\nimport { check } from 'k6'\n\nexport const options = {\n  stages: [\n    { duration: '30s', target: 100 },\n    { duration: '2m',  target: 500 },\n    { duration: '30s', target: 0 },\n  ],\n}\n\nconst TERMS = ['限流', 'postgres', '证书续期', 'kafka 积压', 'FTS5']\n\nexport default function () {\n  const q = TERMS[Math.floor(Math.random() * TERMS.length)]\n  const res = http.get(`http://localhost:8080/api/v1/search?q=${encodeURIComponent(q)}`)\n  check(res, { 'status 200': (r) => r.status === 200 })\n}"),
    turn("", "Bash", "k6 run load/search.js",
         "     checks.........................: 100.00% ✓ 184203     ✗ 0\n     http_req_duration..............: avg=41.2ms  min=8.1ms  med=28ms  p(95)=118ms p(99)=214ms\n     http_reqs......................: 184203  1023.4/s\n     vus_max........................: 500"),
    turn("500 并发下 1023 QPS，p99 214ms。瓶颈在 SQLite 的单写锁，读是够的。"),
])

topic("etl-pipeline", "给 etl 补上断点续跑，重启后不要从头来", [
    turn("", "Bash", "rg -n 'checkpoint|resume' internal/etl/",
         ""),
    turn("""一处都没有——现在重启就是从头跑。

检查点存哪是关键：存本地文件的话，容器一换就没了；
存 Postgres 和数据本身在一个事务里，才能保证「写了数据一定记了位置」。"""),
    turn("", "Write", "internal/etl/checkpoint.go",
         "// 检查点与数据写在**同一个事务**里。\n//\n// 分开写的话，两者之间崩溃会出现两种坏情况之一：\n// 先写数据后记位置 → 重启后重复处理；\n// 先记位置后写数据 → 重启后丢数据。\nfunc (w *Worker) commit(ctx context.Context, tx *sql.Tx, chunk Chunk) error {\n\tif err := w.writeRows(ctx, tx, chunk.Rows); err != nil {\n\t\treturn err\n\t}\n\t_, err := tx.ExecContext(ctx,\n\t\t`INSERT INTO etl_checkpoints (partition, offset) VALUES ($1, $2)\n\t\t ON CONFLICT (partition) DO UPDATE SET offset = EXCLUDED.offset`,\n\t\tchunk.Partition, chunk.To)\n\treturn err\n}"),
    turn("", "Bash", "go test ./internal/etl -run TestResume -v",
         "=== RUN   TestResume_AfterCrash\n--- PASS: TestResume_AfterCrash (0.12s)\n=== RUN   TestResume_NoDuplicateOnRetry\n--- PASS: TestResume_NoDuplicateOnRetry (0.09s)\nPASS"),
])

topic("web-console", "Docker 镜像 1.8G，CI 拉一次要三分钟", [
    turn("", "Bash", "docker history web-console:latest --format '{{.Size}}\\t{{.CreatedBy}}' | head -6",
         "1.12GB\tRUN npm install\n 412MB\tCOPY . /app\n 184MB\tRUN apt-get install -y build-essential\n  84MB\tFROM node:20\n  12MB\tRUN npm run build\n   0B\tCMD [\"node\", \"server.js\"]"),
    turn("""构建期的东西全留在了最终镜像里。多阶段构建，
最后一层只拷 `dist/` 和运行时依赖。"""),
    turn("", "Write", "Dockerfile",
         "# ── 构建阶段 ──\nFROM node:20 AS build\nWORKDIR /app\nCOPY package*.json ./\nRUN npm ci\nCOPY . .\nRUN npm run build && npm prune --omit=dev\n\n# ── 运行阶段：只带走产物与运行时依赖 ──\nFROM node:20-slim\nWORKDIR /app\nCOPY --from=build /app/dist ./dist\nCOPY --from=build /app/node_modules ./node_modules\nCMD [\"node\", \"dist/server.js\"]"),
    turn("", "Bash", "docker build -t web-console:slim . && docker images web-console --format '{{.Tag}}\\t{{.Size}}'",
         "slim\t198MB\nlatest\t1.8GB"),
    turn("1.8G → 198MB。"),
])

topic("api-gateway", "Redis 缓存击穿，热 key 过期那一刻全打到数据库", [
    turn("", "Bash", "redis-cli --hotkeys 2>/dev/null | tail -5",
         "[100.00%] Keys sampled: 41882\nhot key found with counter: 18204\tkeyname: search:hot:trending\nhot key found with counter: 12441\tkeyname: search:hot:latest\nhot key found with counter:  8112\tkeyname: user:profile:1024"),
    turn("""`search:hot:trending` 一个 key 占了 18204 次访问。
它一过期，所有并发请求同时穿到数据库。

**singleflight**：同一个 key 的并发回源合并成一次，其余等结果。"""),
    turn("", "Edit", "internal/cache/cache.go",
         "+// 同一个 key 的并发回源合并成一次——热 key 过期那一刻\n+// 会有上千个请求同时穿透，合并后只有一个真正打到数据库。\n+var group singleflight.Group\n+\n+func (c *Cache) GetOrLoad(ctx context.Context, key string, load func() (any, error)) (any, error) {\n+\tv, err, _ := group.Do(key, load)\n+\treturn v, err\n+}"),
    turn("", "Bash", "go test ./internal/cache -run TestSingleflight -race -count=20",
         "ok  \tapi-gateway/internal/cache\t0.884s"),
])


# ── 极短会话：问一句、答一句、可能跑一条命令就结束 ────────────────
# 真实语料里这类占比最高，而且正是「搜到了才想起来」的那些。

QUICK = [
    ("api-gateway", "chi 的 middleware 顺序是先注册先执行吗",
     "是。`r.Use` 按注册顺序包裹，最先注册的在最外层——所以 Recoverer 要早于业务中间件，"
     "不然业务里 panic 了它兜不住。", None, None),
    ("api-gateway", "http.Server 的 ReadTimeout 和 ReadHeaderTimeout 差在哪",
     "`ReadHeaderTimeout` 只管读完请求头，`ReadTimeout` 管整个请求体读完。\n\n"
     "上传接口要把 `ReadTimeout` 放宽甚至设 0，但 `ReadHeaderTimeout` 必须留着——"
     "否则慢速攻击（Slowloris）能一直占着连接不发完头。", None, None),
    ("api-gateway", "grpc 和 http 用同一个端口怎么分流",
     "`cmux` 按前几个字节判协议，或者用 `h2c` + `Handler` 里判 `Content-Type: application/grpc`。\n\n"
     "前者不改业务代码，后者少一层。生产上我用前者——协议判定错了至少能从 cmux 的日志看出来。", None, None),
    ("search-service", "FTS5 的 bm25() 返回的是负数？",
     "是，SQLite 的 `bm25()` 返回**负值**，越小越相关。\n\n"
     "所以排序要 `ORDER BY bm25(fts)` 而不是 `DESC`——写反了会把最不相关的排最前面，"
     "而结果看起来「有排序」，很难发现。", None, None),
    ("search-service", "外部内容表（external content）省多少空间",
     None, "Bash",
     "sqlite3 idx.db \"select name, sum(pgsize)/1024/1024 mb from dbstat group by name order by mb desc limit 4\"",
     "docs_fts_data|41\ndocs|38\ndocs_fts_idx|12\ndocs_fts_docsize|2"),
    ("search-service", "snippet() 能不能自定义高亮标记",
     "能，第三、四个参数就是。但**不要用 HTML 标签**——正文里可能本来就有 `<mark>`，"
     "转义顺序一错就等于把历史内容里的任意 HTML 注进页面。\n\n"
     "用控制字符（比如 U+0002/U+0003）做标记，前端**先整体转义、再替换成标签**。", None, None),
    ("etl-pipeline", "Postgres 的 COPY 比批量 INSERT 快多少",
     None, "Bash",
     "psql -c \"\\timing on\" -c \"insert into t select * from generate_series(1,100000)\" -c \"copy t from '/tmp/100k.csv'\"",
     "INSERT 0 100000\nTime: 4128.221 ms\nCOPY 100000\nTime: 284.114 ms"),
    ("etl-pipeline", "upsert 冲突时怎么只更新变了的列",
     "`ON CONFLICT DO UPDATE SET ... WHERE` 加个条件：\n\n"
     "```sql\nON CONFLICT (id) DO UPDATE\nSET body = EXCLUDED.body, updated_at = now()\nWHERE t.body IS DISTINCT FROM EXCLUDED.body\n```\n\n"
     "`IS DISTINCT FROM` 而不是 `!=`——后者遇到 NULL 会返回 NULL，等于不更新。", None, None),
    ("etl-pipeline", "jsonb 和 json 该用哪个",
     "存了要查就 `jsonb`（有 GIN 索引，但写入要解析）；只是原样存档就 `json`（保留键序与空白）。\n\n"
     "我们这边是要查的，用 `jsonb`。注意它**不保留键的顺序**，"
     "如果哪里靠键序做校验和会出问题。", None, None),
    ("etl-pipeline", "怎么看某张表最近被 vacuum 过没",
     None, "Bash",
     "psql -c \"select relname, last_autovacuum, n_dead_tup from pg_stat_user_tables order by n_dead_tup desc limit 3\"",
     "   relname    |        last_autovacuum        | n_dead_tup \n--------------+-------------------------------+------------\n etl_chunks   | 2026-06-18 02:14:08.221+02    |    1284203\n etl_runs     | 2026-06-21 03:01:44.118+02    |       8841\n orders       | 2026-06-21 03:02:11.904+02    |        412"),
    ("web-console", "useEffect 里的 fetch 怎么正确取消",
     "用 `AbortController`，并在清理函数里 abort：\n\n"
     "```ts\nuseEffect(() => {\n  const ac = new AbortController()\n  fetch(url, { signal: ac.signal })\n    .then(setData)\n    .catch((e) => { if (e.name !== 'AbortError') setError(e) })\n  return () => ac.abort()\n}, [url])\n```\n\n"
     "`catch` 里要把 `AbortError` 排掉——否则组件卸载会被当成一次真实错误报出来。", None, None),
    ("web-console", "为什么 key 用 index 会出问题",
     "列表顺序变了之后，React 按 index 复用节点，于是**状态跟错了行**——"
     "比如你在第 2 行输了字，删掉第 1 行之后那些字跑到别的行去了。\n\n"
     "有稳定 id 就用 id；实在没有，至少别在会重排/增删的列表上用 index。", None, None),
    ("web-console", "CSS 里 :has() 现在能用了吗",
     None, "Bash", "npx browserslist 'defaults' | head -6",
     "and_chr 128\nand_ff 131\nchrome 128\nchrome 127\nedge 128\nfirefox 131"),
    ("web-console", "vite 的 base 配置和 router 的 basename 要一起改吗",
     "要。前者管资源路径（构建期），后者管路由匹配（运行期），"
     "只改一个的表现是「页面能开但样式全丢」或者「资源加载正常但路由 404」。\n\n"
     "我把它们都读同一个环境变量，避免两处漂移。", None, None),
    ("dotfiles", "ssh 的 ControlMaster 会不会有副作用",
     "有一个：主连接断了，复用它的会话**全部一起断**。\n\n"
     "所以 `ControlPersist` 别设太长，跳板机上尤其。我设 10 分钟，"
     "够连续操作复用，又不至于挂太久。", None, None),
    ("dotfiles", "systemd --user 的服务开机自启要 enable-linger 吗",
     None, "Bash", "loginctl show-user $USER --property=Linger",
     "Linger=no"),
    ("dotfiles", "rg 怎么搜被 gitignore 掉的文件",
     "`rg -u` 一次放宽一层：`-u` 搜 ignore 掉的、`-uu` 连隐藏文件、`-uuu` 连二进制。\n\n"
     "**这是个真会骗人的默认值**——搜不到时你会以为「不存在」，"
     "其实只是被 ignore 规则挡住了。", None, None),
    ("dotfiles", "zsh 里 $path 和 $PATH 是什么关系",
     "绑定的：`path` 是数组、`PATH` 是冒号分隔的字符串，改一个另一个跟着变。\n\n"
     "**所以 `path=$(...)` 会直接把 PATH 冲掉**，之后满屏 `command not found`。"
     "同类的还有 `fpath` / `cdpath` / `manpath`。临时变量别用这些名字。", None, None),
    ("api-gateway", "pprof 的 web 界面怎么在服务器上看",
     None, "Bash", "go tool pprof -http=:0 -no_browser cpu.out",
     "Serving web UI on http://localhost:41883"),
    ("search-service", "go 的 embed 能嵌入子目录吗",
     "能，`//go:embed static` 会把整个目录嵌进去，但**默认跳过 `_` 和 `.` 开头的文件**。\n\n"
     "要带上它们得写 `//go:embed all:static`。我在这上面栽过一次——"
     "`.well-known` 目录死活出不来。", None, None),
]

for _q in QUICK:
    # 纯文本那些只有 5 个字段，带命令的有 6 个——补齐再解包
    _proj, _title, _text, _tool, _cmd, _out = (list(_q) + [None] * 6)[:6]
    if _tool:
        topic(_proj, _title, [turn(_text or "", _tool, _cmd, _out)])
    else:
        topic(_proj, _title, [turn(_text)])


# ── 一条会触发「已截断」的会话 ──────────────────────────────────
#
# 索引对工具结果按 tool_result_cap（默认 4096 字节）截断，界面上标「已截断」
# 并显示原始体积。语料里得真有一条超阈值的，否则那个角标在截图里永远不出现。

_BUILD_LOG = "\n".join(
    f"[{i:4d}/1284] compiling github.com/example/api-gateway/internal/"
    f"{mod} ... ok ({ms} ms)"
    for i, (mod, ms) in enumerate(
        [(m, 40 + (i * 37) % 900) for i, m in enumerate(
            ["server", "auth", "ratelimit", "cache", "db", "config", "telemetry",
             "middleware", "handlers", "store", "queue", "worker"] * 8)], 1)
)

topic("api-gateway", "全量构建从 40 秒涨到 4 分钟，看下是哪一步慢了", [
    turn("先把完整的构建日志打出来。",
         "Bash", "go build -v ./... 2>&1", _BUILD_LOG),
    turn("""日志太长，直接看耗时最长的几个包。"""),
    turn("", "Bash",
         "go build -v ./... 2>&1 | grep -oP '\\((\\d+) ms\\)' | tr -d '( ms)' | sort -rn | head -5",
         "912\n898\n874\n851\n829"),
    turn("""没有单个包特别慢——是**包的数量**涨了。

`internal/` 下现在 96 个包，其中一半是只有一两个文件的小包。
Go 的编译单元是包，包多了并行度上不去、链接也变慢。

先加构建缓存，再考虑合并那些过细的包。"""),
    turn("", "Bash", "go build -v ./... 2>&1 | tail -3",
         "github.com/example/api-gateway/internal/worker\ngithub.com/example/api-gateway/cmd/gateway\nok  4m02s"),
])


# ══════════════════════════════════════════════════════════════════
# 落盘
# ══════════════════════════════════════════════════════════════════

# 每个会话首条 user 消息前都被注入 CLAUDE.md 全文——真实文件里就是这样，
# 索引时会被剥离。语料里保留它，才能验证剥离确实生效。
INJECT = ("<system-reminder>\n# 项目约定\n\n- 提交信息用中文，一行说清做了什么\n"
          "- 改动前先跑 `make test`\n- 不引入新依赖前先问\n</system-reminder>\n\n")

TOOL_INPUT = {
    "Bash": lambda cmd: {"command": cmd},
    "Read": lambda cmd: {"file_path": cmd},
    "Write": lambda cmd: {"file_path": cmd},
    "Edit": lambda cmd: {"file_path": cmd},
}

# 扩展名 → 围栏代码块的语言标记
_LANG = {
    ".go": "go", ".py": "python", ".ts": "ts", ".tsx": "tsx", ".js": "js",
    ".mjs": "js", ".sh": "bash", ".zsh": "bash", ".yaml": "yaml", ".yml": "yaml",
    ".json": "json", ".css": "css", ".md": "markdown", ".lua": "lua",
    ".conf": "nginx", ".ini": "ini", ".service": "ini",
}


def _lang_of(path):
    for ext, lang in _LANG.items():
        if path.endswith(ext):
            return lang
    # 没有扩展名的（.zshrc / Dockerfile / pre-commit）按内容猜个大概
    base = path.rsplit("/", 1)[-1]
    if base.startswith(".z") or base in ("pre-commit", "cli.ini"):
        return "bash"
    if base == "Dockerfile":
        return "dockerfile"
    return ""


def normalize(tn):
    """把剧本里的一轮调整成真实工具**结果**的样子。

    真实的 `Write` 结果是一句「File created successfully at: …」，
    文件内容出现在助手那段话的围栏代码块里——而不是塞进工具结果。
    写反了的话，内容既不会被 Markdown 渲染，也不会被语法高亮
    （判定只认读文件类命令），截图上就是一大坨黑白文字。
    """
    if tn["tool"] not in ("Write", "Edit") or not tn["out"]:
        return tn
    path, body = tn["cmd"], tn["out"]
    if tn["tool"] == "Write":
        lang = _lang_of(path)
        block = f"\n\n```{lang}\n{body}\n```"
        return {**tn, "text": (tn["text"] or f"写进 `{path}`：") + block,
                "out": f"File created successfully at: {path}"}
    # Edit：结果里给出改动片段，与真实形态一致
    return {**tn, "out": f"The file {path} has been updated. 改动如下：\n{body}"}


def write_claude(path, proj, uid, start, title, turns, custom_title=None, agent_name=None):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    lines, t = [], start

    def emit(o):
        lines.append(json.dumps(o, ensure_ascii=False))

    emit({"type": "user", "timestamp": ts(t), "cwd": proj, "sessionId": uid,
          "message": {"role": "user", "content": INJECT + title}})
    if custom_title:
        emit({"type": "custom-title", "timestamp": ts(t), "sessionId": uid,
              "customTitle": custom_title})
    if agent_name:
        emit({"type": "agent-name", "timestamp": ts(t), "sessionId": uid,
              "agentName": agent_name})

    for i, tn in enumerate(turns):
        tn = normalize(tn)
        t += timedelta(seconds=random.randint(18, 210))
        content = []
        if tn["text"]:
            content.append({"type": "text", "text": tn["text"]})
        if tn["tool"]:
            content.append({"type": "tool_use", "id": f"t{i}", "name": tn["tool"],
                            "input": TOOL_INPUT[tn["tool"]](tn["cmd"])})
        emit({"type": "assistant", "timestamp": ts(t), "cwd": proj, "sessionId": uid,
              "message": {"role": "assistant", "content": content}})
        if tn["tool"]:
            t += timedelta(seconds=random.randint(1, 24))
            emit({"type": "user", "timestamp": ts(t), "cwd": proj, "sessionId": uid,
                  "message": {"role": "user", "content": [
                      {"type": "tool_result", "tool_use_id": f"t{i}", "content": tn["out"]}]}})

    open(path, "w", encoding="utf-8").write("\n".join(lines) + "\n")
    return t


def write_codex(path, proj, start, title, turns):
    """Codex 的 rollout 格式与 Claude 完全不同——语料里两种都要有，
    否则「两套 JSONL 格式不同」那一节没有东西可佐证。"""
    os.makedirs(os.path.dirname(path), exist_ok=True)
    lines, t = [], start

    def emit(o):
        lines.append(json.dumps(o, ensure_ascii=False))

    emit({"timestamp": ts(t), "type": "session_meta", "payload": {"cwd": proj}})
    emit({"timestamp": ts(t), "type": "response_item",
          "payload": {"type": "message", "role": "user",
                      "content": [{"type": "input_text", "text": title}]}})
    for tn in turns:
        tn = normalize(tn)
        t += timedelta(seconds=random.randint(18, 200))
        if tn["text"]:
            emit({"timestamp": ts(t), "type": "response_item",
                  "payload": {"type": "message", "role": "assistant",
                              "content": [{"type": "output_text", "text": tn["text"]}]}})
        if tn["tool"]:
            emit({"timestamp": ts(t), "type": "response_item",
                  "payload": {"type": "function_call", "name": "shell",
                              "arguments": json.dumps({"command": tn["cmd"]}, ensure_ascii=False)}})
            t += timedelta(seconds=random.randint(1, 18))
            emit({"timestamp": ts(t), "type": "response_item",
                  "payload": {"type": "function_call_output", "output": tn["out"]}})
    open(path, "w", encoding="utf-8").write("\n".join(lines) + "\n")


def claude_path(proj, uid):
    return f"{HOME}/.claude/projects/{proj.replace('/', '-')}/{uid}.jsonl"


def codex_path(start, n):
    d = start.strftime("%Y/%m/%d")
    return (f"{HOME}/.codex/sessions/{d}/rollout-{start:%Y-%m-%dT%H-%M-%S}"
            f"-{n:08d}-aaaa-bbbb-cccc-dddddddddddd.jsonl")


def uid_for(n):
    return f"{n:08d}-1c4e-4a7b-9f2d-{n:012d}"


# ══════════════════════════════════════════════════════════════════
# 主循环：把剧本铺在三个月的工作日上
# ══════════════════════════════════════════════════════════════════

n = 0
day = 0
order = TOPICS[:]
random.shuffle(order)

for tp in order:
    # 跳过周末——真实语料里周末的会话明显稀疏
    day += random.choice([0, 0, 1, 1, 1, 2, 3])
    d = BASE + timedelta(days=day)
    while d.weekday() >= 5:
        day += 1
        d = BASE + timedelta(days=day)
    start = d + timedelta(hours=random.randint(0, 9), minutes=random.randint(0, 59))
    n += 1
    proj = PROJECTS[tp["project"]]
    # 五分之一走 Codex，其余 Claude
    if n % 5 == 0:
        write_codex(codex_path(start, n), proj, start, tp["title"], tp["turns"])
    else:
        write_claude(claude_path(proj, uid_for(n)), proj, uid_for(n), start,
                     tp["title"], tp["turns"], tp["custom_title"])

print(f"主线 {n} 个会话")


# ══════════════════════════════════════════════════════════════════
# 子代理：主会话派出去几个 agent 分头查
#
# 子代理的判定看目录结构：<project>/<父会话uid>/subagents/agent-<x>.jsonl
# ══════════════════════════════════════════════════════════════════

SUBAGENTS = [
    ("查清限流中间件现在插在链路的什么位置", "链路侦察",
     [turn("", "Bash", "rg -n 'r\\.Use' internal/server/router.go",
           "40:\tr.Use(middleware.RequestID)\n41:\tr.Use(middleware.RealIP)\n42:\tr.Use(middleware.Logger)\n43:\tr.Use(middleware.Recoverer)\n44:\tr.Use(auth.Middleware(cfg.JWT))\n45:\tr.Use(ratelimit.Middleware(cfg.RateLimit))"),
      turn("限流在第 45 行，**鉴权之后**——匿名请求先被 401 挡掉，不占桶。位置是对的。")]),

    ("对比令牌桶与滑动窗口在高并发下的内存开销", "算法开销",
     [turn("", "Bash", "go test ./internal/ratelimit -bench . -benchmem",
           "BenchmarkBucket-16       8412104    141 ns/op     0 B/op   0 allocs/op\nBenchmarkSlidingWin-16    412884   2841 ns/op   488 B/op   6 allocs/op"),
      turn("""令牌桶每个 key 常驻两个 `float64`；滑动窗口要存时间戳队列。

10 万个活跃 IP 下：令牌桶约 **3.2 MB**，滑动窗口约 **190 MB**。
差 60 倍，选令牌桶。""")]),

    ("找出所有会被限流影响的现有接口", "影响面",
     [turn("", "Bash", "rg -o 'r\\.(Get|Post|Put|Delete)\\(\"[^\"]+' internal/server/router.go | sed 's/.*\"//' | sort",
           "/api/v1/documents\n/api/v1/documents/{id}\n/api/v1/search\n/api/v1/search/suggest\n/healthz\n/livez\n/metrics\n/readyz"),
      turn("""23 个路由，其中 **4 个是探活/指标类**（`/healthz` `/livez` `/readyz` `/metrics`），
必须豁免——它们本来就该高频，被限流挡住会导致 k8s 误判服务已死。""")]),

    ("把限流的配置项补进配置文档", "文档",
     [turn("", "Edit", "docs/config.md",
           "+## ratelimit\n+\n+| 键 | 类型 | 默认 | 说明 |\n+|---|---|---|---|\n+| `enabled` | bool | `false` | 总开关 |\n+| `by` | enum | `ip` | `ip` / `token` / `route` |\n+| `rate` | int | `20` | 每秒补充的令牌数 |\n+| `burst` | int | `40` | 桶容量，决定能攒多大突发 |\n+| `exempt` | []string | 探活类路由 | 豁免的路由前缀 |"),
      turn("`docs/config.md` 加了 ratelimit 一节，五个键都写了默认值与含义。")]),

    ("跑一轮压测确认限流真的生效", "压测",
     [turn("", "Bash", "k6 run --vus 500 --duration 2m load/ratelimit.js",
           "     http_reqs......................: 184203  1535.0/s\n     http_req_duration..............: avg=38ms  p(95)=112ms p(99)=180ms\n     ✓ 放行的请求返回 200.........: 181083\n     ✓ 超限的请求返回 429.........: 3120\n     ✗ 5xx..........................: 0"),
      turn("500 并发下被限流的 3120 个请求全部返回 **429**，没有 5xx，通过的 p99 是 180ms。限流生效且没有把服务压垮。")]),
]

n += 1
parent_uid = uid_for(n)
proj = PROJECTS["api-gateway"]
pdir = f"{HOME}/.claude/projects/{proj.replace('/', '-')}"
pstart = BASE + timedelta(days=day + 2, hours=2, minutes=12)

write_claude(f"{pdir}/{parent_uid}.jsonl", proj, parent_uid, pstart,
             "给 api-gateway 的限流做一轮完整评估，派几个 agent 分头查", [
                 turn("""我派五个子 agent 分头查，各自独立跑，回来汇总：

1. 链路侦察 —— 限流现在插在哪
2. 算法开销 —— 令牌桶 vs 滑动窗口
3. 影响面 —— 哪些接口会被影响
4. 文档 —— 配置项补齐
5. 压测 —— 确认真的生效"""),
                 turn("", "Bash", "ls internal/ratelimit/",
                      "bucket.go\nbucket_test.go\nconfig.go\nmiddleware.go\nmiddleware_test.go\nstore.go"),
                 turn("""五个方向都回来了：

| 方向 | 结论 |
|---|---|
| 链路 | 在鉴权之后，位置正确 |
| 算法 | 令牌桶 3.2 MB vs 滑动窗口 190 MB，**差 60 倍** |
| 影响面 | 23 个路由，4 个探活类必须豁免 |
| 文档 | 已补 `docs/config.md` 的 ratelimit 一节 |
| 压测 | 500 并发下 429 正常、零 5xx、p99 180ms |

唯一要动的是**探活路由的豁免**——现在配置里写死了 `/healthz`，
但实际有四个。"""),
             ], custom_title="限流完整评估")

for i, (sub_title, agent_name, sub_turns) in enumerate(SUBAGENTS):
    write_claude(f"{pdir}/{parent_uid}/subagents/agent-{i:016x}.jsonl",
                 proj, f"{parent_uid}-sub{i}",
                 pstart + timedelta(minutes=3 + i * 6),
                 sub_title, sub_turns, agent_name=agent_name)
    n += 1

print(f"追加 1 个主会话 + {len(SUBAGENTS)} 个子代理，共 {n} 个会话")
