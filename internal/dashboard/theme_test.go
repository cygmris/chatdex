package dashboard

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 四套主题必须定义**同一组** token。
//
// 少赋一个不会报错，只会让该元素在这套主题下继承到别处的值——
// 通常表现为「某个字在某个主题下几乎看不见」。这类问题肉眼很难普查，
// 所以用测试守住：新增主题时漏了哪个 token，这里会直接指出来。
func TestThemesDefineSameTokens(t *testing.T) {
	themes := parseThemes(t)
	if len(themes) != 4 {
		t.Fatalf("解析出 %d 套主题，want 4：%v", len(themes), themeNames(themes))
	}

	// 以 desk 为基准
	base, ok := themes["desk"]
	if !ok {
		t.Fatal("缺少 desk 主题")
	}
	for name, tokens := range themes {
		for k := range base {
			if _, ok := tokens[k]; !ok {
				t.Errorf("主题 %s 缺少 token %s", name, k)
			}
		}
		for k := range tokens {
			if _, ok := base[k]; !ok {
				t.Errorf("主题 %s 多出 token %s（desk 里没有）", name, k)
			}
		}
	}
}

// 对比度必须脚本算，不能靠「看着还行」。
//
// 正文 4.5:1、次要文字 4.5:1（都是正文尺寸，不适用大字的 3:1 放宽）。
// 不达标就调 token，不是放宽标准。
// ANSI 颜色是**装饰性着色**（给命令输出上色），不承担正文阅读，
// 所以按 WCAG 的非正文标准 3:1 要求，而不是正文的 4.5:1。
// 但必须可辨——终端配色照搬到网页背景上，亮色主题下的「黄」和「白」
// 会直接消失，这个测试就是拦这个的。
func TestANSIColorsAreLegible(t *testing.T) {
	themes := parseThemes(t)
	names := []string{
		"--ansi-black", "--ansi-red", "--ansi-green", "--ansi-yellow",
		"--ansi-blue", "--ansi-magenta", "--ansi-cyan", "--ansi-white",
	}
	var checked int
	for theme, tokens := range themes {
		for _, n := range names {
			fg, ok := parseHex(tokens[n])
			if !ok {
				t.Errorf("主题 %s 缺 %s", theme, n)
				continue
			}
			// 正文区与代码块两种底色都要可辨
			for _, bgName := range []string{"--bg", "--panel-2"} {
				bg, ok := parseHex(tokens[bgName])
				if !ok {
					continue
				}
				if r := contrast(fg, bg); r < 3.0 {
					t.Errorf("主题 %s 的 %s 对 %s 只有 %.2f:1（需 ≥3:1）", theme, n, bgName, r)
				}
				checked++
			}
		}
	}
	// 防止 parseThemes 返回空导致这个测试变成一句空话
	if want := 4 * 8 * 2; checked != want {
		t.Errorf("只校验了 %d 组，期望 %d 组（四套主题 × 8 色 × 2 种底色）", checked, want)
	}
}

func TestThemeContrastMeetsWCAG_AA(t *testing.T) {
	themes := parseThemes(t)

	cases := []struct {
		fg, bg string
		min    float64
		what   string
	}{
		{"--fg", "--bg", 4.5, "正文对背景"},
		{"--fg-dim", "--bg", 4.5, "次要文字对背景"},
		{"--fg", "--panel", 4.5, "正文对侧栏"},
		{"--accent", "--accent-bg", 4.5, "强调色对其底色"},
		{"--hit-fg", "--bg", 4.5, "命中高亮对背景"},
	}

	for name, tokens := range themes {
		for _, c := range cases {
			fg, okf := parseHex(tokens[c.fg])
			bg, okb := parseHex(tokens[c.bg])
			if !okf || !okb {
				// hit-bg 在暗色主题里是 transparent，跳过这类非颜色值
				continue
			}
			if r := contrast(fg, bg); r < c.min {
				t.Errorf("主题 %s 的%s对比度 %.2f:1 < %.1f:1（%s=%s on %s=%s）",
					name, c.what, r, c.min, c.fg, tokens[c.fg], c.bg, tokens[c.bg])
			}
		}
	}
}

