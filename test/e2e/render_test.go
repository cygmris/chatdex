package e2e

import (
	"regexp"
	"strings"
	"testing"
)

// 本文件守的是**结构不变式**，不是"这一次跑对了"。
//
// XSS 与渲染效果的行为验证在真浏览器里做（DOMPurify 需要真实 DOM，Go 里跑不了），
// 结论记在实现日志里。这里断言的是让那些结论继续成立的前提：消毒环节还在、
// 路由还是集中的、vendored 库与许可还在包里。这些一旦被改坏，浏览器实测未必
// 当场发现，而这个测试会。

// 渲染管线必须是 marked → DOMPurify → DOM，中间那一环不可省。
//
// 实测过 marked 的裸输出：<script>、onerror、javascript: href、<iframe>
// 四种全部原样透传。所以"忘了消毒"不是变难看，是直接开洞。
func TestMarkdownPipelineKeepsSanitizer(t *testing.T) {
	e := start(t)
	boot := fetchText(t, e.uiPort, "/boot.js")

	if !strings.Contains(boot, "DOMPurify.sanitize") {
		t.Fatal("CD.md 里没有 DOMPurify.sanitize —— marked 单用会原样透传 script 与 javascript: href")
	}
	// 顺序：解析在前、消毒在后，且都在 CD.md 里
	mdStart := strings.Index(boot, "CD.md = ")
	if mdStart < 0 {
		t.Fatal("找不到 CD.md")
	}
	body := boot[mdStart:]
	if end := strings.Index(body, "\nCD."); end > 0 {
		body = body[:end]
	}
	parse := strings.Index(body, "marked.parse")
	clean := strings.Index(body, "DOMPurify.sanitize")
	if parse < 0 || clean < 0 || parse > clean {
		t.Errorf("CD.md 里 parse/sanitize 的顺序不对：parse=%d sanitize=%d", parse, clean)
	}
	// 光查「sanitize 这个字符串在不在」是不够的：在它前面插一句 return，
	// 字符串还在但那行永远跑不到。做变异验证时这一招真的骗过了第一版断言。
	//
	// 合法写法本身是 `return window.DOMPurify.sanitize(...)`，所以要放过
	// 紧挨着 sanitize 的那个 return，只拦它前面还有别的 return 的情况。
	between := body[parse:clean]
	if i := strings.LastIndex(between, "return"); i >= 0 {
		tail := strings.TrimSpace(between[i+len("return"):])
		tail = strings.TrimSpace(strings.TrimSuffix(tail, "window."))
		if tail != "" {
			t.Errorf("最后一个 return 没有直接返回消毒结果：%q", tail)
		}
		if strings.Contains(between[:i], "return") {
			t.Errorf("marked.parse 与 DOMPurify.sanitize 之间还有别的 return，消毒可能被绕过：%q",
				strings.TrimSpace(between[:i]))
		}
	}
	// URI 白名单：javascript:/data: 不能靠默认配置碰运气
	if !strings.Contains(body, "ALLOWED_URI_REGEXP") {
		t.Error("没有收紧 URI 白名单")
	}
	// 失败要退回原文而不是吞掉内容
	if !strings.Contains(body, "catch") || !strings.Contains(body, "md-fallback") {
		t.Error("CD.md 没有失败退回路径")
	}
}

// ANSI 必须先转义后解析。反了就等于把工具输出里的任意 HTML 注进页面——
// 与 escHit 同一条纪律，也是这个项目里犯过一次就不该再犯的错。
func TestANSIEscapesBeforeParsing(t *testing.T) {
	e := start(t)
	boot := fetchText(t, e.uiPort, "/boot.js")

	start := strings.Index(boot, "CD.ansi = ")
	if start < 0 {
		t.Fatal("找不到 CD.ansi")
	}
	body := boot[start:]
	if end := strings.Index(body, "\n/* ---"); end > 0 {
		body = body[:end]
	}
	// 快路径与慢路径都必须过 CD.esc
	if !strings.Contains(body, "CD.esc") {
		t.Fatal("CD.ansi 里没有 CD.esc —— 工具输出里的 HTML 会被原样注入")
	}
	if !strings.Contains(body, "text.slice") || !strings.Contains(body, "CD.esc(text.slice") {
		t.Error("正文分片没有逐段转义")
	}
	// 不得引入终端模拟器（0.19% 的占比不值得）
	if strings.Contains(boot, "xterm") {
		t.Error("引入了终端模拟器，与设计相悖")
	}
}

// 路由只能走 CD.route。新写一个视图时顺手 location.search 一把，
// URL 与界面就会各说各话，而这种不一致不会有任何报错。
func TestViewsDoNotTouchHistoryDirectly(t *testing.T) {
	e := start(t)
	for _, v := range []string{"search", "digest", "timeline", "chat", "settings", "reader"} {
		src := fetchText(t, e.uiPort, "/views/"+v+".js")
		for _, bad := range []string{"history.pushState", "history.replaceState", "location.search", "window.location ="} {
			if strings.Contains(src, bad) {
				t.Errorf("views/%s.js 里直接用了 %s —— 路由必须集中在 CD.route", v, bad)
			}
		}
	}
}

