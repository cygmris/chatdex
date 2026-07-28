# chatdex

统一索引并检索 **Claude Code** 与 **Codex** 全部会话记录的本地常驻服务。

> 决策、方案、踩坑结论大量沉淀在历史会话里，却没有任何检索入口 —— 写完即丢。
> chatdex 让人能找回上下文，也让 agent 通过 MCP 自查「我上次是怎么解决这个的」。

## 形态

照搬 [specloop](../specloop)：**Go 常驻单例 + `systemd --user` + MCP 端点 + 只读 Web dashboard**。

- 端口块 **5020-5029**（`5021` 前端 / `5022` API+MCP）—— 5010-5019 归 specloop
- 索引用 SQLite FTS5，不引外部搜索服务
- **只读**：不写、不改、不删任何会话原始文件
- **只监听 `127.0.0.1`** —— 会话内容含凭证与私有 IP

## 状态

🔵 需求已定稿（`session-search`），design / tasks 待做。见 `docs/BACKLOG.md`。

## 开发方式

specloop 驱动。规范在 `.spec-workflow/specs/session-search/`，审批模式 `auto-with-log`。

```
Requirements ✅ → Design → Tasks → Implementation
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

## 相关

- 参考实现：specloop（同作者的 spec 驱动开发服务）
- 参考实现：`~/projects/specloop`
