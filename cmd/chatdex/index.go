package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cygmris/chatdex/internal/config"
	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/parser"
)

// openIndex 按配置打开索引库并组装扫描器。serve 与 index 两个子命令共用。
func openIndex(cfg config.Config) (*index.Store, *index.Scanner, error) {
	st, err := index.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	sc := &index.Scanner{
		Store: st,
		Reg: parser.NewRegistry(
			parser.Claude{Home: cfg.Home},
			parser.Codex{Home: cfg.Home},
		),
		Cfg: cfg.Index,
	}
	return st, sc, nil
}

func runIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	cfgPath := fs.String("config", config.Path(), "配置文件路径")
	quiet := fs.Bool("quiet", false, "不打进度")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	// 与 serve 争同一把「锁」：两个写者同时扫同一个库会读到同一个水位、
	// 解析同一段追加内容、各写一份块——事务只保证各自原子，不会互相察觉。
	// 端口绑定是 serve 已在用的单例机制，这里复用它，语义才一致。
	ln, err := listen(cfg.Ports.API)
	if err != nil {
		return fmt.Errorf("chatdex 服务正在运行（端口 %d 被占用）。"+
			"服务本身会增量索引；要手动重建请先 systemctl --user stop chatdex", cfg.Ports.API)
	}
	defer ln.Close()

	st, sc, err := openIndex(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("索引库 %s\n", cfg.DBPath)
	start := time.Now()
	if !*quiet {
		last := time.Now()
		sc.OnFile = func(path string, indexed int) {
			// 行内刷新，不刷屏
			if time.Since(last) < 200*time.Millisecond {
				return
			}
			last = time.Now()
			fmt.Fprintf(os.Stderr, "\r已索引 %d 个文件  %s", indexed, trimPath(path))
		}
	}

	rep, err := sc.ScanOnce()
	if !*quiet {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil {
		return err
	}

	fmt.Printf("扫描 %d 个文件，索引 %d 个，新增 %d 块，跳过坏行 %d，重建 %d，失效 %d\n",
		rep.FilesSeen, rep.FilesIndexed, rep.BlocksAdded, rep.LinesSkipped, rep.Rebuilt, rep.MarkedDead)
	fmt.Printf("索引库体积 %s，耗时 %s\n", humanBytes(rep.DBBytes), time.Since(start).Round(time.Second))
	if rep.SizeCapped {
		fmt.Println("⚠️ 索引库已达配置的体积上限，已停止索引新增内容（未删除任何数据）")
	}

	stats, err := st.Stats()
	if err != nil {
		return err
	}
	fmt.Printf("会话 %d（失效 %d），内容块 %d，其中截断 %d，已有摘要 %d\n",
		stats.Sessions, stats.DeadSessions, stats.Blocks, stats.TruncatedBlocks, stats.Summarized)
	for _, k := range []string{"user", "assistant", "tool_use", "tool_result", "summary"} {
		if n := stats.BlocksByKind[k]; n > 0 {
			fmt.Printf("  %-12s %d\n", k, n)
		}
	}
	return nil
}

func trimPath(p string) string {
	if len(p) <= 70 {
		return p
	}
	return "…" + p[len(p)-69:]
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
