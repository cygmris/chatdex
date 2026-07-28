# chatdex 架构

单进程 Go 常驻服务。三条主线贯穿全部设计：**只读 + 只本机**、**一份索引三个消费端**、**早期就能用**。

```
~/.claude/projects/**.jsonl ─┐
~/.codex/sessions/**.jsonl  ─┴→ parser.Registry → index.Scanner → SQLite(FTS5)
                                                                      │
                                          ┌───────────────────────────┤
                                          ↓                           ↓
                                   search.Engine ←──────────── summary.Worker → llm.Client
                                    （唯一检索实现）                              (127.0.0.1)
                          ┌───────────────┼───────────────┐
                          ↓               ↓               ↓
                   dashboard :5021   httpapi :5022   mcpserver :5022/mcp
                          └──────→ chat.Agent ──→ mcpserver.Tools（同一份工具实现）
```

## 模块

| 包 | 职责 | 不做什么 |
|---|---|---|
| `internal/model` | 统一消息模型（`SessionMeta` / `Block`） | 不认识任何 JSONL 结构 |
| `internal/parser` | 两套格式各一个解析器 + 注入指令剥离 | 不认识 SQL |
| `internal/index` | 建库、写块、增量水位、摘要队列 | 不做检索 |
| `internal/search` | **全项目唯一的检索实现** + CJK 归一化 | 不认识 JSONL |
| `internal/llm` | 摘要与聊天共用的 LLM 抽象 + 回环强制校验 | 无 embedding（R8 门控） |
| `internal/summary` | 抽稀、分段、后台 worker | 不阻断索引与检索 |
| `internal/chat` | 多轮工具循环 + 预算 | **不实现检索**，转调 mcpserver.Tools |
| `internal/mcpserver` | MCP 端点 + 三个工具（唯一一份工具实现） | 不写 SQL |
| `internal/httpapi` | JSON API + SSE | 不写 SQL |
| `internal/dashboard` | `embed.FS` 静态资源 | 只读，无写入入口 |

## 关键决策与它们的代价

### 1. CJK 用「单字切分 + U+0001 分隔符」，不用 trigram

FTS5 的 `unicode61` 把一整串中文当成一个 token。两个方案在 71.3 MB 真实语料上实测：

| 方案 | 索引库 | 相对文本 | 建库耗时 |
|---|---|---|---|
| `tokenize='trigram'` | 313.2 MB | ×4.39 | 15 s |
| **`unicode61` + CJK 单字切分** | **142.9 MB** | **×2.00** | **3 s** |

选后者。分隔符用 `U+0001` 而非空格是为了**无损**：`Strip` 能逐字节还原原文，
于是索引与展示共用同一份正文，省掉一份 GB 级拷贝。

**索引侧与查询侧必须走同一个 `split()`**——归一化不一致是这个模式最常见的 bug，
`normalize_test.go` 用真实 FTS5 表守住这条。

### 2. 会话排序按「最佳块」而非命中数

实测事故：找一段 restic 对话时，命中最多的两个会话（2272 / 2154 次）都不是目标，
目标只命中 669 次。所以排序用 `MIN(bm25)`，命中数只展示、不参与排序。
`engine_test.go` 里有一条以此事故为 fixture 的回归测试。

### 3. `snippet()` 只对最终展示的那几行算

`bm25()` / `snippet()` 只能用在**直接查询 FTS 表**的语境里，一旦与 `blocks`/`sessions`
同处一个 SELECT（哪怕写成子查询被优化器展平）就会报
`unable to use function bm25 in the requested context`。解法是
`WITH f AS MATERIALIZED (...)` —— `MATERIALIZED` 这个词缺一不可。

更要紧的是**不能在那个 CTE 里算 snippet**：它要把正文从外部内容表取回来重新分词，
代价与全库命中数成正比。实测查「的」（92 864 块命中）：只取 rowid 37 ms、
加 bm25 99 ms、**再加 snippet 2.83 s**；而外层根本不读片段，`fillBest` 又对 20 个
会话各重跑一遍——21 × 2.8 s，正好是修复前那 63.8 秒。

现在片段只按 rowid 单取（`MATCH ? AND rowid = ?`，<1 ms，与命中总数无关）。

### 4. 两个 listener 共用一个 mux

需求把 `5021` 定为前端、`5022` 定为 API+MCP。当两个独立服务写就会跨源；
同进程共享 `ServeMux` 则页面同源，不需要 CORS 也不必反代。

⚠️ Go 1.22 的 `ServeMux` 在**注册期**就对冲突模式 panic：`"GET /"`（方法窄路径宽）
与 `"/mcp/"`（方法宽路径窄）互不包含。所以 dashboard 的根兜底不带方法限定。
这类错误编译不出来、只在启动瞬间炸，`TestMCPEndpointCoexistsWithDashboard` 守住它。

### 5. 单例靠端口绑定，`index` 子命令也争这把锁

两个写者会读到同一个水位、解析同一段追加内容、各写一份块——事务只保证各自原子，
不会互相察觉，结果是同一 `seq` 出现重复块。所以 `serve` 与 `index` 抢同一个端口，
且**在打开索引库之前**就退出。

### 6. 高亮哨兵是控制字符，不是 `<mark>`

片段正文是会话原文，里面完全可能有 `<script>`。后端吐 `U+0002/U+0003`，
前端**先整体 HTML 转义、再**换成 `<mark>`；顺序反了就等于把历史会话里的任意 HTML
注进页面。给 MCP 与聊天 agent 的文本则走 `StripAll` 全部去掉——
实测本地模型会把 `\x02 \x03` 原样抄进给用户看的答案里。

## 实测数据（2026-07-29，本机真实语料）

| 指标 | 值 |
|---|---|
| 会话文件 | 3176（Claude 3013 含子代理 / Codex 163） |
| 内容块 | 632 322（tool_use 248k / tool_result 244k / assistant 119k / user 20k） |
| 入库正文 | 0.53 GB |
| 索引库 | 1.0 GB（= 正文的 1.9×） |
| 全量索引 | 13 分 0 秒 |
| 检索延迟 | 中位 **23 ms**，20 条真实查询里 19 条 < 260 ms |
| 最慢查询 | 342 ms（`的`，单个 CJK 常用字，命中近十万块） |
| 摘要吞吐 | 21.8 s/会话（含 500 ms 限速）→ 全量约 19.3 小时 |

「亚秒级检索」这条非功能需求达成，最坏情况仍有约 3 倍余量。

## 安全约束（不可放宽，逐条有代码与测试）

| 约束 | 实现 | 守住它的测试 |
|---|---|---|
| 只读会话文件 | 一律 `os.Open`（`O_RDONLY`） | E2E：跑完整流程后原始文件 size/mtime/内容逐字节相等 |
| 只监听 `127.0.0.1` | `const loopback`，地址不做成配置项 | 集成测试真的去本机局域网 IP 上连一次，连得上就失败 |
| 索引库 `0600` | `Open` 显式 chmod 主库与 `-wal`/`-shm` | 单测 + E2E 双重断言 |
| LLM 只允许回环 | `requireLoopback`，构造即失败，无开关 | 7 个远端/内网/通配端点的负例 |
| 无向量索引（R8 门控） | 代码里无 embedding 表/列/方法 | `grep -rniE 'embedding\|vector'` 仅命中注释 |
