# chatdex 推广稿（草稿）

> 2026-08-29 重写。上一版路径 `~/chatdex-promo-drafts.md` 已不存在，
> 且那是 R6 时代的内容（现已到 R22）。**下面所有数字都是当天在本机实测的**，
> 发之前若隔了较久请重新量一遍——这份稿子自己就该守「别拿旧数当现状」那条。

## 核实过的事实（发稿前复查用）

| 项 | 值 | 怎么来的 |
|---|---|---|
| 索引规模 | 4,236 个会话 / 1,242,898 个内容块 | 本机 sqlite 实测 |
| 检索延迟 | 27–113 ms | `/api/search` 连打三次 |
| 运行时依赖 | 2 个（`go-sdk`、`modernc.org/sqlite`） | `go.mod` |
| 代码量 | 7,518 行实现 / 8,762 行测试 | `wc -l` |
| 架构决策记录 | 39 条 | `docs/architecture.md` |
| 变异用例 | 87 条，全咬 | `test/mutation/run.py` |

⚠️ **别写「23ms」**——那是 R6 时代的数，LobeHub 收录页抓的就是那一版，已经不准了。

---

## 版本 A · 中文长帖（V2EX / 少数派 / 掘金）

**标题**：我把两年的 Claude Code 和 Codex 会话做成了可检索的本地索引

正文：

用 AI 编码助手久了会遇到一件事：**你知道那个问题解决过，但找不到是哪次会话。**
决策、踩过的坑、最后为什么选了方案 B——全沉在 JSONL 里，没有入口。

chatdex 是一个本地常驻服务，把 Claude Code 与 Codex 的全部会话统一索引起来：

- **全文检索**，中英文都行（CJK 走单字切分 + 短语查询，「备份」能命中「增量备份」
  内部，不会命中散在各处的两个字）
- **按项目回溯的时间线**，可以回答「那阵子我到底在干什么」
- **本地 LLM 生成摘要**，以及一个「问一问」——它调检索工具去翻你的会话，
  并把**它实际搜了什么**展示出来（不展示的话你无从判断它有没有搜对方向）
- **对接 restic 做备份**，但 chatdex 不做 restic 的壳子——它做 restic 做不到的那部分：
  「我索引过的会话，备份里到底有没有」

几个可能有意思的点：

**只读是硬约束。** 绝不写/改/删任何会话原始文件，有测试守着。
LLM 端点只允许 `127.0.0.1`（本地 Ollama），不留任何远端 API 的逃生口。

**依赖只有两个**，SQLite 用的是纯 Go 实现（无 CGO），所以能交叉编译到四个平台。

**测试比实现多**（8,762 vs 7,518 行），另有一套变异扫描：87 条变异，
逐条给断言注入一个它本该拦住的改动，看它咬不咬。**全咬**。
这套东西是被逼出来的——项目里反复出现同一类 bug：**失败没有信号**。

举三个真实的例子：

- 覆盖率页报「已覆盖 3082」，而它依据的快照停在一周前。数字算术上完全正确，
  只是没交代它是拿哪一刻的快照算的。
- 时间线上选筛选条件，下拉变了、URL 变了、列表重载了——**结果一条没变**，
  因为后端从来没读过那三个参数。四个正反馈齐全，而它们全是假的。
- 覆盖率把 1,973 个会话报成「永久丢失」。抽样查证：**86% 其实在旧快照里躺着**，
  只是判定只跟最新快照比，而消失的文件按定义不在最新快照里。

这些的共同点是**缺席的东西不会在输出里留下痕迹**——没跑的测试、没被调用的函数、
没被使用的参数、没查完的范围，在结果上都与「跑了/调了/用了但恰好无事发生」一模一样。
仓库里有 39 条架构决策记录，一半在讲这件事怎么反复咬人。

GitHub：<https://github.com/cygmris/chatdex>

---

## 版本 B · 英文短帖（Hacker News Show HN）

**Title**: Show HN: chatdex – local full-text search over your Claude Code and Codex sessions

I kept hitting the same problem: I *knew* I'd solved something before, but couldn't
find which session. All the decisions and dead ends live in JSONL files with no way in.

chatdex indexes Claude Code and Codex transcripts into a local, read-only service:
full-text search (CJK-aware), a per-project timeline, local-LLM summaries, and an
"ask" mode that shows you exactly which searches it ran.

Design constraints that shaped it:

- **Read-only, enforced by tests.** It never writes to your transcripts.
- **No remote LLM.** The endpoint is restricted to `127.0.0.1` — no escape hatch.
- **Two runtime dependencies**, pure-Go SQLite, so it cross-compiles to four platforms.
- **More test code than implementation** (8.7k vs 7.5k lines), plus a mutation
  harness: 87 mutations, each injecting a change some assertion *should* catch.
  All 87 caught.

The mutation harness exists because one bug class kept recurring: **failures with
no signal.** A coverage page reporting "3082 covered" from a week-old snapshot.
Filters that update the dropdown, the URL, and reload the list — while the backend
never reads them. A counter reporting 1,973 sessions "permanently lost" when 86%
of them were sitting in older snapshots.

They share a shape: **absence leaves no trace in the output.** A skipped test, an
uncalled function, an unused parameter, an incomplete search — each is
indistinguishable from "ran, called, used, and nothing happened."

<https://github.com/cygmris/chatdex>

---

## 发之前

- [ ] 数字重量一遍（这份稿子已经因为「用旧数」被返工过一次）
- [ ] 截图是不是当前界面（R18 之后加了时间线分页、R22 加了「已截断」可点）
- [ ] V2EX / HN 的自我推广版规各自确认
