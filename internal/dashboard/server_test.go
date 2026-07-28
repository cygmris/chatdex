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
		{"/app.js", "escHit"},
		{"/style.css", "--accent"},
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
		if !strings.Contains(string(body), c.want) {
			t.Errorf("%s 内容不含 %q", c.path, c.want)
		}
	}
}
