// Package dashboard 提供只读的 Web 界面。
//
// 静态资源用 embed.FS 打包进二进制，无前端构建链——与 specloop 的 dashboard 同一形态。
// 界面只读：不提供任何写入或删除入口。
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var assets embed.FS

// Register 把静态资源挂到 mux 的根路径。
//
// 用 "/" 而不是 "GET /"：Go 1.22 的 ServeMux 会拒绝「方法更窄但路径更宽」
// 与「方法更宽但路径更窄」相冲突的两个模式——"GET /" 撞上 "/mcp/" 会在
// 注册时直接 panic。根兜底不带方法限定，/mcp 这类更具体的路径才能正常共存。
func Register(mux *http.ServeMux) {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // 编译期就打包好了，取不到只可能是构建出错
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
}
