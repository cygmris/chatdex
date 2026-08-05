package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
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

	// R7 起**明确引入** highlight.js。R4/R6 曾以「代码块只占 2–5%」为由拒绝，
	// 但那个统计漏了 tool_use 的命令（52.1%）——把命令算进来性价比翻转。
	// 断言随之从「不得引入」改为「必须是 vendored 的、不得从 CDN 拉」。
	page := fetchText(t, e.uiPort, "/")
	if !strings.Contains(page, "/vendor/hljs/highlight.min.js") {
		t.Error("页面没有加载 vendored 的 highlight.js")
	}
	for _, cdn := range []string{"cdn.jsdelivr", "unpkg.com", "cdnjs."} {
		if strings.Contains(page, cdn) {
			t.Errorf("从 CDN 加载了资源（%s）—— 必须 vendored，页面不得引用外部域名", cdn)
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

// R7 的两条结构不变式。
//
// 一、mermaid 不得进 index.html。它 3.4 MB —— 比页面其余全部资产加起来还大
// 一个量级。一旦谁"顺手"把它加进 <script>，页面首屏体积翻十几倍，而**功能一切
// 正常**，不会有任何报错提醒。这正是需要测试守的那类退化。
//
// 二、高亮配色必须走 data-hl，且默认那套用既有 token —— 与 data-theme 同机制。
func TestMermaidLoadedOnDemandOnly(t *testing.T) {
	e := start(t)
	page := fetchText(t, e.uiPort, "/")
	if strings.Contains(page, "mermaid") {
		t.Error("index.html 里出现了 mermaid —— 它有 3.4 MB，必须点击时才动态加载")
	}
	// 但它得在包里，否则点了按钮 404
	if body := fetchText(t, e.uiPort, "/vendor/mermaid.min.js"); len(body) < 1_000_000 {
		t.Errorf("vendored mermaid 缺失或不完整（%d 字节）", len(body))
	}
	if lic := fetchText(t, e.uiPort, "/vendor/LICENSE-mermaid.txt"); !strings.Contains(lic, "MIT") {
		t.Error("mermaid 的 MIT 许可没有随包分发")
	}

	boot := fetchText(t, e.uiPort, "/boot.js")
	// 动态加载 + 消毒两件事都得在
	if !strings.Contains(boot, "/vendor/mermaid.min.js") {
		t.Error("boot.js 里没有动态加载 mermaid 的路径")
	}
	// 渲染器输出必须过 DOMPurify：图里的标签文本来自会话内容
	i := strings.Index(boot, "function drawMermaid")
	if i < 0 {
		t.Fatal("找不到 drawMermaid")
	}
	body := boot[i:]
	if end := strings.Index(body, "\n/* ---"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "DOMPurify.sanitize") {
		t.Error("mermaid 的 SVG 没过 DOMPurify —— 渲染器的输出同样不可信")
	}
	if !strings.Contains(body, "USE_PROFILES") {
		t.Error("DOMPurify 没限定 svg profile")
	}
	// htmlLabels 必须关。mermaid 默认把节点文字放进 <foreignObject> 里的 HTML，
	// 而 svg profile 会把那段 HTML 整个剥掉——**图还在，字全没了**。
	// 这个坑是浏览器实测才发现的：只断言"出了 svg"不会咬到它。
	//
	// 只在 initialize(...) 的实参里找，不在整个 boot.js 里找：解释这件事的注释
	// 本身就含 "htmlLabels: false"，全文匹配会匹到我自己写的说明，把配置删了也
	// 照样通过（变异验证时就是这样漏掉的，和 R6 那次匹到自己注释是同一个坑）。
	ini := strings.Index(boot, "m.initialize({")
	if ini < 0 {
		t.Fatal("找不到 mermaid 的 initialize 调用")
	}
	call := boot[ini:]
	if end := strings.Index(call, "});"); end > 0 {
		call = call[:end]
	}
	if !strings.Contains(call, "htmlLabels: false") {
		t.Errorf("mermaid 没关 htmlLabels —— 节点文字会被 svg profile 连同 foreignObject 一起剥掉：%q", call)
	}
}

func TestHighlightThemeUsesExistingTokens(t *testing.T) {
	e := start(t)
	css := fetchText(t, e.uiPort, "/layout.css")
	// 默认（不打 data-hl）那套必须存在，且色值走 token —— 否则四套界面主题里
	// 总有一套下高亮看不见，而这不会有任何报错。
	// 逐条规则取，不整段截。第一版用「找到下一个换行加右括号」当收尾，实际读到了
	// 几十行之外的工具类 CSS——取样范围比命题宽，就会为不相干的改动误报。
	// 这是本项目第三次栽在取样范围上（R4 的 90 字符窗口、R6 的自评注释）。
	var rules []string
	for at := 0; ; {
		i := strings.Index(css[at:], ":root:not([data-hl])")
		if i < 0 {
			break
		}
		i += at
		end := strings.Index(css[i:], "}")
		if end < 0 {
			t.Fatalf("跟随主题的高亮规则没闭合：%q", css[i:min(i+80, len(css))])
		}
		rules = append(rules, css[i:i+end])
		at = i + end
	}
	if len(rules) == 0 {
		t.Fatal("layout.css 里没有 :root:not([data-hl]) 这套跟随主题的高亮")
	}
	var mapped, token int
	for _, r := range rules {
		if strings.Contains(r, "hljs-") {
			mapped++
		}
		if strings.Contains(r, "var(--") {
			token++
		}
		// 色值写死 hex 的话，总有一套界面主题下高亮看不见
		if strings.Contains(r, "#") {
			t.Errorf("跟随主题的高亮里出现写死颜色：%q", strings.TrimSpace(r))
		}
	}
	if mapped == 0 {
		t.Error("这套规则没有映射任何 hljs-* 类")
	}
	if token != len(rules) {
		t.Errorf("%d 条规则里只有 %d 条走 token", len(rules), token)
	}
	if !strings.Contains(strings.Join(rules, ""), "var(--ansi-") {
		t.Error("跟随主题的高亮没有复用既有的 --ansi-* token")
	}
}

// 子代理标记必须挂在 is_sub 上，不能挂在 agent_label 上。
//
// 两者在当前语料里恰好等价（1554 个子代理各有一个 agent_label），所以挂错了
// **界面看起来完全正常**——直到某个子代理没有 label，标记就静默消失，而筛选
// 仍然把它算作子代理。标记与筛选必须来自同一个字段，才不会各说各话。
//
// agent_label 的取值是 agent-<hex> 这类随机标识，本身也不该出现在界面上。
func TestSubagentBadgeKeysOffIsSub(t *testing.T) {
	e := start(t)
	for _, v := range []string{"search", "timeline", "digest"} {
		src := fetchText(t, e.uiPort, "/views/"+v+".js")
		if !strings.Contains(src, "s.is_sub") {
			t.Errorf("views/%s.js 没用 is_sub 判断子代理", v)
		}
		if strings.Contains(src, "agent_label") {
			t.Errorf("views/%s.js 里还在用 agent_label —— 它是随机标识，不是身份判据", v)
		}
	}
	// 过滤条件必须进 URL，否则分享出去的链接还原不出同样的结果
	boot := fetchText(t, e.uiPort, "/boot.js")
	i := strings.Index(boot, "const ROUTE_KEYS")
	if i < 0 {
		t.Fatal("找不到 ROUTE_KEYS")
	}
	end := strings.Index(boot[i:], "]")
	if end < 0 || !strings.Contains(boot[i:i+end], "'agent'") {
		t.Errorf("agent 没进 ROUTE_KEYS：%q", boot[i:i+end])
	}
}

// 回读页的父子入口不得把 agent_label 当名字显示，且子代理列表必须点开才拉。
//
// 「点开才拉」这条守的是请求数：只有 61 / 3207 的会话有子代理，若在 load() 里
// 无条件取一次，98% 的回读会白发一个请求——而**功能一切正常**，不会有任何信号。
func TestReaderRelationsAreLazyAndLabelFree(t *testing.T) {
	e := start(t)
	src := fetchText(t, e.uiPort, "/views/reader.js")

	if strings.Contains(src, "agent_label") {
		t.Error("reader.js 里还在显示 agent_label —— 它是随机标识（agent-<hex>），不是名字")
	}
	// 取子代理列表的调用只能出现在点击处理里，不能在 load() 里
	call := "/children"
	i := strings.Index(src, call)
	if i < 0 {
		t.Fatal("reader.js 没有取子代理列表的调用")
	}
	// 直接取 load() 的函数体来判定，不靠"往回找最近的函数声明"。
	// 第一版就是那么写的，锚点用了 "\n  function "，而 load 的声明是
	// "\n  async function load()" —— 匹配不上，于是把调用挪进 load 的变异**没被咬**。
	// 取样得对准命题本身：命题是「/children 不在 load 体内」，那就去看 load 体内。
	ls := strings.Index(src, "async function load()")
	if ls < 0 {
		t.Fatal("找不到 load()")
	}
	body := src[ls:]
	// load() 之后的下一个函数声明即为其结束边界
	if end := strings.Index(body[1:], "\n  function "); end > 0 {
		body = body[:end]
	}
	if strings.Contains(body, call) {
		t.Error("子代理列表在 load() 里就取了 —— 98% 的会话没有子代理，这是白发的请求")
	}
	_ = i
	if !strings.Contains(src, "child_count") {
		t.Error("没有用 child_count 决定是否显示入口 —— 会出现「0 个子代理」的空块")
	}
}

// 消毒器剥掉 href 之后不能留下一个看着能点的 <a>。
//
// 与 mermaid 那次「图还在字没了」同一形状：**渲染成功不等于渲染正确**，
// 而消毒器恰好只吃掉后者，页面上看不出任何异常。
func TestStrippedLinksDoNotLookClickable(t *testing.T) {
	e := start(t)
	boot := fetchText(t, e.uiPort, "/boot.js")

	if !strings.Contains(boot, "a:not([href])") {
		t.Error("没有处理被剥掉 href 的 <a> —— 页面上会留下点了没反应的死链")
	}
	// 原地址必须留档：这是取证工具，file:///etc/passwd 本身就是信息
	if !strings.Contains(boot, "data-dead-href") {
		t.Error("被剥掉的原地址没有留档")
	}
	// 留档属性必须先清掉内容自带的同名属性，否则正文能伪造出一个地址。
	//
	// 命题是「清除发生在写入之前」，所以就去比这两个调用的先后，不要用
	// 「写入点附近 N 字节内应当出现清除」这种窗口——中文注释一个字三字节，
	// 400 字节只够一百来个字，窗口会莫名其妙地不够长。（这是本项目第五次
	// 在取样范围上栽跟头，前四次是窗口越界、匹到自己注释、锚点对不上。）
	clear := strings.Index(boot, "removeAttribute('data-dead-href')")
	set := strings.Index(boot, "setAttribute('data-dead-href'")
	if clear < 0 {
		t.Error("没有清掉正文自带的 data-dead-href —— 会话内容可以伪造地址")
	}
	if set < 0 {
		t.Error("没有写入 data-dead-href —— 被剥掉的原地址没有留档")
	}
	if clear >= 0 && set >= 0 && clear > set {
		t.Error("先写入后清除 —— 顺序反了，等于没清")
	}
	// 渲染后的加工必须走统一入口，否则新加一道加工要挨个补调用、漏一处没有报错
	for _, v := range []string{"reader", "chat"} {
		src := fetchText(t, e.uiPort, "/views/"+v+".js")
		if strings.Contains(src, "CD.highlightCodeBlocks") {
			t.Errorf("views/%s.js 直接调了 CD.highlightCodeBlocks —— 应当走 CD.enhance", v)
		}
	}
}

// 配置相关的读取必须经过就绪门控。
//
// 配置由 /api/config 异步取回，而用到它的代码可能先跑完——输了这场竞速就
// **静默地什么都不做**：没有报错、没有错值，只是那个功能"没生效"。
// 本项目撞见过两次（R2 的「共 0 条摘要」、R7 的 ui.mermaid_auto），
// 两次都是肉眼偶然发现的。
func TestConfigReadsAreGated(t *testing.T) {
	e := start(t)
	boot := fetchText(t, e.uiPort, "/boot.js")

	if !strings.Contains(boot, "CD.cfgReady") {
		t.Fatal("没有配置就绪的门控（CD.cfgReady）")
	}
	// 用到 mermaidAuto 的地方必须在门控之后，不能同步读当下的值
	i := strings.Index(boot, "if (CD.mermaidAuto)")
	if i < 0 {
		t.Fatal("找不到 CD.mermaidAuto 的使用点")
	}
	// 取该行本身，不用定长窗口——中文注释一个字三字节，定长窗口的长度不可靠
	lineStart := strings.LastIndex(boot[:i], "\n") + 1
	lineEnd := i + strings.Index(boot[i:], "\n")
	line := boot[lineStart:lineEnd]
	if !strings.Contains(line, "cfgReady") {
		t.Errorf("CD.mermaidAuto 被同步读取，没有等配置就绪：%q", strings.TrimSpace(line))
	}
}

// 工具结果的高亮只能按调用参数判语言，不得对输出做自动识别。
//
// 输出里除了源码还有大量构建日志、traceback、git status。对它们做自动识别
// 只会乱涂颜色——而**乱涂比不上色更糟**：它让人以为那些颜色有含义。
// 实测反例：highlightAuto 把一段明显的 JavaScript 认成了 php。
func TestResultHighlightNeverAutoDetects(t *testing.T) {
	e := start(t)
	boot := fetchText(t, e.uiPort, "/boot.js")

	// 判定函数必须存在，且判定依据是调用参数
	i := strings.Index(boot, "CD.resultLang = ")
	if i < 0 {
		t.Fatal("找不到 CD.resultLang")
	}
	body := boot[i:]
	if end := strings.Index(body, "\n/* "); end > 0 {
		body = body[:end]
	}
	if strings.Contains(body, "highlightAuto") {
		t.Error("结果的语言判定里出现了 highlightAuto —— 本期明确禁止对输出做自动识别")
	}
	// 必须逐段判定：只有以读取命令开头的段，其参数才算数。
	// 光看「命令里有没有 cat/head」会把 `git diff x.go | head` 判成 Go。
	if !strings.Contains(body, "READ_HEAD") {
		t.Error("语言判定没有逐段做 —— git diff x.go | head 会被判成源码")
	}

	// 判不出语言时必须退回既有路径，不能兜底成自动识别
	j := strings.Index(boot, "CD.toolResult = ")
	if j < 0 {
		t.Fatal("找不到 CD.toolResult")
	}
	tr := boot[j:]
	if end := strings.Index(tr, "\n/* "); end > 0 {
		tr = tr[:end]
	}
	if !strings.Contains(tr, "CD.ansi") {
		t.Error("判不出语言时没有退回 ANSI 路径")
	}
	// 含 ANSI 的输出不得被语法高亮覆盖：那是程序自己选的颜色
	if !strings.Contains(tr, `\x1b[`) {
		t.Error("没有检测 ANSI —— 两种上色叠加会把转义序列当成源码分析")
	}
}

// readRepoFile 读仓库里的源码文件。
//
// 这两条不变式守的是**后端 Go 代码**，不是下发给浏览器的静态资源，
// 所以不走 fetchText，也不必起服务。
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// 摘要队列的两条结构不变式，各自对应一个卡了两天的真实故障。
func TestSummaryPipelineInvariants(t *testing.T) {
	// ① 入队不得复活 failed。少了这条，一条生成不了的会话会每 2 分钟复活一次、
	// 永远排在最前面，其余任务全部饿死——而 failed 计数因为活不过一轮恒为 0，
	// 界面上完全看不出来。实测这样停摆了两天。
	//
	// 取样只看 EnqueueMissing 的函数体：解释这件事的注释里也会出现 'failed'，
	// 全文匹配会匹到我自己写的说明（本项目栽过三次）。
	src := readRepoFile(t, "internal/index/summarystore.go")
	i := strings.Index(src, "func (s *Store) EnqueueMissing()")
	if i < 0 {
		t.Fatal("找不到 EnqueueMissing")
	}
	body := src[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "'pending','running','failed'") {
		t.Error("EnqueueMissing 没有排除 failed —— 失败任务会被无限复活并饿死队列")
	}

	// ② LLM 请求必须显式设 num_ctx 与关掉 thinking。
	// 前者不设就是 Ollama 默认 2048 静默截断（连开头的指令一起丢）；
	// 后者不关，thinking 模型会把 num_predict 预算全花在推理上，
	// 返回空串而 done_reason=length——上层只看到「模型返回空摘要」。
	w := readRepoFile(t, "internal/summary/worker.go")
	j := strings.Index(w, "func (w *Worker) gen(")
	if j < 0 {
		t.Fatal("找不到 Worker.gen")
	}
	gen := w[j:]
	if end := strings.Index(gen, "\nfunc "); end > 0 {
		gen = gen[:end]
	}
	if !strings.Contains(gen, "NumCtx:") {
		t.Error("摘要请求没设 NumCtx —— Ollama 默认 2048 会静默截断掉开头的指令")
	}
	if !strings.Contains(gen, "NoThink: true") {
		t.Error("摘要请求没关 thinking —— 推理会占光 num_predict，回答为空串")
	}
}

// 分段参数不得写死，且不得再出现「只取首尾」的形状。
//
// 这两条各自对应一个真实缺陷：前者让 MaxInput(24000 字符) 与 num_ctx(8192 token)
// 长期矛盾，分了段仍被截断；后者让 62 个会话的中段整块消失，最大的一条
// 210 万字符只有约 5% 进了摘要。
func TestSummaryBudgetIsDerivedNotHardcoded(t *testing.T) {
	src := readRepoFile(t, "internal/summary/prompt.go")

	// 预算必须由 num_ctx 推导。取样只看 BudgetFor 的函数体——
	// 解释这件事的注释里也会出现 MaxInput 之类的字样。
	i := strings.Index(src, "func BudgetFor(")
	if i < 0 {
		t.Fatal("找不到 BudgetFor —— 预算又被写死了？")
	}
	body := src[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	// 只看**算出预算的那一行**，不看整个函数体：numCtx 在入口的守卫里也会出现
	// （`if numCtx < minNumCtx`），所以「函数体里提到 numCtx」这个条件太松——
	// 把计算式换成常量它照样通过。变异验证当场证实了这一点。
	line := ""
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, "runes :=") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("找不到计算预算的那一行")
	}
	if !strings.Contains(line, "numCtx") {
		t.Errorf("预算不是由 num_ctx 算出来的：%q", strings.TrimSpace(line))
	}

	// Split 不得再走「前一半 + 后一半」的老路
	j := strings.Index(src, "func Split(")
	if j < 0 {
		t.Fatal("找不到 Split")
	}
	sp := src[j:]
	if end := strings.Index(sp, "\nfunc "); end > 0 {
		sp = sp[:end]
	}
	if strings.Contains(sp, "all[len(all)-half:]") {
		t.Error("Split 仍在只取首尾 —— 中段会被整块丢弃")
	}
	if !strings.Contains(sp, "sampleGroup") {
		t.Error("Split 没有走分组取样 —— 超长会话的中段没有覆盖")
	}
}

// 两条 LLM 路径都必须经过同一处 options 装配。
//
// R11 只给 Generate 加了 num_ctx，Chat 是另一个函数、另一个端点，
// 没有任何东西提示它也需要——同一个坑隔一天就在问答链路上重演（被截在 2051 token）。
// 这条断言守的不是「某一处写对了」，而是**不会再有第二条路径自己拼 options**。
func TestBothLLMPathsShareOptionBuilder(t *testing.T) {
	src := readRepoFile(t, "internal/llm/ollama.go")

	for _, fn := range []string{"func (o *Ollama) Generate(", "func (o *Ollama) Chat("} {
		i := strings.Index(src, fn)
		if i < 0 {
			t.Fatalf("找不到 %s", fn)
		}
		body := src[i:]
		if end := strings.Index(body[1:], "\nfunc "); end > 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "buildOptions(") {
			t.Errorf("%s 没有走 buildOptions —— 自己拼 options 迟早会漏掉某个参数", fn)
		}
		// 自己塞 num_ctx 就说明绕开了共用装配
		if strings.Contains(body, `"num_ctx"`) {
			t.Errorf("%s 里直接写了 num_ctx —— 装配必须只有一处", fn)
		}
	}
}

