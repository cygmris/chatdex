package search_test

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
)

func seedTimeline(t *testing.T) (*index.Store, *search.Engine) {
	t.Helper()
	st, e := newEngine(t)

	add := func(uid, project, source string, started, ended int64, firstUser string, summary string) int64 {
		id, err := st.UpsertSession(model.SessionMeta{
			Source: model.Source(source), SessionUID: uid, FilePath: "/s/" + uid + ".jsonl",
			ProjectPath: project, StartedAt: started, EndedAt: ended,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendBlocks(id, []model.Block{
			{Seq: 0, TS: started, Kind: model.KindUser, Body: firstUser},
			{Seq: 1, TS: ended, Kind: model.KindAssistant, Body: "回答"},
		}, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
		if summary != "" {
			if err := st.SetSummary(id, summary, "m", 2); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}

	add("a1", "/proj/alpha", "claude", 3000, 3500, "在 alpha 项目里改了检索排序", "alpha：修复检索排序")
	add("a2", "/proj/alpha", "codex", 2000, 2500, "在 alpha 项目里加了缓存", "")
	add("b1", "/proj/beta", "claude", 1000, 1500, "在 beta 项目里配置部署", "")
	return st, e
}

func TestTimelineGroupsByProjectNewestFirst(t *testing.T) {
	_, e := seedTimeline(t)

	res, err := e.Timeline(search.Query{})
	if err != nil {
		t.Fatal(err)
	}
	gs := res.Groups
	if len(gs) != 2 {
		t.Fatalf("项目数 = %d, want 2", len(gs))
	}
	if gs[0].ProjectPath != "/proj/alpha" {
		t.Errorf("最近活动的项目应排前: %+v", gs[0])
	}
	if gs[0].Total != 2 || len(gs[0].Sessions) != 2 {
		t.Errorf("alpha 会话数 = %d/%d", len(gs[0].Sessions), gs[0].Total)
	}
	// 组内也按时间倒序
	if gs[0].Sessions[0].StartedAt < gs[0].Sessions[1].StartedAt {
		t.Error("组内未按时间倒序")
	}
}

// 需求 9.2：条目要有可辨认文字——有摘要用摘要，没有退回首条用户消息。
func TestTimelineLabelFallsBackToFirstUserMessage(t *testing.T) {
	_, e := seedTimeline(t)
	res, err := e.Timeline(search.Query{Project: "/proj/alpha"})
	if err != nil {
		t.Fatal(err)
	}
	gs := res.Groups

	var withSummary, without search.TimelineSession
	for _, s := range gs[0].Sessions {
		if s.HasSummary {
			withSummary = s
		} else {
			without = s
		}
	}
	if withSummary.Label != "alpha：修复检索排序" {
		t.Errorf("有摘要的条目应用摘要: %q", withSummary.Label)
	}
	if !strings.Contains(without.Label, "加了缓存") {
		t.Errorf("无摘要的条目应退回首条用户消息: %q", without.Label)
	}
	if strings.ContainsRune(without.Label, search.Sep) {
		t.Error("辨识文字含内部分隔标记")
	}
}

// 需求 9.4：过滤条件与检索共用。
func TestTimelineSharesFilters(t *testing.T) {
	_, e := seedTimeline(t)

	cases := []struct {
		name string
		q    search.Query
		want int // 期望的项目组数
	}{
		{"按项目", search.Query{Project: "/proj/beta"}, 1},
		{"按来源", search.Query{Source: "codex"}, 1},
		{"按时间：只要最近的", search.Query{From: 2800}, 1},
		{"按时间：区间外", search.Query{From: 9000}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := e.Timeline(c.q)
			if err != nil {
				t.Fatal(err)
			}
			gs := res.Groups
			if len(gs) != c.want {
				t.Errorf("项目组数 = %d, want %d: %+v", len(gs), c.want, gs)
			}
		})
	}
}

// 日期过滤说的是「该范围内有消息」，不是 session 的起止区间刚好跨过它。
// 长会话可能持续数月；若只比较 sessions.started_at / ended_at，会把中间完全
// 没有活动的日期也算进去，而且点开后没有一个可信的消息落点。
func TestTimelineDateFilterUsesMessageActivityAndTargetSeq(t *testing.T) {
	st, e := newEngine(t)

	add := func(uid string, blocks []model.Block) int64 {
		id, err := st.UpsertSession(model.SessionMeta{
			Source: model.SourceClaude, SessionUID: uid, FilePath: "/s/" + uid + ".jsonl",
			ProjectPath: "/proj/long", StartedAt: 100, EndedAt: 500,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendBlocks(id, blocks,
			index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
		return id
	}

	activeID := add("active", []model.Block{
		{Seq: 0, TS: 100, Kind: model.KindUser, Body: "开始"},
		{Seq: 1, TS: 300, Kind: model.KindAssistant, Body: "范围内活动"},
		{Seq: 2, TS: 500, Kind: model.KindUser, Body: "结束"},
	})
	add("gap", []model.Block{
		{Seq: 0, TS: 100, Kind: model.KindUser, Body: "开始"},
		{Seq: 1, TS: 500, Kind: model.KindAssistant, Body: "结束"},
	})

	res, err := e.Timeline(search.Query{From: 250, To: 350})
	if err != nil {
		t.Fatal(err)
	}
	gs := res.Groups
	if len(gs) != 1 || len(gs[0].Sessions) != 1 {
		t.Fatalf("日期范围内应只有真实有消息的会话: %+v", gs)
	}
	got := gs[0].Sessions[0]
	if got.ID != activeID {
		t.Fatalf("返回会话 id = %d, want %d", got.ID, activeID)
	}
	if got.TargetSeq != 1 {
		t.Errorf("TargetSeq = %d, want 1", got.TargetSeq)
	}

	all, err := e.Timeline(search.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all.Groups[0].Sessions {
		if s.TargetSeq != 0 {
			t.Errorf("未设置日期过滤时 TargetSeq = %d, want 0", s.TargetSeq)
		}
	}
}

// 需求 9.5：项目会话很多时不得一次性全返回。
func TestTimelineCollapsesLargeProject(t *testing.T) {
	st, e := newEngine(t)
	for i := range 40 {
		id, err := st.UpsertSession(model.SessionMeta{
			Source: model.SourceClaude, SessionUID: string(rune('a'+i%26)) + string(rune('0'+i/26)),
			FilePath:    "/s/big" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".jsonl",
			ProjectPath: "/proj/big", StartedAt: int64(1000 + i), EndedAt: int64(1100 + i),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendBlocks(id, []model.Block{
			{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "内容"},
		}, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := e.Timeline(search.Query{})
	if err != nil {
		t.Fatal(err)
	}
	gs := res.Groups
	if len(gs) != 1 {
		t.Fatalf("项目组数 = %d", len(gs))
	}
	if gs[0].Total != 40 {
		t.Errorf("Total = %d, want 40（总数要如实报）", gs[0].Total)
	}
	if len(gs[0].Sessions) >= 40 {
		t.Errorf("展开了 %d 条，应折叠", len(gs[0].Sessions))
	}
}

// 失效会话不出现在时间线上。
func TestTimelineExcludesDeadSessions(t *testing.T) {
	st, e := seedTimeline(t)
	if err := st.MarkDead("/s/b1.jsonl"); err != nil {
		t.Fatal(err)
	}
	res, err := e.Timeline(search.Query{Project: "/proj/beta"})
	if err != nil {
		t.Fatal(err)
	}
	gs := res.Groups
	if len(gs) != 0 {
		t.Errorf("失效会话仍在时间线上: %+v", gs)
	}
}

// seedMany 在一个项目下造 n 个会话，起始时间递增（越晚越新）。
func seedMany(t *testing.T, st *index.Store, project, prefix string, n int, base int64) {
	t.Helper()
	for i := range n {
		uid := prefix + strconv.Itoa(i)
		id, err := st.UpsertSession(model.SessionMeta{
			Source: model.SourceClaude, SessionUID: uid, FilePath: "/s/" + uid + ".jsonl",
			ProjectPath: project, StartedAt: base + int64(i), EndedAt: base + int64(i) + 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendBlocks(id, []model.Block{
			{Seq: 0, TS: base + int64(i), Kind: model.KindUser, Body: "内容"},
		}, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

// Total 必须是真实总数，且**旧的分页边界之外的项目不得消失**。
//
// 🔴 取样必须跨过那条边界，这是本条断言存在的全部理由。已有的
// TestTimelineCollapsesLargeProject 造 40 个会话断言 Total==40，**它在修复前
// 就是绿的**——40 条不到旧实现的全局 LIMIT 200，于是「数页内条数」与
// 「数真实总数」给出同一个答案。取样比命题窄，bug 就是这么活下来的。
//
// 这里造 210 + 5：旧实现取最新 200 条会话，old 项目一条都进不去（整个消失），
// big 项目的 Total 会停在 200。实测真机上正是这个形状——118 个项目只看得到 6 个。
func TestTimelineTotalIsRealCountBeyondPageBoundary(t *testing.T) {
	st, e := newEngine(t)
	seedMany(t, st, "/proj/old", "old", 5, 1000)   // 更早
	seedMany(t, st, "/proj/big", "big", 210, 5000) // 更新，足以在旧实现里独占整页

	res, err := e.Timeline(search.Query{})
	if err != nil {
		t.Fatal(err)
	}
	gs := res.Groups
	got := map[string]*search.ProjectGroup{}
	for i := range gs {
		got[gs[i].ProjectPath] = &gs[i]
	}
	big, ok := got["/proj/big"]
	if !ok {
		t.Fatal("/proj/big 不在结果里")
	}
	if big.Total != 210 {
		t.Errorf("big.Total = %d, want 210 —— 这是分页前的真实总数，不是页内条数", big.Total)
	}
	if len(big.Sessions) > 210 {
		t.Errorf("展开了 %d 条", len(big.Sessions))
	}
	// 这一条才是「112 个项目消失」的直接对照：old 比 big 旧，
	// 在按会话分页的旧实现里连一条都挤不进最新 200 条。
	old, ok := got["/proj/old"]
	if !ok {
		t.Fatal("/proj/old 整个消失了 —— 按会话分页会把靠后的项目挤掉，这正是本期要修的")
	}
	if old.Total != 5 {
		t.Errorf("old.Total = %d, want 5", old.Total)
	}
}

// 分页作用在项目上：翻页不重不漏，且每页里每个项目都是完整的一组。
func TestTimelinePaginatesOverProjectsNotSessions(t *testing.T) {
	st, e := newEngine(t)
	for i := range 5 {
		seedMany(t, st, "/proj/p"+strconv.Itoa(i), "p"+strconv.Itoa(i)+"_", 3, int64(1000+i*100))
	}

	seen := map[string]int{}
	for off := 0; off < 5; off += 2 {
		res, err := e.Timeline(search.Query{Limit: 2, Offset: off})
		if err != nil {
			t.Fatal(err)
		}
		gs := res.Groups
		if off < 4 && len(gs) != 2 {
			t.Fatalf("offset=%d 返回 %d 个项目，want 2", off, len(gs))
		}
		for _, g := range gs {
			seen[g.ProjectPath]++
			// 每个项目在它出现的那一页里是完整的：Total 是真实值，不因分页而变
			if g.Total != 3 {
				t.Errorf("%s Total = %d, want 3", g.ProjectPath, g.Total)
			}
		}
	}
	if len(seen) != 5 {
		t.Errorf("翻完只见到 %d 个项目，want 5（有遗漏）", len(seen))
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("%s 出现了 %d 次（分页重复）", p, n)
		}
	}
}

// 项目总数与本页返回了几个无关，且翻过头不能把它也弄没。
//
// 「翻过头返回空」与「筛完就是没有」在 groups 上同形；ProjectTotal 是唯一
// 能把两者区分开的信号，所以它在越界那一页也必须是真值。
func TestTimelineProjectTotalIsIndependentOfPage(t *testing.T) {
	st, e := newEngine(t)
	for i := range 5 {
		seedMany(t, st, "/proj/q"+strconv.Itoa(i), "q"+strconv.Itoa(i)+"_", 2, int64(1000+i*100))
	}

	for _, tc := range []struct {
		name       string
		offset     int
		wantGroups int
	}{
		{"第一页", 0, 2},
		{"中间页", 2, 2},
		{"最后一页只剩一个", 4, 1},
		{"翻过头", 10, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := e.Timeline(search.Query{Limit: 2, Offset: tc.offset})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Groups) != tc.wantGroups {
				t.Errorf("groups = %d, want %d", len(res.Groups), tc.wantGroups)
			}
			if res.ProjectTotal != 5 {
				t.Errorf("ProjectTotal = %d, want 5 —— 它不该随分页变", res.ProjectTotal)
			}
			if res.Offset != tc.offset || res.Limit != 2 {
				t.Errorf("回显的分页参数不对：offset=%d limit=%d", res.Offset, res.Limit)
			}
		})
	}
}

// 项目总数要跟着过滤条件走，不是全库常数。
//
// 阳性对照式的第二半：只断言「过滤后 ProjectTotal 变小」会被
// 「ProjectTotal 恒等于 groups 长度」骗过，所以同时钉住过滤前的值。
func TestTimelineProjectTotalRespectsFilters(t *testing.T) {
	st, e := newEngine(t)
	seedMany(t, st, "/proj/keep", "keep", 2, 1000)
	seedMany(t, st, "/proj/drop", "drop", 2, 2000)

	all, err := e.Timeline(search.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if all.ProjectTotal != 2 {
		t.Fatalf("未过滤时 ProjectTotal = %d, want 2", all.ProjectTotal)
	}
	one, err := e.Timeline(search.Query{Project: "/proj/keep"})
	if err != nil {
		t.Fatal(err)
	}
	if one.ProjectTotal != 1 {
		t.Errorf("按项目过滤后 ProjectTotal = %d, want 1 —— 它必须跟着过滤条件走", one.ProjectTotal)
	}
}

// seedFilterFixture 造三个项目，内容/块类型/工具名两两可区分。
func seedFilterFixture(t *testing.T) *search.Engine {
	t.Helper()
	st, e := newEngine(t)
	add := func(uid, project string, blocks []model.Block) {
		id, err := st.UpsertSession(model.SessionMeta{
			Source: model.SourceClaude, SessionUID: uid, FilePath: "/s/" + uid + ".jsonl",
			ProjectPath: project, StartedAt: 1000, EndedAt: 2000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	add("s_restic", "/proj/restic", []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "帮我配置 restic 增量备份"},
		{Seq: 1, TS: 1100, Kind: model.KindToolUse, ToolName: "Bash", Body: "restic backup"},
	})
	add("s_nginx", "/proj/nginx", []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "nginx 反向代理怎么配"},
		{Seq: 1, TS: 1100, Kind: model.KindToolUse, ToolName: "Edit", Body: "nginx.conf"},
	})
	add("s_plain", "/proj/plain", []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "随便聊聊"},
	})
	return e
}

func projectsOf(res search.TimelineResult) []string {
	out := make([]string, 0, len(res.Groups))
	for _, g := range res.Groups {
		out = append(out, g.ProjectPath)
	}
	sort.Strings(out)
	return out
}

// q / kind / tool 必须真的筛掉东西 —— 每条都配阳性对照。
//
// 🔴 **只断言「筛完变少了」会被「筛完永远为空」骗过**，而后者恰恰是另一种坏法。
// 所以每个条件都成对：一个必然命中的取值 + 一个必然不命中的取值，
// 断言两者结果**不同**且各自等于预期集合。
//
// 这三条此前被静默忽略：实测 q=<不可能的词> 与无筛选的响应逐字节相同。
func TestTimelineFiltersActuallyApply(t *testing.T) {
	e := seedFilterFixture(t)

	must := func(q search.Query) search.TimelineResult {
		t.Helper()
		res, err := e.Timeline(q)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	base := must(search.Query{})
	if got := projectsOf(base); len(got) != 3 {
		t.Fatalf("未过滤时应有 3 个项目，实际 %v", got)
	}

	for _, tc := range []struct {
		name string
		q    search.Query
		want []string
	}{
		{"q 命中一个", search.Query{Text: "restic"}, []string{"/proj/restic"}},
		{"q 命中另一个（阳性对照：换个词结果要变）", search.Query{Text: "nginx"}, []string{"/proj/nginx"}},
		{"q 一个都不命中", search.Query{Text: "zzz压根不存在的词zzz"}, []string{}},
		{"tool 命中一个", search.Query{ToolName: "Bash"}, []string{"/proj/restic"}},
		{"tool 命中另一个（阳性对照）", search.Query{ToolName: "Edit"}, []string{"/proj/nginx"}},
		{"tool 一个都不命中", search.Query{ToolName: "NoSuchTool"}, []string{}},
		{"kind 只要有工具调用的", search.Query{Kinds: []string{"tool_use"}}, []string{"/proj/nginx", "/proj/restic"}},
		{"kind 换一种（阳性对照：user 三个都有）", search.Query{Kinds: []string{"user"}},
			[]string{"/proj/nginx", "/proj/plain", "/proj/restic"}},
		{"kind 一个都不命中", search.Query{Kinds: []string{"summary"}}, []string{}},
		{"q 与 tool 同时开是 AND", search.Query{Text: "restic", ToolName: "Edit"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := must(tc.q)
			got := projectsOf(res)
			if len(got) != len(tc.want) {
				t.Fatalf("项目 = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("项目 = %v, want %v", got, tc.want)
				}
			}
			// ProjectTotal 也必须跟着筛，否则界面上会说「共 3 个项目」而只列出 1 个
			if res.ProjectTotal != len(tc.want) {
				t.Errorf("ProjectTotal = %d, want %d", res.ProjectTotal, len(tc.want))
			}
		})
	}
}

// 时间线的关键词匹配必须与检索走同一套规则。
//
// 另写一套的代价不是「不一致」这么抽象：使用者会以为那是同一个搜索，
// 于是「检索里搜得到、时间线里搜不到」会被当成时间线漏了会话。
func TestTimelineKeywordAgreesWithSearch(t *testing.T) {
	e := seedFilterFixture(t)
	for _, word := range []string{"restic", "nginx", "备份"} {
		tl, err := e.Timeline(search.Query{Text: word})
		if err != nil {
			t.Fatal(err)
		}
		hits, err := e.SearchSessions(search.Query{Text: word})
		if err != nil {
			t.Fatal(err)
		}
		inSearch := map[string]bool{}
		for _, h := range hits.Sessions {
			inSearch[h.ProjectPath] = true
		}
		for _, p := range projectsOf(tl) {
			if !inSearch[p] {
				t.Errorf("词 %q：时间线列出了 %s，而检索没有 —— 两边用了不同的匹配规则", word, p)
			}
		}
		if len(projectsOf(tl)) != len(inSearch) {
			t.Errorf("词 %q：时间线 %d 个项目、检索 %d 个", word, len(projectsOf(tl)), len(inSearch))
		}
	}
}
