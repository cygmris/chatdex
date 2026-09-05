// Package version 暴露构建时注入的版本信息。
//
// 没走 ldflags 时保持 "dev"：`go run` / 测试也能跑，
// 装上的二进制才带 tag 与 commit。
package version

var (
	Version = "dev"
	Commit  = ""
)

func String() string {
	if Commit == "" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
