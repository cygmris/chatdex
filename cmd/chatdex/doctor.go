package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/cygmris/chatdex/internal/config"
	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/parser"
	"github.com/cygmris/chatdex/internal/version"
)

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := fs.String("config", config.Path(), "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Printf("版本     %s\n", version.String())
	fmt.Printf("配置     %s", *cfgPath)
	if _, err := os.Stat(*cfgPath); os.IsNotExist(err) {
		fmt.Printf("（文件不存在，使用默认值）\n")
	} else {
		fmt.Println()
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("家目录   %s\n", cfg.Home)
	fmt.Printf("索引库   %s\n", cfg.DBPath)
	fmt.Printf("端口     UI %d / API %d（只监听 127.0.0.1）\n", cfg.Ports.UI, cfg.Ports.API)

	reg := parser.NewRegistry(
		parser.Claude{Home: cfg.Home},
		parser.Codex{Home: cfg.Home},
		parser.Grok{Home: cfg.Home},
	)
	roots := cfg.Scan.Roots
	if len(roots) == 0 {
		roots = reg.Roots()
		fmt.Println("扫描根   默认（各解析器的会话目录）")
	} else {
		fmt.Println("扫描根   配置的 scan.roots（只扫这些真实目录；目录符号链接不会被跟进）")
	}
	for _, root := range roots {
		fmt.Printf("  %s\n", describeRoot(root))
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/api/health", cfg.Ports.API)
	if err := printHTTPHealth(url); err == nil {
		return nil
	}
	fmt.Printf("HTTP     %s 连不上（服务没在跑时正常）\n", url)
	return printIndexFile(cfg.DBPath)
}

func describeRoot(root string) string {
	fi, err := os.Lstat(root)
	switch {
	case os.IsNotExist(err):
		return root + "  不存在（没装对应工具时正常）"
	case err != nil:
		return root + "  " + err.Error()
	case fi.Mode()&os.ModeSymlink != 0:
		return root + "  是符号链接 — WalkDir 不会跟进，不会索引其目标"
	case !fi.IsDir():
		return root + "  不是目录"
	default:
		return root + "  真实目录"
	}
}

func printHTTPHealth(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var h map[string]any
	if err := json.Unmarshal(body, &h); err != nil {
		fmt.Printf("HTTP     %s  状态 %d，响应不是 JSON\n", url, resp.StatusCode)
		return nil
	}
	fmt.Printf("HTTP     %s  %d  ok=%v version=%v\n", url, resp.StatusCode, h["ok"], h["version"])
	return nil
}

func printIndexFile(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Println("索引库   尚不存在")
		return nil
	}
	st, err := index.Open(path)
	if err != nil {
		fmt.Printf("索引库   打不开：%v\n", err)
		return err
	}
	defer st.Close()
	s, err := st.Stats()
	if err != nil {
		fmt.Printf("索引库   已打开但读统计失败：%v\n", err)
		return err
	}
	fmt.Printf("索引库   可打开  会话 %d  块 %d  %s\n", s.Sessions, s.Blocks, humanBytes(s.DBBytes))
	return nil
}
