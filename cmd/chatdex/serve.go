package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/cygmris/chatdex/internal/backup"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/cygmris/chatdex/internal/chat"
	"github.com/cygmris/chatdex/internal/config"
	"github.com/cygmris/chatdex/internal/dashboard"
	"github.com/cygmris/chatdex/internal/httpapi"
	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/llm"
	"github.com/cygmris/chatdex/internal/mcpserver"
	"github.com/cygmris/chatdex/internal/search"
	"github.com/cygmris/chatdex/internal/summary"
)

// loopback 是唯一允许的监听地址。
//
// 索引库含工具结果里 cat/env/curl 的明文密钥，等于一份集中的凭证副本——
// 只监听 127.0.0.1 是不可放宽的约束，因此地址不做成配置项，只有端口号可配。
const loopback = "127.0.0.1"

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", config.Path(), "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	// 单例：先抢端口，抢不到就退出，**在碰索引库之前**——
	// 双开会把索引写坏，而写坏的索引不是重启能修的。
	apiLn, err := listen(cfg.Ports.API)
	if err != nil {
		return fmt.Errorf("chatdex 已在运行（或端口 %d 被占用）: %w", cfg.Ports.API, err)
	}
	uiLn, err := listen(cfg.Ports.UI)
	if err != nil {
		apiLn.Close()
		return fmt.Errorf("端口 %d 被占用: %w", cfg.Ports.UI, err)
	}

	st, sc, err := openIndex(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	engine := search.NewEngine(st.DB())
	api := &httpapi.Server{Engine: engine, Store: st, Reg: sc.Reg}

	// LLM 是可选依赖：配不上或没起来，索引与检索照常，只是没有摘要、聊天置灰。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := config.NewLive(cfg)
	// restic 是可选依赖：Runner 总是构造，可用性由它自己在运行时探测——
	// 与本地 LLM 同一模式，装没装都不影响服务能不能起来。
	bk := &backup.Runner{Cfg: func() backup.Config {
		c := live.Get().Backup
		srcs := make([]backup.Source, 0, len(c.Sources))
		for _, s := range c.Sources {
			srcs = append(srcs, backup.Source{Path: s.Path, Enabled: s.Enabled})
		}
		return backup.Config{
			Repo: c.Repo, PasswordFile: c.PasswordFile,
			ResticPath: c.ResticPath, Sources: srcs,
		}
	}}
	api.Backup = bk
	api.Config = &configStore{
		live: live, path: *cfgPath, newLLM: llm.NewOllama,
	}

	client, llmErr := llm.NewOllama(cfg.LLM.Endpoint)
	if llmErr != nil {
		// 端点非回环会走到这里：服务照常起，只是没有 LLM 功能
		slog.Warn("LLM 端点不可用，摘要与聊天功能关闭", "err", llmErr)
		api.ChatUnavailableReason = llmErr.Error()
	} else {
		api.Chat = &chat.Agent{
			LLM:   client,
			Tools: &mcpserver.Tools{Engine: engine},
			Projects: func() []search.ProjectStat {
				ps, err := engine.Projects()
				if err != nil {
					return nil
				}
				return ps
			},
			Live: func() chat.Settings {
				c := live.Get()
				return chat.Settings{
					Model: c.Chat.Model, MaxRounds: c.Chat.MaxToolRounds,
					NumCtx: c.LLM.NumCtx,
				}
			},
		}
		if w := startSummary(ctx, live, st, engine, client); w != nil {
			api.Summary = w
		}
	}

	// 两个 listener 共用同一个 mux：页面与 API 同源，无需 CORS
	mux := http.NewServeMux()
	api.Register(mux)
	mcpserver.Register(mux, engine)
	dashboard.Register(mux)

	go scanLoop(sc, live, bk)

	// 会话名回填：只跑一次，异步——它要扫全部源文件（实测 3.1 GB / 17 秒），
	// 放同步路径上会让服务启动肉眼可见地卡一下，而检索本可以立刻用。
	go func() {
		if n, err := st.BackfillTitles(); err != nil {
			slog.Warn("会话名回填失败", "err", err)
		} else if n > 0 {
			slog.Info("会话名已回填", "会话数", n)
		}
	}()

	errc := make(chan error, 2)
	serve := func(ln net.Listener, name string) {
		slog.Info("监听中", "who", name, "addr", ln.Addr().String())
		errc <- http.Serve(ln, mux)
	}
	go serve(apiLn, "API+MCP")
	go serve(uiLn, "dashboard")

	fmt.Printf("chatdex 已启动\n  dashboard  http://%s:%d\n  API+MCP    http://%s:%d\n",
		loopback, cfg.Ports.UI, loopback, cfg.Ports.API)
	return <-errc
}

