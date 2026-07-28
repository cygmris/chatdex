package mcpserver

import (
	"context"
	"net/http"

	"github.com/cygmris/chatdex/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "chatdex"
	serverVersion = "0.1.0"
)

// New 建立 MCP 服务端并注册三个检索工具。
func New(engine *search.Engine) *mcp.Server {
	t := &Tools{Engine: engine}
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "search_sessions",
		Description: "检索 Claude Code 与 Codex 的历史会话。按相关度返回会话列表，" +
			"每条含会话 id、项目路径、原始文件绝对路径、命中片段与最佳命中位置（best_seq）。" +
			"无命中时明确返回 no_match，不会给近似结果。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a SearchArgs) (*mcp.CallToolResult, SearchOutput, error) {
		out, err := t.SearchSessions(a)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_session",
		Description: "读取某个会话的消息序列。可用 from_seq 从命中位置开始读、用 limit 控制条数；" +
			"超出单次返回预算时会截断并在 note 里说明，可继续用 from_seq 往后读。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a GetSessionArgs) (*mcp.CallToolResult, SessionOutput, error) {
		out, err := t.GetSession(a)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_projects",
		Description: "列出索引里出现过的项目路径及其会话数，用于收窄检索范围。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ProjectsOutput, error) {
		out, err := t.ListProjects()
		return nil, out, err
	})

	return srv
}

// Register 把 MCP 端点挂到 mux。
//
// 不加方法限定：dashboard 的静态资源是 `GET /` 的 catch-all，
// 若这里只注册 POST /mcp，客户端的 GET /mcp 会落到文件服务器上变成 404 找不到文件，
// 排查起来毫无线索。
func Register(mux *http.ServeMux, engine *search.Engine) {
	srv := New(engine)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
}
