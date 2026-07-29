# chatdex 部署与运维

常驻服务，`systemd --user` 托管。**只监听 `127.0.0.1`，只读会话文件。**

## 路径一览

| 用途 | 路径 |
|---|---|
| 二进制 | `~/.local/bin/chatdex` |
| systemd unit | `~/.config/systemd/user/chatdex.service`（源文件在仓库 `deploy/systemd/`） |
| 索引库 | `~/.local/share/chatdex/index.db`（`0600`，目录 `0700`） |
| 配置（可选） | `~/.config/chatdex/config.json`（缺文件即用默认值） |
| 会话来源（只读） | `~/.claude/projects/`、`~/.codex/sessions/` |
| dashboard | http://127.0.0.1:5021 |
| API + MCP | http://127.0.0.1:5022 |

## 安装

```bash
cd ~/projects/chatdex
go build -o ~/.local/bin/chatdex ./cmd/chatdex
cp deploy/systemd/chatdex.service ~/.config/systemd/user/chatdex.service
systemctl --user daemon-reload
systemctl --user enable --now chatdex.service
```

验证：

```bash
systemctl --user is-active chatdex          # active
curl -s http://127.0.0.1:5022/api/stats     # 索引统计
xdg-open http://127.0.0.1:5021              # dashboard
```

## 升级

```bash
go build -o ~/.local/bin/chatdex ./cmd/chatdex && systemctl --user restart chatdex
```

重启会复用已有索引，只补停机期间的增量（日志里能看到 `增量索引完成 files=N`，
N 是个位数而不是三千多）。

## 日常命令

```bash
systemctl --user {start,stop,restart,status} chatdex
journalctl --user -u chatdex -f            # 跟日志
chatdex status                             # 只读打印索引统计
chatdex index                              # 手动跑一轮索引（服务在跑时会被拒绝，见下）
```

崩溃自动拉起：unit 配了 `Restart=always` / `RestartSec=2`，`kill -9` 后约 2 秒起新进程。

## 配置

有两种改法，改的是同一个文件：

- **设置页**（dashboard → 设置）：按字段渲染，带说明与取值范围，保存后多数项立即生效。
  需重启的四项（两个端口、`db_path`、`scan.roots`）会打「需重启」角标并给出命令；
  `index.*` 三项会注明「对历史内容需 `chatdex index` 重建才生效」。
- **手改文件**：格式如下。两种改法都只把**与默认值不同的键**写进文件，
  所以配置文件可以整个不存在，也不会被塞满一堆默认值。


```json
{
  "index":   { "tool_result_cap": 4096, "tool_result_body": true, "max_bytes": 12000000000 },
  "scan":    { "interval_sec": 30 },
  "summary": { "enabled": true, "throttle_ms": 500, "model": "qwen2.5:7b-instruct" },
  "llm":     { "endpoint": "http://127.0.0.1:11434" },
  "chat":    { "model": "qwen2.5:7b-instruct", "max_tool_rounds": 8 },
  "ports":   { "ui": 5021, "api": 5022 }
}
```

几个要点：

- `llm.endpoint` **只接受回环地址**。填远端会在启动时被拒绝（服务照常起，只是没有 LLM 功能）——
  会话内容含明文凭证，这条不留可配的口子。
- `index.tool_result_body: false` 会让工具结果只留元数据（工具名/时间/体积），
  索引库能小一半左右。改完要 `chatdex index` 重跑才对历史生效。
- 监听地址不可配，永远是 `127.0.0.1`，只有端口号能改。
- 配置文件权限 `0600`，写入走 `.tmp → chmod → rename`：断电不会留下半个 JSON
  （半个 JSON 会让服务下次启动直接失败）。
- 设置页里填非回环端点会被当场拒绝并指出是哪一格——界面上能填不等于能存。

外观相关的两项（`ui.light_theme` / `ui.dark_theme`）决定顶栏切到「亮」/「暗」时各用哪套主题，
可选 `desk` / `paper` / `editor` / `term`。填了不存在的主题名会回退到默认并记 warning，不白屏。

## 实测数据（2026-07-29，本机全量）

| 指标 | 值 |
|---|---|
| 会话文件 | 3176（Claude 3013 含子代理 / Codex 163） |
| 内容块 | 632 322 |
| 入库正文 | 0.53 GB |
| 索引库 | 1.0 GB |
| 全量索引耗时 | 13 分 0 秒 |

## 排查

**服务起不来** —— 先看端口是否被占：

```bash
ss -tlnp | grep -E '502[12]'
journalctl --user -u chatdex -n 30 --no-pager
```

chatdex 是单例：第二个实例抢不到端口就会打印「chatdex 已在运行」并退出，
且**在碰索引库之前**退出，不会写坏索引。

`chatdex index` 争的是同一把锁：服务在跑时执行它会被拒绝并提示先 `systemctl --user stop chatdex`。
这不是洁癖——两个写者会读到同一个水位、解析同一段追加内容、各写一份块，
事务只保证各自原子、不会互相察觉，结果是同一 `seq` 出现重复块。
日常不需要手动索引：服务自己每 30 秒增量一次。

**索引里少了内容** —— 日志里搜 warning：

```bash
journalctl --user -u chatdex | grep WARN | sort | uniq -c | sort -rn | head
```

`未知记录类型 / 未知 payload 类型` 说明上游工具加了新的记录格式，需要在
`internal/parser/{claude,codex}.go` 里补一条分支（是内容就索引，是元数据就加进忽略表）。

**索引库太大** —— `chatdex status` 看体积；超过 `index.max_bytes` 时服务会停止索引新增内容
并在日志告警，但**绝不自动删除历史数据**。压缩手段按代价从低到高：
关 `tool_result_body` → 调低 `tool_result_cap` → 提高 `max_bytes`。
