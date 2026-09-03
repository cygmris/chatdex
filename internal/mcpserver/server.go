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
		Description: "检索 Claude Code、Codex 与 Grok CLI 的历史会话。按相关度返回会话列表，" +
			"每条含会话 id、项目路径、原始文件绝对路径、命中片段与最佳命中位置（best_seq）。" +
			"无命中时明确返回 no_match，不会给近似结果。\n" +
			"读结果时注意两个字段的分工：summary 是这个会话的结论，snippet 只是**它为什么匹配**" +
			"的证据，实测七成以上落在 tool_result / tool_use 上——那是工具输出或调用参数，" +
			"不是任何人的结论。要确认结论请用 get_session 从 best_seq 读上下文，别把 snippet 当答案。\n" +
			"加 kind 过滤可以只看散文，但要清楚各是什么：assistant 是**模型当时的论述**" +
			"（可能是推导途中的说法，后文推翻了它也不会有任何标记），user 才是使用者本人写的" +
			"（实测只占 2%）。两者都不等于结论。\n" +
			"结果里出现 last_seq 时，说明该会话在 best_seq 之后**还有对同一关键词的讨论**" +
			"（实测约四成的命中会这样）。它的含义仅此而已：后面还有内容，值得用 get_session " +
			"从 last_seq 再读一段，别只凭 best_seq 那一处就下结论。\n" +
			"⚠️ 它**不表示**前面那处是错的——实测最佳块在会话中的位置中位 56%、前后均匀分布，" +
			"「相关度偏向早期的错误版本」并不成立。没有这个字段则表示 best_seq 已是最后一处命中。",
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
