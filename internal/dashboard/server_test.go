package dashboard_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/dashboard"
)

func TestStaticAssetsAreEmbedded(t *testing.T) {
	mux := http.NewServeMux()
	dashboard.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, c := range []struct{ path, want string }{
		{"/", "chatdex"},
		{"/boot.js", "escHit"},
		{"/theme.css", "--accent"},
		{"/layout.css", "var(--bg)"},
		{"/views/search.js", "register"},
		{"/views/digest.js", "kind: 'summary'"},
		{"/views/timeline.js", "register"},
		{"/views/chat.js", "register"},
		{"/views/reader.js", "openSession"},
		{"/fonts/IBMPlexSans-400-latin.woff2", ""},
		// vendored 第三方库：随二进制打包，不从 CDN 加载（需求 3.6）
		{"/vendor/marked.min.js", "marked"},
		{"/vendor/purify.min.js", "DOMPurify"},
		{"/vendor/hljs/highlight.min.js", "hljs"},
		{"/vendor/hljs/github.css", "hljs"},
		// mermaid 随包分发但**不在页面上加载**（3.5 MB，见 TestMermaidNotLoadedUpfront）
		{"/vendor/mermaid.min.js", "mermaid"},
		// 许可文件也要在，否则分发时就少了归属声明（需求 3.7）
		{"/vendor/LICENSE-marked.txt", "MIT"},
		{"/vendor/LICENSE-dompurify.txt", "Apache License"},
		{"/vendor/LICENSE-highlightjs.txt", "BSD"},
		{"/vendor/LICENSE-mermaid.txt", "MIT"},
	} {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s 状态码 = %d", c.path, resp.StatusCode)
		}
		if c.want != "" && !strings.Contains(string(body), c.want) {
			t.Errorf("%s 内容不含 %q", c.path, c.want)
		}
	}
}