// 问答的范围必须是硬约束，且注入只有一处。
//
// 「模型能做不等于模型会做」：search_sessions 的 project 参数与 list_projects
// 工具都存在了两期，实测模型一次都没主动用过。所以范围要用代码钉住，
// 不能只写进提示词——范围错了更糟，agent 会在错误的范围里反复改写查询。
func TestChatScopeIsEnforcedNotSuggested(t *testing.T) {
	src := readRepoFile(t, "internal/chat/tools.go")

	// 注入必须在 applyScope 这一处
	i := strings.Index(src, "func applyScope(")
	if i < 0 {
		t.Fatal("找不到 applyScope —— 范围注入必须集中在一处")
	}
	body := src[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, `"search_sessions"`) {
		t.Error("applyScope 没有限定只覆盖检索类工具")
	}

	// get_session 不得被范围挡住：已经定位到具体会话，再挡它会让
	// 「点开搜索结果」这条路径莫名其妙地失败
	if strings.Contains(body, `"get_session"`) {
		t.Error("applyScope 碰了 get_session —— 按 id 读取不该受范围限制")
	}

	// 别处不得再有第二个注入点
	agent := readRepoFile(t, "internal/chat/agent.go")
	if strings.Contains(agent, `Args["project"] =`) || strings.Contains(agent, `args["project"] =`) {
		t.Error("agent.go 里也在注入 project —— 注入必须只有一处")
	}
	// 事件流必须发**生效后**的参数：发模型给的等于展示一个没执行过的条件
	if !strings.Contains(agent, "args := applyScope(") {
		t.Error("事件流发的不是应用范围之后的参数")
	}
}

