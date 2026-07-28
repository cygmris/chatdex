// chatdex —— 统一索引并检索 Claude Code 与 Codex 全部会话记录的本地常驻服务。
//
// 只读、只监听 127.0.0.1。子命令分发照 specloop 的形态用标准库 flag，不引 CLI 框架。
package main

import (
	"fmt"
	"os"
)

const usage = `chatdex —— Claude Code / Codex 会话检索服务

用法:
  chatdex serve     常驻服务（dashboard :5021 / API+MCP :5022）
  chatdex index     跑一轮索引后退出
  chatdex status    打印索引状态
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "index":
		err = runIndex(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "chatdex:", err)
		os.Exit(1)
	}
}
