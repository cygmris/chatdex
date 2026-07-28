# chatdex

统一索引并检索 **Claude Code** 与 **Codex** 全部会话记录的本地常驻服务。

> 决策、方案、踩坑结论大量沉淀在历史会话里，却没有任何检索入口 —— 写完即丢。
> chatdex 让人能找回上下文，也让 agent 通过 MCP 自查「我上次是怎么解决这个的」。

## 形态

照搬 [specloop](../specloop)：**Go 常驻单例 + `systemd --user` + MCP 端点 + 只读 Web dashboard**。

- 端口块 **5020-5029**（`5021` 前端 / `5022` API+MCP）—— 5010-5019 归 specloop
- 检索用 **SQLite FTS5 关键词**；**本期不做向量语义检索**（见下）
- **LLM agent 聊天**：在 dashboard 里直接问「我记得做过 X，忘了在哪个会话」，LLM 多轮调检索工具自己改写查询重试
- **会话摘要**：本地 LLM 为每个会话生成一句话摘要，**作为文本一并入 FTS5**
- 本地 LLM（Ollama）是**可选依赖**，端点仅允许 `127.0.0.1`；不可用时检索照常，只是聊天置灰、缺摘要
- 索引范围含 **assistant 输出与工具调用/结果**（「上次那条命令怎么写的」只在工具调用里）
- **只读**：不写、不改、不删任何会话原始文件
- **只监听 `127.0.0.1`，不可放宽** —— 工具结果里有 `cat`/`env`/`curl` 的明文密钥，索引库等于一份集中的凭证副本

## 状态

🟢 **已交付**（`session-search` 全部 22 个任务完成）。

```bash
systemctl --user start chatdex   # 部署见 docs/deploy.md
xdg-open http://127.0.0.1:5021
```

三个视图：**检索**（关键词 + 六项过滤）· **时间线**（按项目聚合）· **问一问**（本地 LLM 多轮检索）。
另有 MCP 端点 `http://127.0.0.1:5022/mcp` 供 agent 自查历史。

实测（本机真实语料）：

| | |
|---|---|
| 会话 / 内容块 | 3176 / 632 322 |
| 索引库 | 1.0 GB（全量索引 13 分钟） |
| 检索延迟 | 中位 23 ms（最慢 342 ms：单个 CJK 常用字这种退化查询） |
| 摘要 | 21.8 s/会话，全量约 19.3 小时（后台跑，可暂停续跑） |

架构与关键决策见 `docs/architecture.md`，部署与排查见 `docs/deploy.md`。

## 开发方式

specloop 驱动。规范在 `.spec-workflow/specs/session-search/`，审批模式 `auto-with-log`。

```
Requirements ✅ → Design ✅ → Tasks ✅ → Implementation ⏳
```

## ⚠️ 核心前置知识：两套 JSONL 格式不同

写解析器前必读 —— 这是踩出来的：

| | Claude Code | Codex |
|---|---|---|
| 路径 | `~/.claude/projects/<slug>/<uuid>.jsonl` | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` |
| 消息位置 | `message.role` | `payload.role`（外层 `type: response_item`）|
| 文本字段 | `content` 为 str，或 list 中 `type=="text"` | `content` list 中 `type=="input_text"` 的 `text` |
| 子代理 | 另存 `<uuid>/subagents/agent-*.jsonl` | 同文件内 |

两边共有的坑：**首条 user 消息是注入的 `CLAUDE.md` / `AGENTS.md` 全文**（数千字），不按长度过滤则每个会话都命中。

另一个坑：**别按关键词命中次数排序**。实测找某段对话时，命中最多的两个会话（2272 / 2154 次）都不是目标，目标只有 669 次 —— 要按「用户说了什么」匹配意图。

## 为什么本期不做向量语义检索

词汇鸿沟是真的：找一段对话时记忆里是「**增量备份**」，原文写的却是「类似 **timemachine** 的管理工具」，关键词搜索**零命中**。

但向量不是唯一解，也未必最优：

- **会话摘要是文本**，且会用概念词重写原文。上例若存有摘要「讨论基于 restic 做类似 TimeMachine 的**增量备份**项目」，搜「增量备份」**关键词就命中了** —— 纯文本手段填平了鸿沟
- **LLM agent 会多轮改写查询重试**，向量做不到（它只给一次相似度排名，不会因结果不对而换个说法再搜）
- 进程内 embedding 的选型、模型体积、全量向量化耗时、混合排序调参，是全项目最贵的一块

所以 R8 设为**门控**：等这套用起来，攒 10 个真实的「还是没搜到」案例做基准集，解决 ≥8/10 就永久关闭，否则才重开并以该基准集验收。

## 相关

- 参考实现：specloop（同作者的 spec 驱动开发服务）
- 参考实现：`~/projects/specloop`
