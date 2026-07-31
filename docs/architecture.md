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
| `internal/config` | 配置读写 + 字段元信息 + `Live` 热生效 | 不认识 HTTP |
| `internal/dashboard` | `embed.FS` 静态资源（外壳 + 四套主题 + 六个视图） | 对**会话数据**只读；写入仅限 chatdex 自己的配置 |

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

### 6. 摘要块存在 `seq = -1`

摘要要能被关键词检索命中（R11.2），所以它必须是 `blocks` 里的一行、进 FTS5；
但它不是对话的一部分，不该出现在回读视图里。用负序号同时满足两者：
回读从 `seq >= 0` 翻页，摘要自然被排除。

⚠️ **代价是一条容易漏的不变式：凡是「数消息」的地方都必须带 `seq >= 0`。**
漏掉它的后果不是报错而是错数——`GetSession` 的 `Total` 会比列出的条数多 1，
消息数正好是整页倍数时前端就多出一个点进去空白的「下一页」；
`msg_count` 则会在已摘要会话的下次追加时 +1，而那个数字正是检索结果与
时间线上显示的「N 条消息」。这个 bug 真的发生过，靠 `repairs` 自愈存量数据修回来的。

### 7. 高亮哨兵是控制字符，不是 `<mark>`

片段正文是会话原文，里面完全可能有 `<script>`。后端吐 `U+0002/U+0003`，
前端**先整体 HTML 转义、再**换成 `<mark>`；顺序反了就等于把历史会话里的任意 HTML
注进页面。给 MCP 与聊天 agent 的文本则走 `StripAll` 全部去掉——
实测本地模型会把 `\x02 \x03` 原样抄进给用户看的答案里。

### 8. 四套主题 = 同一组 CSS 变量的四组取值

`theme.css` 里 `[data-theme="desk"|"paper"|"editor"|"term"]` 各定义**同一组** token，
其余样式一律写 `var(--x)`。加一套皮不用碰任何布局代码；反过来，某套主题漏声明一个
token 会被 `TestThemesDefineSameTokens` 当场拦下，而不是在页面上表现为一处怪颜色。

明暗分两层：顶栏按钮切的是 **mode**（亮 / 暗 / 跟随系统，三态循环），设置页选的是
**pick**（亮时用哪套 / 暗时用哪套）。合并成一个「五选一」列表的话，「跟随系统」就
没处安放了。

防闪烁只能靠 `<head>` 里那段内联脚本：外链脚本要等 HTML 解析完才执行，那时备份
已经用默认色画过一帧。它是全站**唯一**允许的内联脚本，`test/e2e/theme_test.go` 断言
「有且仅有一段、在 head 内、样式表之后、body 之前」。

⚠️ 一个踩过的坑：作者样式表里的 `display` 会盖掉备份给 `[hidden]` 的 `display:none`，
于是 `el.hidden = true` 看着毫无反应。`.filters` 和 `.chat-form` 都中过招
（后者意味着「LLM 不可用时聊天入口置灰」实际没生效）。现在 layout.css 顶部有一条
`[hidden] { display: none !important; }` 兜底。

### 9. 配置元信息只声明一次，热生效靠「每次用时取」

`config.Fields()` 是 16 个配置项的唯一声明（key / label / help / kind / hot / min / max /
options），`GET /api/config` 把它下发给前端渲染整张表单——前端不写第二份字段清单，
新增配置项只改 `meta.go`。`TestFieldsCoverEveryConfigKey` 反向校验条数，漏一个就失败。

热生效的关键不在保存那一侧，而在**读取**那一侧：`config.Live` 持 `atomic.Pointer[Config]`，
`summary.Worker` 与 `chat.Agent` 必须每轮从它取值。启动时把 Model 拷成结构体字段的
那一刻，这个配置项就悄悄变成「需重启」了。真正需重启的四项（两个端口、`db_path`、
`scan.roots`）在元信息里标 `hot:false`，界面上打角标并给出重启命令，不假装已生效。

保存只写**与默认值不同的键**：文件里永远只有「你改过的东西」，将来调整默认值能自动
跟随，而不是被一份固化的旧默认值锁死。写入走 `.tmp → chmod 0600 → rename`。

### 10. 路由走查询串，不走路径

URL 是「在看什么」的唯一来源；`localStorage` 只留偏好（明暗、主题指派、左栏两态）。
早先把当前视图存在 localStorage 里，代价是链接分享不出去、后退键直接退出应用、
两个标签页互相覆盖。

选查询串（`?view=digest&id=17&seq=42`）而不是路径（`/session/17`）的理由是实测的：
dashboard 的根处理器是 `http.FileServer`，未知路径返回 **404**，要支持路径式路由
就得在服务端加 SPA 兜底。查询串则服务端一行不动。

两处易错，都有测试守着：

- **`switchView` 与 `mountView` 必须分开**。前者是「用户点了导航」会写历史，后者是
  「按状态渲染」不碰 URL。不分开的话，`popstate` 里调用会再 push 一次，与浏览器
  自己的历史打架。
- **回读是覆盖层不是视图**，所以 URL 里是「底层视图 + id/seq」。这样「关掉回读回哪」
  由 URL 自己表达，不用另存 back 状态。⚠️ 实测踩过：`apply` 里若先挂底层视图再开回读，
  两个异步 fetch 会互相覆盖——检索结果回来得晚就把回读内容顶掉了。所以 URL 带 `id` 时
  根本不挂底层视图的内容。

