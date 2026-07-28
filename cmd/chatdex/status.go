package main

import (
	"flag"
	"fmt"

	"github.com/cygmris/chatdex/internal/config"
	"github.com/cygmris/chatdex/internal/index"
)

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := fs.String("config", config.Path(), "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	st, err := index.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	s, err := st.Stats()
	if err != nil {
		return err
	}
	fmt.Printf("索引库   %s（%s）\n", cfg.DBPath, humanBytes(s.DBBytes))
	fmt.Printf("会话     %d（失效 %d）\n", s.Sessions, s.DeadSessions)
	fmt.Printf("内容块   %d（截断 %d）\n", s.Blocks, s.TruncatedBlocks)
	fmt.Printf("已有摘要 %d\n", s.Summarized)
	return nil
}