func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("%s:%d", loopback, port))
}

// startSummary 起摘要后台任务；配置里关掉则返回 nil。
// startSummary 起摘要后台任务。模型/限速/启用开关都从 live 每轮重读，
// 所以设置页改完立刻生效，不需要重启。
func startSummary(ctx context.Context, live *config.Live, st *index.Store, engine *search.Engine, client llm.Client) *summary.Worker {
	w := &summary.Worker{
		Store: st, Engine: engine, LLM: client,
		Live: func() summary.Settings {
			c := live.Get()
			return summary.Settings{
				Model: c.Summary.Model, ThrottleMS: c.Summary.ThrottleMS,
				Enabled: c.Summary.Enabled, NumCtx: c.LLM.NumCtx,
				Window: c.Summary.Window,
			}
		},
	}
	go w.Run(ctx)
	cfg := live.Get()
	slog.Info("摘要任务已启动", "model", cfg.Summary.Model, "throttle_ms", cfg.Summary.ThrottleMS)
	return w
}

// autoBackupGap 是「扫描后顺手备一次」两次之间的最小间隔。
//
// 扫描每 30 秒一轮，不限流的话活跃的一天能造出几百个快照——restic 靠去重
// 不会白占空间，但快照列表会变得没法看。半小时够密了：真丢文件的场景是
// 日常清理，不是秒级的。
const autoBackupGap = 30 * time.Minute

// autoBackupTimeout 是单次自动备份的上界，**必须小于 autoBackupGap**——
// 这样两次自动备份不可能重叠，省掉一套并发控制。
//
// 上界是必需的而不是保险：restic 撞上过期的仓库锁会长时间重试，而这条
// 路径原本跑在扫描循环里，一卡就是索引整个停下（需求 5.5 明确禁止）。
// 实测 4.22 GB 全量 28 秒，20 分钟有 40 倍余量。
const autoBackupTimeout = 20 * time.Minute

// scanLoop 后台增量扫描，保持索引最新（需求 5.2）。
//
// bk 可为 nil（备份是可选依赖）。
func scanLoop(sc *index.Scanner, live *config.Live, bk *backup.Runner) {
	var lastBackup time.Time
	for {
		c := live.Get()
		// 截断阈值与开关也热生效（只影响之后新索引的内容，界面上已标注）
		sc.Cfg = c.Index
		intervalSec := c.Scan.IntervalSec
		if intervalSec <= 0 {
			intervalSec = 30
		}
		rep, err := sc.ScanOnce()
		if err != nil {
			slog.Error("扫描失败", "err", err)
		} else if rep.FilesIndexed > 0 || rep.MarkedDead > 0 {
			slog.Info("增量索引完成", "files", rep.FilesIndexed, "blocks", rep.BlocksAdded,
				"rebuilt", rep.Rebuilt, "dead", rep.MarkedDead)
			// 只在真索引到东西之后才备：没有新内容时备一次纯属白跑。
			// 备份失败只记日志——它绝不能影响索引与检索（需求 5.5）。
			if bk != nil && c.Backup.AfterScan && time.Since(lastBackup) >= autoBackupGap {
				lastBackup = time.Now()
				// 异步 + 有上界：备份绝不能挡住下一轮扫描（需求 5.5）
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), autoBackupTimeout)
					defer cancel()
					res, err := bk.Backup(ctx)
					// 结果记进 Runner，由 /api/backup/status 带到界面上——
					// 只 slog 的话失败就只有 journalctl 里看得见（需求 5.3）
					bk.RecordAuto(res, err)
					if err != nil {
						slog.Warn("扫描后自动备份失败", "err", err)
					} else {
						slog.Info("扫描后自动备份完成", "snapshot", res.SnapshotID,
							"new", res.FilesNew, "bytes", res.BytesAdded)
					}
				}()
			}
		}
		time.Sleep(time.Duration(intervalSec) * time.Second)
	}
}