### 11. Markdown 必须配消毒，这是量出来的

会话内容是敌意输入：里面有抓过的网页、`cat` 过的文件、工具吐出的任意字节。
实测 `marked.parse()` 的裸输出：

```html
<h1>Hi</h1>
<script>alert(1)</script>
<img src=x onerror=alert(2)>
<p><a href="javascript:alert(3)">click</a></p>
<iframe src=//evil></iframe>
```

四种攻击全部原样透传。所以 DOMPurify 不是「加固」，是管线里**不可省的一环**：
`marked.parse → DOMPurify.sanitize → DOM`，URI 白名单收紧到 http/https/mailto。

选型也是量出来的（自己下下来量，不引用聚合站互相矛盾的数字）：marked 38 KB
（markdown-it 121 KB，插件生态这里用不上；snarkdown 2 KB 但不支持表格，而实测
3 482 个 assistant 块有表格）+ DOMPurify 28 KB，合计 69 KB，对比字体 512 KB 可忽略。

**ANSI 只做 SGR，自写 45 行**：实测 673 286 块里只有 1 274 块（0.19%）含转义序列，
这个比例不值得引终端模拟器，也不值得引一个 MPL-2.0 的依赖。其中 1 136 块是
`tool_result`（上色），**140 块是 `user`**——多是 Claude Code 自己的界面文本被一并
存下来，它们走 Markdown 路径，所以在 `CD.md` 入口把 ANSI **剥掉**而不是上色：
上色要先产出 HTML，HTML 再喂给 Markdown 解析器就乱套了。

⚠️ 渲染前后有两条同源纪律：**先转义、再加标记**。`escHit` 如此，`CD.ansi` 如此；
而给已渲染的 HTML 加会话号链接时反过来——**不能对 HTML 串做正则替换**（会打断标签），
必须遍历文本节点。

### 12. 工具调用：一张映射表 + 三种骨架

`tool_use` 占全部内容块的 **39.4%**（274k / 696k），是回读时看得最多的一类。
R3 之前它按序列化 JSON 原样显示——转义引号满地、命令和说明挤一行。

**为什么不是"每个工具一个渲染函数"**：前 15 个工具覆盖 88%，但尾巴很长（各种
MCP 工具，键名各异）。逐个写既写不完，新工具出现时还会留下空白。改为**声明每个
字段是什么角色**（primary / sub / ctx / diff / body / noise），渲染骨架只有三种：

| 骨架 | 覆盖 | 谁 |
|---|---|---|
| 命令型 | 52.1% | `Bash` / `exec_command` / `write_stdin` |
| 文件型 | 23.2% | `Read` / `Write` / `Edit` / `NotebookEdit` |
| 通用键值 | 其余 | 未在映射表里的一切工具 |

未知工具落通用键值表，**不会有"空白"这种状态**。

另有两类正文根本不是 JSON，各走一条分支：`apply_patch`（3.5%）是 patch 文本，
逐行前缀判断着色，不引 diff 库；`exec`（5.3%）是 JS 源码，走代码块，**不做语言
探测**——用"不是 JSON 对象"反推即可，猜语言只会猜错。

两处细节值得记：

- **`$` 提示符用 `::before` 画，不进 DOM 文本**。用真实字符写的话，选中复制会把
  提示符一起带走，粘出去不能执行——而"复制下来重跑"正是回读命令的主要用途。
- **命令的转义天然还原**：命令是从 JSON 字符串解出来的，`\"` 自动变回 `"`，
  不需要手工反转义（手工做反而容易做错）。

⚠️ **判断正文形态必须走 API，不能读裸库。** 最初直接读 SQLite 的 `body` 列，
得出"31% 非法 JSON"的结论，实为读到了 CJK 单字切分用的 `U+0001` 分隔符——
那是索引内部表示，API 下发前会 `Strip`。按 API 输出重测，真实比例是
**91.2% 合法 JSON**。这个坑会再犯，所以写在这里。

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
| 摘要吞吐 | 中位 **0.8 s**/会话（均值 2.0 s，p90 2.9 s，最慢 42.2 s）→ 全量 3176 个实际 **2 小时 13 分**跑完 |

「亚秒级检索」这条非功能需求达成，最坏情况仍有约 3 倍余量。

## 安全约束（不可放宽，逐条有代码与测试）

| 约束 | 实现 | 守住它的测试 |
|---|---|---|
| 只读会话文件 | 一律 `os.Open`（`O_RDONLY`） | E2E：跑完整流程后原始文件 size/mtime/内容逐字节相等 |
| 配置写入路径固定 | 只写 `~/.config/chatdex/config.json`，前端给不了任意路径 | `ConfigStore` 最小接口 + 保存单测（权限 0600、原子写、失败不动原文件） |
| 只监听 `127.0.0.1` | `const loopback`，地址不做成配置项 | 集成测试真的去本机局域网 IP 上连一次，连得上就失败 |
| 索引库 `0600` | `Open` 显式 chmod 主库与 `-wal`/`-shm` | 单测 + E2E 双重断言 |
| LLM 只允许回环 | `llm.ValidateEndpoint`，构造即失败，无开关；**保存路径同样过这一关** | 7 个远端/内网/通配端点的负例 + 设置页保存的 3 条负例 |
| 无向量索引（R8 门控） | 代码里无 embedding 表/列/方法 | `grep -rniE 'embedding\|vector'` 仅命中注释 |
