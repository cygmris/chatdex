package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/cygmris/chatdex/internal/config"
	"github.com/cygmris/chatdex/internal/dashboard"
	"github.com/cygmris/chatdex/internal/httpapi"
	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/search"
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
	api := &httpapi.Server{Engine: engine, Store: st}

	// 两个 listener 共用同一个 mux：页面与 API 同源，无需 CORS
	mux := http.NewServeMux()
	api.Register(mux)
	dashboard.Register(mux)

	go scanLoop(sc, cfg.Scan.IntervalSec)

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

// scanLoop 后台增量扫描，保持索引最新（需求 5.2）。
func scanLoop(sc *index.Scanner, intervalSec int) {
	if intervalSec <= 0 {
		intervalSec = 30
	}
	for {
		rep, err := sc.ScanOnce()
		if err != nil {
			slog.Error("扫描失败", "err", err)
		} else if rep.FilesIndexed > 0 || rep.MarkedDead > 0 {
			slog.Info("增量索引完成", "files", rep.FilesIndexed, "blocks", rep.BlocksAdded,
				"rebuilt", rep.Rebuilt, "dead", rep.MarkedDead)
		}
		time.Sleep(time.Duration(intervalSec) * time.Second)
	}
}
