// 外部测试包：这样才能 import internal/index（index 依赖 search，同包会成环）。
package search_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
)

func newEngine(t *testing.T) (*index.Store, *search.Engine) {
	t.Helper()
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, search.NewEngine(st.DB())
}

type seed struct {
	uid, project, source string
	parent               string // 非空 = 子 agent，指向父会话的 uid
	blocks               []model.Block
}

func seedSessions(t *testing.T, st *index.Store, seeds ...seed) map[string]int64 {
	t.Helper()
	ids := map[string]int64{}
	for _, s := range seeds {
		src := model.Source(s.source)
		if src == "" {
			src = model.SourceClaude
		}
		id, err := st.UpsertSession(model.SessionMeta{
			Source: src, SessionUID: s.uid, ParentUID: s.parent,
			FilePath:    "/sessions/" + s.uid + ".jsonl",
			ProjectPath: s.project, StartedAt: 1000, EndedAt: 2000,
		})
		if err != nil {
			t.Fatal(err)
		}
		for i := range s.blocks {
			s.blocks[i].Seq = i
			if s.blocks[i].TS == 0 {
				s.blocks[i].TS = 1500
			}
		}
		if err := st.AppendBlocks(id, s.blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
		ids[s.uid] = id
	}
	return ids
}

func repeatBlocks(kind model.Kind, text string, n int) []model.Block {
	out := make([]model.Block, n)
	for i := range out {
		out[i] = model.Block{Kind: kind, Body: text}
	}
	return out
}

// ⭐ 本项目存在的理由，也是 R1.2 的验收：
//
// 实测事故——找那段 restic 对话时，关键词命中最多的两个会话（2272 / 2154 次）
// 都不是目标，目标只命中 669 次。按命中数求和排序会原样重建这个 bug。
// 排序必须看会话内**最佳块**的相关度。
func TestSessionRankingIgnoresHitCount(t *testing.T) {
	st, e := newEngine(t)

	// 两个「高频但不相关」的会话：restic 这个词在大量长篇日志里被反复提到，
	// 但没有一处是在讨论它本身。
	noise := "构建日志：正在下载 restic 依赖包，跳过缓存，继续下一步，输出很长很长很长很长的一堆内容"
	// 目标会话：只提了两次，但正是在讨论「基于 restic 做增量备份」这件事。
	target := "基于 restic 做一个类似 TimeMachine 的增量备份工具"

	seedSessions(t, st,
		seed{uid: "noisy-1", project: "/p/a", blocks: repeatBlocks(model.KindToolResult, noise, 60)},
		seed{uid: "noisy-2", project: "/p/a", blocks: repeatBlocks(model.KindToolResult, noise, 45)},
		seed{uid: "target", project: "/p/b", blocks: []model.Block{
			{Kind: model.KindUser, Body: target},
			{Kind: model.KindAssistant, Body: "好，先确认 restic 的快照仓库能不能增量"},
		}},
	)

	res, err := e.SearchSessions(search.Query{Text: "restic"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 3 {
		t.Fatalf("应命中 3 个会话，实得 %d", len(res.Sessions))
	}
	if res.Sessions[0].SessionUID != "target" {
		var got []string
		for _, s := range res.Sessions {
			got = append(got, s.SessionUID)
		}
		t.Fatalf("排序错误：期望 target 排第一，实得 %v", got)
	}
	// 命中数照常展示，但确实是「少的那个」排在前面
	if res.Sessions[0].Hits >= res.Sessions[1].Hits {
		t.Errorf("目标会话命中数 %d 不该 >= 噪声会话 %d —— 那这条测试就没在测排序",
			res.Sessions[0].Hits, res.Sessions[1].Hits)
	}
	// 片段要足以判断「是不是这条」，且不含内部分隔标记
	if !strings.Contains(res.Sessions[0].Snippet, "restic") {
		t.Errorf("片段未含关键词: %q", res.Sessions[0].Snippet)
	}
	if strings.ContainsRune(res.Sessions[0].Snippet, search.Sep) {
		t.Error("片段含内部分隔标记")
	}
}

// 无命中必须明确返回「无」，不得用近似结果冒充（需求 1.6）。
func TestNoMatchIsExplicit(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st, seed{uid: "s1", project: "/p", blocks: []model.Block{
		{Kind: model.KindUser, Body: "部署到 Railway 上"},
	}})

	res, err := e.SearchSessions(search.Query{Text: "完全不存在的关键词xyzzy"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NoMatch || len(res.Sessions) != 0 {
		t.Errorf("应为无命中，实得 %d 条 NoMatch=%v", len(res.Sessions), res.NoMatch)
	}
}

// 中文检索：摘要用概念词重写原文，于是能命中原文没出现过的词（需求 11.2 的机制）。
func TestChineseAndSummaryHit(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st, seed{uid: "s1", project: "/p", blocks: []model.Block{
		{Kind: model.KindUser, Body: "做一个类似 TimeMachine 的管理工具"},
		{Kind: model.KindSummary, Body: "讨论基于 restic 做增量备份工具"},
	}})

	// 原文里「增量备份」一次未出现，只在摘要里有
	res, err := e.SearchSessions(search.Query{Text: "增量备份"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].BestKind != string(model.KindSummary) {
		t.Errorf("摘要未命中: %+v", res.Sessions)
	}
}

func TestFiltersCombineWithAnd(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st,
		seed{uid: "claude-a", project: "/proj/alpha", source: "claude", blocks: []model.Block{
			{Kind: model.KindToolUse, ToolName: "Bash", Body: `{"command":"rsync -av /src /dst"}`, TS: 1000},
		}},
		seed{uid: "codex-b", project: "/proj/beta", source: "codex", blocks: []model.Block{
			{Kind: model.KindToolUse, ToolName: "exec", Body: `{"command":"rsync -av /a /b"}`, TS: 5000},
		}},
	)

	cases := []struct {
		name string
		q    search.Query
		want string // 期望命中的唯一会话；"" 表示应无命中
	}{
		{"按来源", search.Query{Text: "rsync", Source: "codex"}, "codex-b"},
		{"按项目", search.Query{Text: "rsync", Project: "/proj/alpha"}, "claude-a"},
		{"按工具名", search.Query{Text: "rsync", ToolName: "Bash"}, "claude-a"},
		{"按内容类型", search.Query{Text: "rsync", Kinds: []string{"tool_use"}}, ""},
		{"按时间", search.Query{Text: "rsync", From: 4000}, "codex-b"},
		{"矛盾条件 AND 后为空", search.Query{Text: "rsync", Source: "codex", Project: "/proj/alpha"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := e.SearchSessions(c.q)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if c.name == "按内容类型" {
					// 两条都是 tool_use，应各自命中
					if len(res.Sessions) != 2 {
						t.Errorf("应命中 2 条，实得 %d", len(res.Sessions))
					}
					return
				}
				if len(res.Sessions) != 0 {
					t.Errorf("应无命中，实得 %d 条", len(res.Sessions))
				}
				return
			}
			if len(res.Sessions) != 1 || res.Sessions[0].SessionUID != c.want {
				t.Errorf("期望只命中 %s，实得 %+v", c.want, res.Sessions)
			}
		})
	}
}

// 子目录下产生的会话也算「该项目目录下」。
func TestProjectFilterMatchesSubdirectories(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st, seed{uid: "sub", project: "/proj/alpha/services/api", blocks: []model.Block{
		{Kind: model.KindUser, Body: "在子目录里跑 rsync"},
	}})
	res, err := e.SearchSessions(search.Query{Text: "rsync", Project: "/proj/alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Errorf("子目录会话未被项目过滤命中: %+v", res.Sessions)
	}
}

// 失效会话（原始文件已删）不得出现在检索结果里（需求 5.4）。
func TestDeadSessionsExcluded(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st, seed{uid: "gone", project: "/p", blocks: []model.Block{
		{Kind: model.KindUser, Body: "这个会话的文件后来被删了 rsync"},
	}})
	if err := st.MarkDead("/sessions/gone.jsonl"); err != nil {
		t.Fatal(err)
	}
	res, err := e.SearchSessions(search.Query{Text: "rsync"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 0 {
		t.Errorf("失效会话仍出现在结果里: %+v", res.Sessions)
	}
}

func TestGetSessionPaginatesInOrder(t *testing.T) {
	st, e := newEngine(t)
	blocks := make([]model.Block, 30)
	for i := range blocks {
		blocks[i] = model.Block{Kind: model.KindUser, Body: "第 " + string(rune('A'+i%26)) + " 条消息"}
	}
	ids := seedSessions(t, st, seed{uid: "long", project: "/p", blocks: blocks})

	v, err := e.GetSession(ids["long"], 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if v.Total != 30 {
		t.Errorf("Total = %d, want 30", v.Total)
	}
	if len(v.Messages) != 10 {
		t.Errorf("首页返回 %d 条, want 10", len(v.Messages))
	}
	if v.Messages[0].Seq != 0 || v.Messages[9].Seq != 9 {
		t.Error("消息未按 seq 正序返回")
	}
	if v.FilePath == "" {
		t.Error("未给出原始文件绝对路径（需求 3.4）")
	}

	page2, err := e.GetSession(ids["long"], 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page2.Messages[0].Seq != 10 {
		t.Errorf("第二页首条 Seq = %d, want 10", page2.Messages[0].Seq)
	}
	// 回读的正文必须是可展示的原文，不含内部标记
	for _, m := range page2.Messages {
		if strings.ContainsRune(m.Body, search.Sep) {
			t.Fatal("回读正文含内部分隔标记")
		}
	}
}

func TestBuildMatchEscapesSpecialChars(t *testing.T) {
	// FTS5 的特殊字符必须被引号包成字面量，否则查询会语法错误或语义跑偏
	for _, q := range []string{`rsync -av`, `foo*`, `a"b`, `NEAR(x y)`, `col:val`} {
		st, e := newEngine(t)
		seedSessions(t, st, seed{uid: "s", project: "/p", blocks: []model.Block{
			{Kind: model.KindUser, Body: "普通内容"},
		}})
		if _, err := e.SearchSessions(search.Query{Text: q}); err != nil {
			t.Errorf("查询 %q 报错: %v", q, err)
		}
	}
}

func TestProjectsAggregation(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st,
		seed{uid: "a1", project: "/proj/alpha", blocks: []model.Block{{Kind: model.KindUser, Body: "x"}}},
		seed{uid: "a2", project: "/proj/alpha", blocks: []model.Block{{Kind: model.KindUser, Body: "y"}}},
		seed{uid: "b1", project: "/proj/beta", blocks: []model.Block{{Kind: model.KindUser, Body: "z"}}},
	)
	ps, err := e.Projects()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, p := range ps {
		got[p.ProjectPath] = p.Sessions
	}
	if got["/proj/alpha"] != 2 || got["/proj/beta"] != 1 {
		t.Errorf("项目聚合 = %v", got)
	}
}

// 会话名不进 FTS（需求 4.1）。
//
// 它只有 2% 覆盖率，为它动索引结构（重建 FTS 表、改三个触发器）代价远大于收益。
// 这条断言防的是「顺手也加进去吧」——那个改动一旦做了就很难退回来。
func TestTitleIsNotFullTextIndexed(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st, seed{uid: "s1", project: "/p", blocks: []model.Block{
		{Kind: model.KindUser, Body: "正文里完全没有那个词"},
	}})
	var id int64
	if err := st.DB().QueryRow(`SELECT id FROM sessions LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	const title = "独一无二的会话名XYZ"
	if err := st.SetTitle(id, title); err != nil {
		t.Fatal(err)
	}

	res, err := e.SearchSessions(search.Query{Text: title})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 0 {
		t.Errorf("按会话名搜到了 %d 条 —— 标题不该进 FTS（需求 4.1）", len(res.Sessions))
	}

	// 但它必须能随会话一起被读出来
	got, err := e.SearchSessions(search.Query{Text: "正文里"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Title != title {
		t.Errorf("会话名没随结果返回：%+v", got.Sessions)
	}
}

// 主会话 / 子 agent 三态过滤。
//
// 断言写成**互补性**而不是逐条点名：main 与 sub 必须无交集，且并集等于不过滤时的
// 全集。这条能咬住"两个分支写反了"——那种错误下逐条断言仍然可能各自通过
// （每边都返回了非空且形态正确的结果），只有互补性会立刻崩。
func TestAgentFilterPartitionsResults(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st,
		seed{uid: "m1", project: "/p", blocks: repeatBlocks("assistant", "限流 中间件", 1)},
		seed{uid: "m2", project: "/p", blocks: repeatBlocks("assistant", "限流 网关", 1)},
		seed{uid: "s1", project: "/p", parent: "m1", blocks: repeatBlocks("assistant", "限流 令牌桶", 1)},
		seed{uid: "s2", project: "/p", parent: "m1", blocks: repeatBlocks("assistant", "限流 滑动窗口", 1)},
		seed{uid: "s3", project: "/p", parent: "m2", blocks: repeatBlocks("assistant", "限流 压测", 1)},
	)

	// text 为空时 SearchSessions 走的是 recentSessions 那条分支——检索页首屏
	// 正是这个状态。两条分支都要验，只验带关键词的会漏掉最常见的那个。
	for _, text := range []string{"限流", ""} {
		assertPartition(t, e, text)
	}
}

func assertPartition(t *testing.T, e *search.Engine, text string) {
	t.Helper()
	ids := func(agent string) map[int64]bool {
		res, err := e.SearchSessions(search.Query{Text: text, Agent: agent})
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]bool{}
		for _, h := range res.Sessions {
			out[h.ID] = true
			// 顺带验 IsSub 与所选分支自洽
			switch agent {
			case "main":
				if h.IsSub {
					t.Errorf("agent=main 的结果里出现了子 agent：%d", h.ID)
				}
			case "sub":
				if !h.IsSub {
					t.Errorf("agent=sub 的结果里出现了主会话：%d", h.ID)
				}
			}
		}
		return out
	}

	all, main, sub := ids(""), ids("main"), ids("sub")
	if len(all) != 5 || len(main) != 2 || len(sub) != 3 {
		t.Fatalf("text=%q 条数不对：全部 %d（应 5）主 %d（应 2）子 %d（应 3）",
			text, len(all), len(main), len(sub))
	}
	for id := range main {
		if sub[id] {
			t.Errorf("会话 %d 同时出现在 main 与 sub 里", id)
		}
	}
	for id := range all {
		if !main[id] && !sub[id] {
			t.Errorf("会话 %d 不在 main 也不在 sub —— 两者的并集不等于全集", id)
		}
	}
}

// 认不出来的取值当「全部」，不报错也不返回空——与主题名、高亮配色名同一条降级纪律。
func TestUnknownAgentValueFallsBackToAll(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st,
		seed{uid: "m1", project: "/p", blocks: repeatBlocks("assistant", "限流", 1)},
		seed{uid: "s1", project: "/p", parent: "m1", blocks: repeatBlocks("assistant", "限流", 1)},
	)
	for _, bad := range []string{"MAIN", "subagent", "true", "1", "'"} {
		res, err := e.SearchSessions(search.Query{Text: "限流", Agent: bad})
		if err != nil {
			t.Fatalf("agent=%q 报错了：%v", bad, err)
		}
		if len(res.Sessions) != 2 {
			t.Errorf("agent=%q 返回 %d 条，应当等同于「全部」的 2 条", bad, len(res.Sessions))
		}
	}
}

// Codex 侧没有子 agent，选「仅子 agent」应当正常返回空而不是报错（需求 1.5）。
func TestSubFilterWithCodexSourceReturnsEmpty(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st,
		seed{uid: "c1", project: "/p", source: "codex", blocks: repeatBlocks("assistant", "限流", 1)},
		seed{uid: "m1", project: "/p", blocks: repeatBlocks("assistant", "限流", 1)},
		seed{uid: "s1", project: "/p", parent: "m1", blocks: repeatBlocks("assistant", "限流", 1)},
	)
	res, err := e.SearchSessions(search.Query{Text: "限流", Source: "codex", Agent: "sub"})
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if len(res.Sessions) != 0 {
		t.Errorf("codex + 仅子 agent 应当为空，实际 %d 条", len(res.Sessions))
	}
}

// 时间线与检索共用同一处 agent 过滤片段（agentFilter），不能只有一边生效。
func TestTimelineHonorsAgentFilter(t *testing.T) {
	st, e := newEngine(t)
	seedSessions(t, st,
		seed{uid: "m1", project: "/p", blocks: repeatBlocks("assistant", "限流", 1)},
		seed{uid: "s1", project: "/p", parent: "m1", blocks: repeatBlocks("assistant", "限流", 1)},
		seed{uid: "s2", project: "/p", parent: "m1", blocks: repeatBlocks("assistant", "限流", 1)},
	)
	count := func(agent string) (n int, subs int) {
		gs, err := e.Timeline(search.Query{Agent: agent})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range gs {
			for _, s := range g.Sessions {
				n++
				if s.IsSub {
					subs++
				}
			}
		}
		return
	}
	if n, _ := count(""); n != 3 {
		t.Errorf("时间线不过滤应有 3 条，实际 %d", n)
	}
	if n, subs := count("main"); n != 1 || subs != 0 {
		t.Errorf("时间线 agent=main 应有 1 条主会话，实际 %d 条其中 %d 个子 agent", n, subs)
	}
	if n, subs := count("sub"); n != 2 || subs != 2 {
		t.Errorf("时间线 agent=sub 应有 2 条子 agent，实际 %d 条其中 %d 个子 agent", n, subs)
	}
}
