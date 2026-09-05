# chatdex 部署与运维

常驻服务，**只监听 `127.0.0.1`，只读会话文件。**

Linux 用 `systemd --user`，macOS 用用户级 `launchd`（`~/Library/LaunchAgents/`）。
两边都不需要 root / sudo / system daemon。

## 路径一览

| 用途 | 路径 |
|---|---|
| 二进制 | `~/.local/bin/chatdex` |
| Linux systemd unit | `~/.config/systemd/user/chatdex.service`（源文件在仓库 `deploy/systemd/`） |
| macOS launchd plist | `~/Library/LaunchAgents/dev.cygmris.chatdex.plist`（模板在 `deploy/launchd/`） |
| 索引库 | `~/.local/share/chatdex/index.db`（`0600`，目录 `0700`） |
| 配置（可选） | `~/.config/chatdex/config.json`（缺文件即用默认值，`0600`，目录 `0700`） |
| macOS 日志 | `~/Library/Logs/chatdex/stdout.log`、`stderr.log`（目录须事先创建，launchd 不会建父目录） |
| 会话来源（只读） | `~/.claude/projects/`、`~/.codex/sessions/`、`~/.grok/sessions/` |
| dashboard | http://127.0.0.1:5021 |
| API + MCP | http://127.0.0.1:5022 |

chatdex **只读**原始会话文件，不会写入、改名或删除它们。卸载服务也不会动会话源文件、索引库或配置。

## Linux systemd 与 macOS launchd 对照

| | Linux | macOS |
|---|---|---|
| 用户级服务 | `systemd --user` | `launchd` LaunchAgent |
| 单元文件 | `~/.config/systemd/user/chatdex.service` | `~/Library/LaunchAgents/dev.cygmris.chatdex.plist` |
| 安装 / 启用 | `systemctl --user enable --now chatdex` | `scripts/macos-install.sh` |
| 启动 / 停止 / 重启 | `systemctl --user start\|stop\|restart chatdex` | `scripts/macos-service.sh start\|stop\|restart` |
| 状态 | `systemctl --user status chatdex` | `scripts/macos-service.sh status` |
| 日志 | `journalctl --user -u chatdex -f` | `tail -f ~/Library/Logs/chatdex/stderr.log` |
| 打开 dashboard | `xdg-open http://127.0.0.1:5021` | `open http://127.0.0.1:5021` |
| 看端口 | `ss -tlnp \| grep -E '502[12]'` | `lsof -nP -iTCP:5021 -sTCP:LISTEN` |
| 健康检查 | `curl http://127.0.0.1:5022/api/health` | 同左（脚本也会打这个，不只看 launchctl loaded） |

macOS **没有** `systemctl`、`journalctl`、`xdg-open`、`ss`。plist 里的路径必须是绝对路径：launchd **不展开 `~`**。

launchd 默认 `PATH` 只有 `/usr/bin:/bin:/usr/sbin:/sbin`。本仓库的 plist 会显式设成包含 `/opt/homebrew/bin` 与 `/usr/local/bin`，但 `restic` / `rclone` 仍建议在配置里填**绝对路径**（`backup.restic_path` / `backup.mirror.rclone_path`），不要依赖 PATH。

崩溃自动拉起：Linux unit 是 `Restart=always` / `RestartSec=2`；macOS plist 是 `KeepAlive=true` / `ThrottleInterval=2`。

## 源码构建

需 Go 1.26+。SQLite 用 `modernc.org/sqlite`（纯 Go），交叉编译关掉 CGO：

```bash
cd <仓库目录>
CGO_ENABLED=0 go build -o ~/.local/bin/chatdex ./cmd/chatdex
```

macOS arm64（可在 Linux 上交叉）：

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o chatdex-darwin-arm64 ./cmd/chatdex
```

注入版本（安装脚本会带上）：

```bash
go build -ldflags "-X github.com/cygmris/chatdex/internal/version.Version=0.1.0 -X github.com/cygmris/chatdex/internal/version.Commit=$(git rev-parse --short HEAD)" -o ~/.local/bin/chatdex ./cmd/chatdex
```

也可以下 [Releases](https://github.com/cygmris/chatdex/releases/latest) 的预编译包（linux / macOS × amd64 / arm64）。

## 首次安装

### Linux

```bash
install -m755 chatdex ~/.local/bin/chatdex   # 或自己 go build
cp deploy/systemd/chatdex.service ~/.config/systemd/user/chatdex.service
systemctl --user daemon-reload
systemctl --user enable --now chatdex.service
curl -s http://127.0.0.1:5022/api/health
xdg-open http://127.0.0.1:5021
```

### macOS

```bash
./scripts/macos-install.sh
# 或已有二进制：
#   CHATDEX_PREBUILT=/path/to/chatdex ./scripts/macos-service.sh install
./scripts/macos-service.sh status
open http://127.0.0.1:5021
```

安装脚本会：创建 `~/.local/bin`、`~/.config/chatdex`、`~/.local/share/chatdex`、`~/Library/Logs/chatdex`、`~/Library/LaunchAgents`；**保留**已有 `config.json` 与 `index.db`；plist 里 `ProgramArguments` 显式传 `--config` 的绝对路径；`RunAtLoad=true`；日志目录先建再 bootstrap。默认 label 是 `dev.cygmris.chatdex`；机器上已有别的 label 时用 `CHATDEX_LABEL=` 覆盖。

## 启动、停止、升级、回滚、卸载

### Linux

```bash
systemctl --user {start,stop,restart,status} chatdex
journalctl --user -u chatdex -f

# 升级（自己构建）
go build -o ~/.local/bin/chatdex ./cmd/chatdex && systemctl --user restart chatdex

