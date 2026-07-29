package e2e

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// 本文件覆盖任务 12 的两项：防闪烁的结构不变式、键盘可达的结构前提。
//
// **对比度不在这里**：四套主题的 WCAG AA 计算已经在
// internal/dashboard/theme_test.go 里做了（那里能直接读到 theme.css 的 token）。
// 在两个包各算一遍，就是本项目一路在防的「两边各写一份必然漂移」。
// 这是与 tasks.md 任务 12 描述的一处偏离，已在实现日志里记录。
//
// 浏览器里才能验的两件事（暗色偏好下首帧截图、Tab/Enter 实际操作）由
// claude-browser 实测完成，结论同样记在实现日志里。这里验的是**让它们成立的
// 那些结构前提**——首帧截图只能证明「这一次没闪」，而结构不变式一旦被改坏，
// 这个测试会立刻失败。

func fetchText(t *testing.T, port int, path string) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		t.Fatalf("取 %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("取 %s 状态码 = %d", path, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var inlineScriptRe = regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)

// 防闪烁靠的是：主题在**首屏绘制之前**就定下来。
// 能做到这一点的唯一写法是一段内联脚本，位于 <head> 内、样式表之后、<body> 之前。
// 外链脚本要等 HTML 解析完才执行，那时浏览器已经用默认色画过一帧。
func TestAntiFlashInlineScriptIsInHead(t *testing.T) {
	e := start(t)
	html := fetchText(t, e.uiPort, "/")

	headEnd := strings.Index(html, "</head>")
	bodyStart := strings.Index(html, "<body")
	if headEnd < 0 || bodyStart < 0 {
		t.Fatal("页面没有 head/body")
	}

	var inline []struct{ pos int }
	for _, m := range inlineScriptRe.FindAllStringSubmatchIndex(html, -1) {
		tag := html[m[0]:m[1]]
		if strings.Contains(tag, "src=") {
			continue
		}
		inline = append(inline, struct{ pos int }{m[0]})
	}

	// 多一段内联脚本，就多一处「首屏之前会跑什么」的不确定；
	// 这条约束也是 design.md 里写死的（全站唯一允许的内联脚本）
	if len(inline) != 1 {
		t.Fatalf("页面里有 %d 段内联脚本，只允许防闪烁那一段", len(inline))
	}
	pos := inline[0].pos
	if pos > headEnd {
		t.Error("防闪烁脚本不在 <head> 里——它会在首帧之后才执行，等于没有")
	}
	if pos > bodyStart {
		t.Error("防闪烁脚本出现在 <body> 之后")
	}
	if css := strings.LastIndex(html[:pos], "theme.css"); css < 0 {
		t.Error("防闪烁脚本在样式表之前——主题变量还没定义就先选了主题")
	}

	body := inlineScriptRe.FindAllStringSubmatch(html, -1)
	var script string
	for _, m := range body {
		if !strings.Contains(m[0], "src=") {
			script = m[1]
		}
	}
	for _, must := range []string{"prefers-color-scheme", "dataset.theme", "localStorage"} {
		if !strings.Contains(script, must) {
			t.Errorf("防闪烁脚本里没有 %s——它凭什么知道该用哪套主题？", must)
		}
	}
	// 系统偏暗时必须落到暗色主题名上，不能兜底成亮色
	if !strings.Contains(script, "editor") {
		t.Error("防闪烁脚本没有暗色主题兜底名")
	}
}

// 键盘可达的结构前提：能点的东西必须是能聚焦的东西。
//
// 这是**源码层面**的检查，不是 DOM 检查——它挡的是「新写一个视图时，
// 顺手用 div + onclick 做了一个列表」这类回归。实际的 Tab/Enter 操作由
// 浏览器实测覆盖。
func TestClickableThingsAreFocusable(t *testing.T) {
	e := start(t)

	// 左栏导航（切视图）必须是真 button：div 不进 Tab 序列
	boot := fetchText(t, e.uiPort, "/boot.js")
	if strings.Contains(boot, `<div class="side-nav`) || strings.Contains(boot, `<div class="side-mini`) {
		t.Error("左栏导航用了 div——键盘 Tab 不到，切视图就没法用键盘完成")
	}
	if !strings.Contains(boot, "CD.clickable") {
		t.Error("boot.js 里没有 CD.clickable：非 button 元素的键盘触发要靠它")
	}

	// 三个结果列表的条目是 <article>（一条结果里有标题、片段、多行元信息，
	// 塞进 button 语义和排版都不对），所以必须走 CD.clickable 补上 tabIndex + Enter
	for _, v := range []string{"search", "digest", "timeline"} {
		src := fetchText(t, e.uiPort, "/views/"+v+".js")
		if !strings.Contains(src, "CD.clickable") {
			t.Errorf("views/%s.js 的条目直接挂 onclick，键盘用户搜到了却打不开", v)
		}
		if strings.Contains(src, ".onclick = () => CD.openSession") {
			t.Errorf("views/%s.js 里还有裸的 onclick 打开会话", v)
		}
	}
}

// 能聚焦但看不出焦点在哪，和不能聚焦一样没法用。
func TestFocusRingIsVisible(t *testing.T) {
	e := start(t)
	css := fetchText(t, e.uiPort, "/layout.css")

	if !strings.Contains(css, ":focus-visible") {
		t.Fatal("layout.css 里没有 :focus-visible 焦点环")
	}
	// 焦点环必须用主题 token：写死颜色的话，四套主题里总有一套看不见
	i := strings.Index(css, ":focus-visible")
	block := css[i:min(i+200, len(css))]
	if !strings.Contains(block, "var(--") {
		t.Error("焦点环用了写死的颜色，暗色主题下可能几乎不可见")
	}

	// input/select 的 outline: none 必须有替代（border-color + box-shadow）
	if strings.Contains(css, "outline: none") && !strings.Contains(css, "box-shadow: 0 0 0 2px") {
		t.Error("去掉了 outline 却没给替代的焦点提示")
	}
}
