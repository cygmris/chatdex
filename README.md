# chatdex

**把 Claude Code 与 Codex 的全部历史会话变成可检索的东西。** 本地常驻，只读，不联网。

简体中文 | [English](README.en.md)

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![SQLite FTS5](https://img.shields.io/badge/SQLite-FTS5-003B57?logo=sqlite&logoColor=white)](https://sqlite.org/fts5.html)
[![MCP](https://img.shields.io/badge/MCP-endpoint-6E56CF)](https://modelcontextprotocol.io)

> 你和 AI 一起做的每个决定、踩过的每个坑、写对的每条命令，都躺在几千个 JSONL 文件里，
> 没有任何入口能找回来。chatdex 给它们一个入口——给你，也给 agent 自己。

![检索](docs/images/search.png)

---

## 它解决什么

`~/.claude/projects/` 和 `~/.codex/sessions/` 里堆着你全部的工作记录。`grep` 撑不住，因为：

- **中文搜不到。** SQLite FTS5 的 `unicode61` 把一整句中文当成一个 token，搜「限流」搜不出「请求限流」。
- **命中最多 ≠ 你要找的。** 真实语料实测：命中最多的两个会话（2272 / 2154 次）都不是目标，
  目标只命中 669 次——那两个只是在长日志里反复提到这个词。
- **答案往往在工具调用里。** 「上次那条命令怎么写的」不在对话正文，在 `tool_use` 的参数里。
- **你记的词和原文的词对不上。** 记忆里是「增量备份」，原文写的是「类似 TimeMachine 的管理工具」。

chatdex 对这四条各有对策，且都有实测数字撑着——见 [`docs/architecture.md`](docs/architecture.md)。

## 特性

| | |
|---|---|
| 🔍 **中英混合全文检索** | CJK 单字切分 + FTS5，中位延迟 **23 ms**（3176 会话 / 63 万块的真实语料） |
| 🧠 **摘要一并入索引** | 本地 LLM 给每个会话写一句话摘要，**用概念词重写原文**——这正是填平上面那条词汇鸿沟的手段 |
| 💬 **问一问** | 用大白话提问，LLM 多轮改写查询自己重试，并**把每一轮搜了什么都摊开给你看** |
| 🏷 **会话名** | 你 `/rename` 起的名字优先于 LLM 摘要显示——人写的比机器猜的可信 |
| 🕘 **时间线与会话回读** | 按项目聚合；点进去逐条回读原始对话，长会话分页 |
| 📝 **Markdown 与 ANSI 渲染** | assistant 输出按 Markdown 显示，命令输出的颜色码正确上色；回读页可一键切回**原文**看原始字节 |
| 🔗 **可分享的链接** | 视图、检索词、全部过滤条件、正在读的会话都在 URL 里——发给别人能还原同样的结果，后退键也照常用 |
| 🔌 **MCP 端点** | agent 可以自己查「我上次是怎么解决这个的」 |
| 🎨 **四套主题** | 亮 / 暗 / 跟随系统，四套配色的对比度全部由脚本验证达到 WCAG AA |
| ⚙️ **设置页** | 配置项在浏览器里改，多数即时生效 |
| 🔒 **只读 + 只本机** | 见下方[安全边界](#安全边界) |

## 快速开始

需要 Go 1.26+。**不需要** Node、构建链、Docker，或任何网络访问。

```bash
git clone https://github.com/cygmris/chatdex.git && cd chatdex
go build -o ~/.local/bin/chatdex ./cmd/chatdex

chatdex index      # 首次全量索引，3000 会话约 13 分钟
chatdex serve      # dashboard :5021 / API+MCP :5022
```

浏览器打开 <http://127.0.0.1:5021>。常驻运行：

```bash
cp deploy/systemd/chatdex.service ~/.config/systemd/user/
systemctl --user enable --now chatdex
```

完整部署与排查见 [`docs/deploy.md`](docs/deploy.md)。

### 可选：本地 LLM

摘要与「问一问」需要本地 [Ollama](https://ollama.com)。**它是可选依赖**——不装照样索引、照样检索，
只是少了问一问这个页面和条目上的摘要行。

```bash
ollama pull qwen2.5:7b-instruct
```

端点**只接受回环地址**，填远端会被直接拒绝，且没有任何开关可以放宽——原因见下。

### 接入 MCP

让 agent 自己查历史：

```json
{
  "mcpServers": {
    "chatdex": { "url": "http://127.0.0.1:5022/mcp" }
  }
}
```

提供三个工具：`search_sessions`、`get_session`、`list_projects`。

## 界面

> 所有截图用的都是**造出来的演示数据**——120 个虚构会话、4 个虚构项目，不是任何人的真实会话。

### 检索：摘要是主标题，片段是证据

每条结果以会话摘要打头，一眼能看出是不是你要的那个；下面的片段说明它**为什么**匹配。

![检索](docs/images/search.png)

### 筛到工具调用那一层

六项过滤：来源、内容类型、工具名、项目、起止日期。筛到工具调用，正是回答「上次那条命令怎么写的」
的用法——命中直接落在命令本身上。

![带过滤的检索](docs/images/search-filters.png)

### 摘要

翻或搜全部会话摘要。每条都标出由哪个模型、什么时候生成——摘要可信到什么程度，取决于这两件事。

![摘要](docs/images/digest.png)

### 问一问

用大白话提问，LLM 改写查询自己重试，**且每一轮都摊开显示**，你才知道它有没有搜对方向。
答案里的会话号可以直接点进去。

![问一问](docs/images/chat.png)

### 时间线

按项目聚合、按时间倒序——适合回答「那阵子我到底在干什么」。

![时间线](docs/images/timeline.png)

### 会话回读

逐条回读原始对话。assistant 的输出按 Markdown 渲染、命令输出的 ANSI 颜色正确上色，
**工具调用按结构显示**——命令就是命令（复制下来能直接跑）、文件编辑是前后对照、patch 是带色的 diff。左上角一键切「原文」看索引时存下的原始字节——这是取证工具，
看到原始字节有时才是重点。地址栏里带着会话号，刷新与分享都能回到同一处。

![会话回读](docs/images/reader.png)

### 设置

全部配置项由后端的**单一份**元信息渲染而来。需重启的项会明说；索引相关项会注明只影响之后新索引的内容。

![设置](docs/images/settings.png)

### 左栏可收起

收起成 46px 单字导航，需要把宽度还给正文时用。

![收起的左栏](docs/images/sidebar-mini.png)

## 安全边界

会话记录里有你 `cat` 过的配置、`env` 打过的变量、`curl` 带过的 token。**索引库是这些东西的集中副本。**
所以下面四条是硬约束，代码里没有开关，每条都有测试守着：

| 约束 | 怎么保证的 |
|---|---|
| **绝不写会话文件** | 一律 `os.Open`（`O_RDONLY`）。E2E 测试跑完整流程后，逐字节比对原始文件的 size / mtime / 内容 |
| **只监听 `127.0.0.1`** | 地址不做成配置项。集成测试真的去局域网 IP 上连一次，连得上就算失败 |
| **索引库 `0600`** | 主库与 `-wal` / `-shm` 都显式 chmod，目录 `0700` |
| **LLM 端点只允许回环** | 构造即失败，没有 `--allow-remote` 之类的逃生口。7 个远端 / 内网 / 通配地址的负例测试 |

配置文件同样是 `0600`，写入走 `.tmp → chmod → rename`，断电不会留下半个 JSON。

## 它**不是**备份

索引库存的是**派生文本**，不是原文副本。实测：原始会话 5.9 GB，入库正文 549 MB（约 9%）。
差额来自 JSONL 结构开销，以及这些刻意的取舍：

- 工具结果**按 4096 字节截断**（可配）——实测 65 万块里有 3.8 万块被截
- 图片、二进制、思考过程等**不入库**
- 每个会话首条消息里注入的 `CLAUDE.md` / `AGENTS.md` 全文**被剥离**

原始 `.jsonl` 才是唯一事实来源，每条记录都存了它的绝对路径与偏移量以便随时指回去。
**要备份请用备份工具。**

## 实测数据

真实语料、真实机器，不是合成基准：

| | |
|---|---|
| 会话 / 内容块 | 3 176 / 632 322 |
| 索引库 | 1.0 GB（全量索引 13 分 0 秒） |
| 检索延迟 | 中位 **23 ms**，20 条真实查询里 19 条 < 260 ms |
| 最慢查询 | 342 ms——单个 CJK 常用字，命中近十万块的退化情形 |
| 摘要吞吐 | 中位 0.8 s/会话，全量 3 176 个 **2 小时 13 分**跑完 |

## 两套 JSONL 格式不同（写解析器前必读）

| | Claude Code | Codex |
|---|---|---|
| 路径 | `~/.claude/projects/<slug>/<uuid>.jsonl` | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` |
| 角色字段 | `message.role` | `payload.role`（外层 `type: response_item`） |
| 文本字段 | `content` 为 str，或 list 中 `type=="text"` | list 中 `type=="input_text"` |
| 子代理 | 另存 `<uuid>/subagents/agent-*.jsonl` | 同文件内 |

解析器可插拔——在 `internal/parser` 里实现 `Parser` 接口即可，索引与检索都不用碰。

## 为什么没有向量语义检索

词汇鸿沟是真的：记忆里是「增量备份」，原文写的是「类似 TimeMachine 的管理工具」，关键词**零命中**。
但向量不是唯一解，也未必最优：

- **摘要是文本，且用概念词重写原文。** 若存有摘要「讨论基于 restic 做增量备份工具」，
  搜「增量备份」**关键词就命中了**。
- **agent 会多轮改写查询重试**，向量做不到：它只给一次相似度排名，不会因为结果不对就换个说法再搜。
- 进程内 embedding 的选型、模型体积、全量向量化耗时、混合排序调参，是全项目最贵的一块。

所以这件事被设成**门控**：先攒 10 个真实的「摘要 + agent 改写都没搜到」的案例做基准集。
这套设计若能解决 ≥8/10，该需求永久关闭；否则才重开，并以那个基准集验收。
代码里没有预埋任何 embedding 表或字段。

## 文档

- [`docs/architecture.md`](docs/architecture.md) —— 架构与九个关键决策**及它们的代价**，
  含 63.8 s → 276 ms 那次查询性能事故的完整归因
- [`docs/deploy.md`](docs/deploy.md) —— 部署、配置、排查
- [`docs/design-parity.md`](docs/design-parity.md) —— 界面与设计稿的逐条差异及原因

## 许可

代码 MIT，见 [`LICENSE`](LICENSE)。

内置字体为 [IBM Plex](https://github.com/IBM/plex)（Sans / Mono），SIL Open Font License 1.1，
许可全文在 `internal/dashboard/static/fonts/LICENSE.txt`。字体随二进制打包是刻意的——
页面不引用任何外部域名，既保证离线可用，也不向第三方泄露你的浏览行为。

渲染 Markdown 用到两个随二进制打包的库（同样不引用任何外部域名）：
[marked](https://github.com/markedjs/marked)（MIT）与
[DOMPurify](https://github.com/cure53/DOMPurify)（Apache-2.0 / MPL-2.0），
许可全文在 `internal/dashboard/static/vendor/`。