// 就绪信号必须先于挂载视图建立。
//
// 第一版把 CD.projectsReady 放在 CD.route.apply 之后，视图挂载时它还是
// undefined，`(CD.projectsReady || Promise.resolve()).then(...)` 的兜底立刻兑现——
// **写了门控却等于没等**，比压根没写更难发现，因为代码读起来像是处理过了。
func TestReadySignalsPrecedeViewMount(t *testing.T) {
	e := start(t)
	boot := fetchText(t, e.uiPort, "/boot.js")

	i := strings.Index(boot, "CD.boot = ")
	if i < 0 {
		t.Fatal("找不到 CD.boot")
	}
	body := boot[i:]
	if end := strings.Index(body, "\n};"); end > 0 {
		body = body[:end]
	}
	ready := strings.Index(body, "CD.projectsReady =")
	mount := strings.Index(body, "CD.route.apply(")
	if ready < 0 || mount < 0 {
		t.Fatalf("CD.boot 里缺少就绪信号或挂载调用：ready=%d mount=%d", ready, mount)
	}
	if ready > mount {
		t.Error("就绪信号在挂载视图之后才建立 —— 视图拿到 undefined，门控形同虚设")
	}
}

// 备份配置里的每个字段都必须真的有人读。
//
// 这条守的是一整类 bug 而不是一处：`backup.after_scan` 曾经就是个**死开关**
// ——配置结构体里有、设置页能勾、保存也成功，但全项目没有任何代码读它。
// 用户勾上之后什么都不会发生，而界面上完全看不出来。
//
// 判据是「字段名出现在 internal/config 之外的非测试 Go 代码里」：
// 声明它、把它塞进设置页元数据，都不算「用了它」。
func TestEveryBackupSettingIsActuallyRead(t *testing.T) {
	decl := readRepoFile(t, "internal/config/config.go")
	i := strings.Index(decl, "type Backup struct {")
	if i < 0 {
		t.Fatal("找不到 config.Backup")
	}
	body := decl[i:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	var fields []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "type ") {
			continue
		}
		if name, _, ok := strings.Cut(line, " "); ok && name != "" {
			fields = append(fields, name)
		}
	}
	if len(fields) < 4 {
		t.Fatalf("只解析出 %d 个字段（%v），解析多半错了", len(fields), fields)
	}

	// 收集 internal/config 之外的全部非测试 Go 源码
	var users strings.Builder
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		if strings.Contains(filepath.ToSlash(p), "/internal/config/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil {
			users.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	src := users.String()
	for _, f := range fields {
		if !strings.Contains(src, f) {
			t.Errorf("配置项 Backup.%s 没有任何代码读它 —— 界面上是个勾了没反应的死开关", f)
		}
	}
}

