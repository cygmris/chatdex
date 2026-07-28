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
func Register(mux *http.ServeMux) {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // 编译期就打包好了，取不到只可能是构建出错
	}
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
}