// 离线约束：样式表里不得出现任何外部域名。
func TestThemeHasNoExternalResources(t *testing.T) {
	css := readStatic(t, "theme.css")
	for _, bad := range []string{"http://", "https://", "//fonts.", "@import url(//"} {
		if strings.Contains(css, bad) {
			t.Errorf("theme.css 含外部资源引用 %q —— 断网就废了", bad)
		}
	}
	if !strings.Contains(css, `url("/fonts/`) {
		t.Error("字体未走本地 /fonts/ 路径")
	}
}

// ---------- 辅助 ----------

var (
	themeBlockRe = regexp.MustCompile(`\[data-theme="([a-z]+)"\]\s*\{([^}]*)\}`)
	tokenRe      = regexp.MustCompile(`(--[a-z0-9-]+)\s*:\s*([^;]+);`)
	hexRe        = regexp.MustCompile(`^#([0-9a-fA-F]{6})$`)
)

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := assets.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("读 %s: %v", name, err)
	}
	return string(b)
}

func parseThemes(t *testing.T) map[string]map[string]string {
	t.Helper()
	css := readStatic(t, "theme.css")
	out := map[string]map[string]string{}
	for _, m := range themeBlockRe.FindAllStringSubmatch(css, -1) {
		tokens := map[string]string{}
		for _, tm := range tokenRe.FindAllStringSubmatch(m[2], -1) {
			tokens[tm[1]] = strings.TrimSpace(tm[2])
		}
		out[m[1]] = tokens
	}
	return out
}

func themeNames(m map[string]map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func parseHex(v string) ([3]float64, bool) {
	m := hexRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]float64{}, false
	}
	n, err := strconv.ParseUint(m[1], 16, 32)
	if err != nil {
		return [3]float64{}, false
	}
	return [3]float64{
		float64((n>>16)&0xff) / 255,
		float64((n>>8)&0xff) / 255,
		float64(n&0xff) / 255,
	}, true
}

// relativeLuminance 按 WCAG 2.1 定义。
func relativeLuminance(c [3]float64) float64 {
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c[0]) + 0.7152*lin(c[1]) + 0.0722*lin(c[2])
}

func contrast(a, b [3]float64) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// 防止上面那条对比度测试「因为没解析到任何颜色」而空跑通过。
func TestContrastCasesActuallyRan(t *testing.T) {
	themes := parseThemes(t)
	n := 0
	for name, tokens := range themes {
		fg, okf := parseHex(tokens["--fg"])
		bg, okb := parseHex(tokens["--bg"])
		if !okf || !okb {
			t.Fatalf("主题 %s 的 --fg/--bg 没解析成颜色，对比度测试是空跑的", name)
		}
		t.Logf("%-7s 正文 %.2f:1 · 次要 %.2f:1", name, contrast(fg, bg), func() float64 {
			d, _ := parseHex(tokens["--fg-dim"])
			return contrast(d, bg)
		}())
		n++
	}
	if n != 4 {
		t.Fatalf("只跑了 %d 套主题", n)
	}
}

// layout.css 里不得出现硬编码颜色。
//
// 写死一个 #hex，那个元素就会在另外三套主题下失配——而这种失配只在
// 切到那套主题时才看得见，平时开发根本不会碰到。
func TestLayoutUsesTokensOnly(t *testing.T) {
	css := readStatic(t, "layout.css")
	// 允许注释里出现 #hex（说明性文字），只查真实声明
	var offenders []string
	for _, line := range strings.Split(css, "\n") {
		code := line
		if i := strings.Index(code, "/*"); i >= 0 {
			code = code[:i]
		}
		if hardColorRe.MatchString(code) {
			offenders = append(offenders, strings.TrimSpace(line))
		}
	}
	if len(offenders) > 0 {
		t.Errorf("layout.css 出现硬编码颜色（应改用 var(--x)）:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

var hardColorRe = regexp.MustCompile(`(#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|:\s*(red|blue|green|black|white|gray|grey)\b)`)