// 备份的三条结构不变式。
//
// 判据的取样范围要盯紧：「restic」这个字串现在**合法地**出现在 reader.js 的
// 恢复命令、backup.js 的 restic forget 提示、设置页帮助文字、两个 README 里
// ——那些都是展示文本，不是交互。按字串判会全线误报（本项目栽过九次取样范围）。
// 真正的命题是「**谁在起 restic 进程**」，所以只看 exec.Command 调用点。
func TestBackupStructuralInvariants(t *testing.T) {
	root := filepath.Join("..", "..")

	// ① 起进程只许在 internal/backup 里。
	// 别处冒出一条 exec.Command，就意味着命令行拼装有了第二个来源——
	// 加参数时必然漏掉一处（R13 立的规矩）。
	var offenders []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		slash := filepath.ToSlash(p)
		if strings.Contains(slash, "/internal/backup/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(b), "exec.Command") {
			offenders = append(offenders, slash)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("internal/backup 之外起了外部进程：%v —— 与 restic 的交互必须只有一个出口", offenders)
	}

	// ② 密码只经文件传，绝不进环境变量的值。
	// RESTIC_PASSWORD 会让密码出现在 /proc/<pid>/environ（同用户可读）。
	runner := readRepoFile(t, "internal/backup/runner.go")
	if !strings.Contains(runner, "RESTIC_PASSWORD_FILE=") {
		t.Error("没用 RESTIC_PASSWORD_FILE 传密码")
	}
	if strings.Contains(runner, `"RESTIC_PASSWORD="`) {
		t.Error("用了 RESTIC_PASSWORD —— 密码会出现在进程环境里")
	}

	// ③ 配置结构体里不得有可序列化的密码字段。
	// 存路径可以，存密码不行——配置文件会被备份、被同步、被截图。
	cfg := readRepoFile(t, "internal/config/config.go")
	i := strings.Index(cfg, "type Backup struct {")
	if i < 0 {
		t.Fatal("找不到 config.Backup")
	}
	body := cfg[i:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	// 只认字段声明行，跳过注释——注释里解释「密码不进配置」也会出现 Password
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "type ") {
			continue
		}
		name, _, _ := strings.Cut(line, " ")
		if strings.Contains(name, "Password") && !strings.Contains(name, "File") {
			t.Errorf("配置结构体里有可序列化的密码字段 %q —— 只能存路径", name)
		}
	}

	// ④ 解析 restic 输出只认 JSON。
	//
	// 命题是「**解析**输出时靠 JSON」，不是「每条命令都带 --json」——
	// `run(ctx, "init")` 不消费输出，不需要那个参数；firstLine 还得能处理
	// 纯文本 stderr。所以取样对象是**解析函数**，不是调用它们的函数
	// （第一版取了 Backup()，而它把解析委托给 parseSummary，当场误报）。
	parsers := map[string]string{
		"func parseSummary(":                "internal/backup/runner.go",
		"func (r *Runner) Snapshots(":       "internal/backup/runner.go",
		"func (r *Runner) filesInSnapshot(": "internal/backup/coverage.go",
		"func (r *Runner) locate(":          "internal/backup/fetch.go",
	}
	for fn, file := range parsers {
		src := readRepoFile(t, file)
		j := strings.Index(src, fn)
		// 锚点写错会取到空样本，而空样本永远「通过」——所以找不到直接 Fatal
		if j < 0 {
			t.Fatalf("取样锚点 %q 在 %s 里不存在 —— 断言取的是空样本", fn, file)
		}
		b := src[j:]
		if end := strings.Index(b[1:], "\nfunc "); end > 0 {
			b = b[:end+1]
		}
		if !strings.Contains(b, "json.Unmarshal") {
			t.Errorf("%s 没走 json.Unmarshal —— 解析人类可读输出跨版本必碎", fn)
		}
	}

	// 备份命令本身必须带 --json，否则 parseSummary 拿到的是进度条
	if !strings.Contains(runner, `[]string{"backup", "--json"}`) {
		t.Error("backup 命令没带 --json —— summary 行根本不会出现")
	}
}