// vendored 库与许可都得在包里：少了库页面直接白屏，少了许可是分发时的许可违约，
// 而后者不会有任何其他信号。
func TestVendoredLibsAndLicensesShipped(t *testing.T) {
	e := start(t)
	for path, want := range map[string]string{
		"/vendor/marked.min.js":         "marked",
		"/vendor/purify.min.js":         "DOMPurify",
		"/vendor/LICENSE-marked.txt":    "MIT",
		"/vendor/LICENSE-dompurify.txt": "Apache License",
	} {
		if body := fetchText(t, e.uiPort, path); !strings.Contains(body, want) {
			t.Errorf("%s 里没有 %q", path, want)
		}
	}
}

// 页面不得在运行时引用外部域名（离线可用 + 不向第三方泄露浏览行为）。
//
// 按「是否发起请求」判定而不是简单 grep 域名：DOMPurify 里有 w3.org 的
// **命名空间字符串**（createElementNS 用的），那不是网络请求。
var fetchLikeRe = regexp.MustCompile(`(?:src|href)\s*=\s*["']https?://|fetch\(\s*["']https?://|import\(\s*["']https?://`)

func TestNoRuntimeExternalRequests(t *testing.T) {
	e := start(t)
	for _, p := range []string{"/", "/boot.js", "/theme.css", "/layout.css",
		"/views/search.js", "/views/reader.js", "/views/chat.js",
		"/vendor/marked.min.js", "/vendor/purify.min.js"} {
		if m := fetchLikeRe.FindString(fetchText(t, e.uiPort, p)); m != "" {
			t.Errorf("%s 会向外部域名发起请求：%q", p, m)
		}
	}
}

// 工具调用渲染的结构不变式（R4）。
//
// 行为验证（真实语料下的命令/前后对照/diff 着色）在浏览器里做，结论记在
// 实现日志里。这里守的是让那些结论继续成立的前提。
func TestToolCallRenderingInvariants(t *testing.T) {
	e := start(t)
	boot := fetchText(t, e.uiPort, "/boot.js")

	// 映射表只能有一处声明。写第二张表不会报错，只会让两处慢慢漂移，
	// 而漂移的表现是"某个工具的某个字段莫名其妙不显示了"。
	if n := strings.Count(boot, "const TOOL_MAP"); n != 1 {
		t.Errorf("TOOL_MAP 声明了 %d 次，只能有一处", n)
	}
	for _, v := range []string{"search", "digest", "timeline", "chat", "settings", "reader"} {
		if strings.Contains(fetchText(t, e.uiPort, "/views/"+v+".js"), "TOOL_MAP") {
			t.Errorf("views/%s.js 里出现了 TOOL_MAP —— 映射表必须只在 boot.js", v)
		}
	}

	// patch 渲染必须先转义再包 span。反了就是把 patch 内容里的任意 HTML
	// 注进页面——与 escHit / CD.ansi 同一条纪律。
	start := strings.Index(boot, "function renderPatch")
	if start < 0 {
		t.Fatal("找不到 renderPatch")
	}
	body := boot[start:]
	if end := strings.Index(body, "\nfunction "); end > 0 {
		body = body[:end]
	}
	esc := strings.Index(body, "CD.esc(line)")
	span := strings.Index(body, "p-add")
	if esc < 0 {
		t.Fatal("renderPatch 没有对整行做 CD.esc")
	}
	if span > 0 && esc > span {
		t.Error("renderPatch 先包了 span 才转义 —— 顺序反了会注入 HTML")
	}

	// 未引入语法高亮库（需求 5.1）
	for _, lib := range []string{"highlight.js", "hljs", "Prism", "shiki"} {
		if strings.Contains(boot, lib) {
			t.Errorf("引入了语法高亮 %s —— 本期明确不做", lib)
		}
	}

	// diff 颜色必须走 token：写死 hex 的话总有一套主题下看不见。
	//
	// 只看到本条规则的 } 为止——第一版用固定 90 字符窗口，会越界读到下一条
	// 规则里的 var(--) 从而放行写死的颜色（变异验证时真的漏掉了）。
	css := fetchText(t, e.uiPort, "/layout.css")
	for _, cls := range []string{".p-add", ".p-del"} {
		i := strings.Index(css, cls+" {")
		if i < 0 {
			t.Errorf("layout.css 里没有 %s 规则", cls)
			continue
		}
		end := strings.Index(css[i:], "}")
		if end < 0 {
			t.Errorf("%s 规则没有闭合", cls)
			continue
		}
		if rule := css[i : i+end]; !strings.Contains(rule, "var(--") {
			t.Errorf("%s 用了写死颜色：%q", cls, strings.TrimSpace(rule))
		}
	}
}