# 升级（预编译包：先停，否则 cp 会 Text file busy）
systemctl --user stop chatdex
install -m755 chatdex ~/.local/bin/chatdex
systemctl --user start chatdex
```

### macOS

```bash
./scripts/macos-service.sh start
./scripts/macos-service.sh stop
./scripts/macos-service.sh restart
./scripts/macos-service.sh status     # launchctl + 实际请求 /api/health
./scripts/macos-service.sh upgrade    # 构建到 staging → file/shasum 校验 → 停服务 → 原子替换 → 健康检查
./scripts/macos-service.sh uninstall  # 只删 plist 和二进制
```

`upgrade` 失败会把上一个二进制拷回去再拉起。它**不会**删除用户会话源文件、索引库或配置。服务停止后再升级，脚本仍会重新 bootstrap，索引数据保留。

查看日志：

```bash
tail -f ~/Library/Logs/chatdex/stderr.log
```

## 日常命令

```bash
chatdex version            # 构建时注入的版本 / commit
chatdex doctor             # 配置、扫描根、索引库、HTTP /api/health
chatdex status             # 只读打印索引统计（含版本）
chatdex index              # 手动跑一轮索引（服务在跑时会被拒绝，见下）
```

`GET /api/health` 返回服务状态、版本、UI/API 端口、索引是否可打开。部署脚本必须打这个 URL，不能只看 `launchctl` loaded。

## 配置

有两种改法，改的是同一个文件：

- **设置页**（dashboard → 设置）：按字段渲染，带说明与取值范围，保存后多数项立即生效。
  需重启的项（两个端口、`db_path`、`home`、`scan.roots`）会打「需重启」角标；
  `index.*` 三项会注明「对历史内容需 `chatdex index` 重建才生效」。
- **手改文件**：格式如下。两种改法都只把**与默认值不同的键**写进文件。

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

- `llm.endpoint` **只接受回环地址**。填远端会在启动时被拒绝（服务照常起，只是没有 LLM 功能）。
- 监听地址不可配，永远是 `127.0.0.1`，只有端口号能改。
- 配置文件权限 `0600`，写入走 `.tmp → chmod → rename`。
- `restic` / `rclone` 在 macOS launchd 下请填绝对路径，见上文 PATH 说明。

### 索引范围（`scan.roots`）

空（默认）= 扫各解析器的会话目录：`~/.claude/projects`、`~/.codex/sessions`、`~/.grok/sessions`。

非空 = **只扫列出的真实目录**，不在范围内的已索引会话会被标为失效。每条必须是绝对路径，并且仍须落在解析器认识的树下（`home` 决定前缀，例如 `~/.claude/projects/<项目>`），否则文件会被扫到但不索引。

**目录符号链接不是缩小范围的办法。** 扫描走 `filepath.WalkDir`，它不跟进目录 symlink——根是 symlink、或树里出现目录 symlink，目标里的会话都不会被索引。不要用「在假 HOME 里 ln -s 几个项目」来限制范围；要限制就配 `scan.roots` 为真实目录。失效的 symlink 会被跳过，不会让整轮扫描失败。

改 `scan.roots` 需重启服务。

## 权限

| 对象 | 权限 | 说明 |
|---|---|---|
| 原始会话文件 | 只读打开 | chatdex 绝不写入、改名、删除 |
| 索引库目录 | `0700` | 索引含工具结果里的明文片段，等于一份凭证副本 |
| `index.db` 与 `-wal`/`-shm` | `0600` | 同上 |
| `config.json` | `0600`，目录 `0700` | 含本机路径；密码只以文件路径形式出现，不进这份 JSON |
| 日志目录 | `0700` | 日志可能带会话文件路径，不要世界可读 |

## 实测数据（2026-07-29，本机全量）

| 指标 | 值 |
|---|---|
| 会话文件 | 3176（Claude 3013 含子代理 / Codex 163） |
| 内容块 | 632 322 |
| 入库正文 | 0.53 GB |
| 索引库 | 1.0 GB |
| 全量索引耗时 | 13 分 0 秒 |

## 排查

**服务起不来** —— 先看端口是否被占，再看健康检查：

Linux：

```bash
ss -tlnp | grep -E '502[12]'
journalctl --user -u chatdex -n 30 --no-pager
curl -s http://127.0.0.1:5022/api/health
chatdex doctor
```

macOS：

```bash
lsof -nP -iTCP:5021 -sTCP:LISTEN
lsof -nP -iTCP:5022 -sTCP:LISTEN
tail -50 ~/Library/Logs/chatdex/stderr.log
curl -s http://127.0.0.1:5022/api/health
chatdex doctor
```

chatdex 是单例：第二个实例抢不到端口就会打印「chatdex 已在运行」并退出，且**在碰索引库之前**退出，不会写坏索引。

`chatdex index` 争的是同一把锁：服务在跑时执行它会被拒绝。这不是洁癖——两个写者会读到同一个水位、解析同一段追加内容、各写一份块，事务只保证各自原子、不会互相察觉，结果是同一 `seq` 出现重复块。日常不需要手动索引：服务自己每 30 秒增量一次。

**索引里少了内容** —— 日志里搜 warning。`未知记录类型 / 未知 payload 类型` 说明上游工具加了新的记录格式，需要在 `internal/parser/{claude,codex,grok}.go` 里补一条分支。

若只索引到部分项目：先确认没有把目录 symlink 当成扫描根（`chatdex doctor` 会标出来），再看 `scan.roots` 是否过窄。

**索引库太大** —— `chatdex status` 看体积；超过 `index.max_bytes` 时服务会停止索引新增内容并在日志告警，但**绝不自动删除历史数据**。压缩手段按代价从低到高：关 `tool_result_body` → 调低 `tool_result_cap` → 提高 `max_bytes`。
