// 外部测试包：这样才能 import internal/index（index 依赖 search，同包会成环）。
package search_test

import (
	"fmt"
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
// 源文件消失的会话必须照样搜得到，并且标出 Alive=false。
//
// R1 时这里断言的是相反的行为（「失效会话不再出现」）。那条断言把 alive 读成了
// 「内容还算不算数」——而它只描述文件在不在磁盘上，内容一直都在 blocks 里。
// 3.9% 时看不出差别，57% 时就是半个索引（R26）。
func TestDeadSessionsSearchableAndMarked(t *testing.T) {
	st, e := newEngine(t)
	ids := seedSessions(t, st,
		seed{uid: "gone", project: "/p", blocks: []model.Block{
			{Kind: model.KindUser, Body: "这个会话的文件后来被删了 rsync"},
		}},
		seed{uid: "here", project: "/p", blocks: []model.Block{
			{Kind: model.KindUser, Body: "这个会话的文件还在 rsync"},
		}},
	)
	if err := st.MarkDead("/sessions/gone.jsonl"); err != nil {
		t.Fatal(err)
	}

	alive := func(hits []search.SessionHit) map[int64]bool {
		m := map[int64]bool{}
		for _, h := range hits {
			m[h.ID] = h.Alive
		}
		return m
	}
	check := func(name string, got map[int64]bool) {
		t.Helper()
		if v, ok := got[ids["gone"]]; !ok {
			t.Errorf("%s：源文件已消失的会话不在结果里", name)
		} else if v {
			t.Errorf("%s：已消失的会话 Alive 应为 false", name)
		}
		if v, ok := got[ids["here"]]; !ok || !v {
			t.Errorf("%s：仍存在的会话应出现且 Alive=true，实际 ok=%v v=%v", name, ok, v)
		}
	}

	res, err := e.SearchSessions(search.Query{Text: "rsync"})
	if err != nil {
		t.Fatal(err)
	}
	check("关键词检索", alive(res.Sessions))

	res, err = e.SearchSessions(search.Query{Project: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	check("无关键词列表", alive(res.Sessions))

	blocks, err := e.SearchBlocks(search.Query{Text: "rsync"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, b := range blocks {
		seen[b.SessionID] = true
	}
	if !seen[ids["gone"]] {
		t.Errorf("块检索：已消失会话的块不在结果里")
	}

	ps, err := e.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Sessions != 2 {
		t.Errorf("项目列表应把已消失的会话计入，实际 %+v", ps)
	}
}

// 父子关系不看 alive：父会话源文件没了，子代理照样能找到它；反之亦然。
func TestRelationsIgnoreAlive(t *testing.T) {
	st, e := newEngine(t)
	ids := seedSessions(t, st,
		seed{uid: "main", project: "/p", blocks: repeatBlocks("assistant", "父", 1)},
		seed{uid: "kid", project: "/p", parent: "main", blocks: repeatBlocks("assistant", "子", 1)},
	)
	for _, f := range []string{"/sessions/main.jsonl", "/sessions/kid.jsonl"} {
		if err := st.MarkDead(f); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.Parent(ids["kid"])
	if err != nil || p == nil || p.ID != ids["main"] || p.Alive {
		t.Errorf("父会话源文件消失后仍应返回且 Alive=false，实际 p=%+v err=%v", p, err)
	}
	kids, total, err := e.Children(ids["main"], 0)
	if err != nil || total != 1 || len(kids) != 1 || kids[0].Alive {
		t.Errorf("子代理源文件消失后仍应列出且 Alive=false，实际 total=%d kids=%+v err=%v", total, kids, err)
	}
	v, err := e.GetSession(ids["main"], search.SessionQuery{Limit: 10})
	if err != nil || v.ChildCount != 1 {
		t.Errorf("ChildCount 应计入已消失的子代理，实际 %d err=%v", v.ChildCount, err)
	}
}

func TestGetSessionPaginatesInOrder(t *testing.T) {
	st, e := newEngine(t)
	blocks := make([]model.Block, 30)
	for i := range blocks {
		blocks[i] = model.Block{Kind: model.KindUser, Body: "第 " + string(rune('A'+i%26)) + " 条消息"}
	}
	ids := seedSessions(t, st, seed{uid: "long", project: "/p", blocks: blocks})

	v, err := e.GetSession(ids["long"], search.SessionQuery{Limit: 10})
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

	page2, err := e.GetSession(ids["long"], search.SessionQuery{Offset: 10, Limit: 10})
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
		res, err := e.Timeline(search.Query{Agent: agent})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range res.Groups {
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

// LastSeq 的契约只有一条：**同一会话内最后一个命中块的 seq**。
// 它存在的理由是 best_seq 只保证「最相关」不保证「最后」——两者不等时，
// 你读到的可能是会话里已被后续覆盖的说法。
//
// 这里**刻意不断言 best 与 last 的先后**：那取决于 BM25 打分，是 FTS 的行为不是本项目的契约，
// 断言它等于把测试绑在实现细节上（写这个测试时先后猜「长块排前」「短块排前」，两次都被实测推翻）。
//
// ⚠️ 一个会影响使用的实测性质：多词查询是 AND，订正块若没重复全部查询词就**根本不算命中**，
// LastSeq 也不会指向它。这个提示只在「前后两处都匹配同一查询」时有效。
func TestLastSeqIsTheLastMatchingBlock(t *testing.T) {
	st, e := newEngine(t)

	seedSessions(t, st,
		seed{uid: "twice", project: "/p/a", blocks: []model.Block{
			{Kind: model.KindUser, Body: "查一下出口"},
			{Kind: model.KindToolResult, Body: "判定：住宅 IP"},
			{Kind: model.KindAssistant, Body: "先按这个写文档"},
			{Kind: model.KindToolResult, Body: "订正：住宅 IP 属误判，实测归属机房"},
		}},
		seed{uid: "once", project: "/p/b", blocks: []model.Block{
			{Kind: model.KindUser, Body: "另一个话题"},
			{Kind: model.KindToolResult, Body: "顺带提一句住宅 IP"},
		}},
	)

	res, err := e.SearchSessions(search.Query{Text: "住宅"})
	if err != nil {
		t.Fatal(err)
	}
	byUID := map[string]search.SessionHit{}
	for _, s := range res.Sessions {
		byUID[s.SessionUID] = s
	}

	got, ok := byUID["twice"]
	if !ok {
		t.Fatalf("应命中 twice，实得 %v", res.Sessions)
	}
	if got.LastSeq != 3 {
		t.Fatalf("最后一个命中块是 seq=3，实得 %d", got.LastSeq)
	}
	if got.Hits != 2 {
		t.Fatalf("应有 2 处命中，实得 %d", got.Hits)
	}

	// 只提一次时二者必然相等——MCP 层据此省略该字段，不给零信息量的提示
	only := byUID["once"]
	if only.LastSeq != only.BestSeq {
		t.Fatalf("只提一次时 LastSeq 应等于 BestSeq，实得 last=%d best=%d", only.LastSeq, only.BestSeq)
	}
}

// 命中在块末尾时，片段里的高亮必须出现在前 3 行里——结果卡片只露 3 行（R29，实测 5150/5485）。
// 两条路径都断言：SearchSessions 与 SearchBlocks 各自取片段，少守一条就漏一条。
func TestSnippetHitIsVisibleInFirstLines(t *testing.T) {
	st, e := newEngine(t)
	body := strings.Repeat("前面的铺垫文字", 40) + "\n一行\n两行\n三行\n最后才说到事故这个词"
	seedSessions(t, st, seed{uid: "late", project: "/p", blocks: []model.Block{{Kind: model.KindUser, Body: body}}})
	check := func(name, snip string) {
		t.Helper()
		i := strings.Index(snip, search.HitOpen)
		if i < 0 {
			t.Fatalf("%s：片段里没有高亮：%q", name, snip)
		}
		if lines := strings.Count(snip[:i], "\n") + 1; lines > 3 {
			t.Errorf("%s：高亮在第 %d 行，卡片只露 3 行：%q", name, lines, snip)
		}
	}
	res, err := e.SearchSessions(search.Query{Text: "事故"})
	if err != nil || len(res.Sessions) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	check("SearchSessions", res.Sessions[0].Snippet)
	hits, err := e.SearchBlocks(search.Query{Text: "事故"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	check("SearchBlocks", hits[0].Snippet)
}

// 回读按类型过滤：分页、总数、定位全部按**可见集**算。
//
// 固定语料 8 条：seq 0 user / 1 tool_use / 2 tool_result / 3 assistant / 4 user / 5 tool_use / 6 tool_result / 7 assistant。
// 只看 user+assistant 时可见集是 seq 0,3,4,7。
func seedMixedKinds(t *testing.T, st *index.Store) int64 {
	t.Helper()
	var blocks []model.Block
	for i := 0; i < 2; i++ {
		blocks = append(blocks,
			model.Block{Kind: model.KindUser, Body: "问"},
			model.Block{Kind: model.KindToolUse, ToolName: "Bash", Body: "{}"},
			model.Block{Kind: model.KindToolResult, Body: "结果"},
			model.Block{Kind: model.KindAssistant, Body: "答"},
		)
	}
	return seedSessions(t, st, seed{uid: "mixed", project: "/p", blocks: blocks})["mixed"]
}

func seqs(ms []search.Message) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = m.Seq
	}
	return out
}

func TestGetSessionFiltersByKindAndPagesTheVisibleSet(t *testing.T) {
	st, e := newEngine(t)
	id := seedMixedKinds(t, st)
	conv := []string{"user", "assistant"}

	// 第二页（每页 2 条）= 可见集第 3、4 条 = seq 4、7；不是 seq>=2 那两条
	v, err := e.GetSession(id, search.SessionQuery{Offset: 2, Limit: 2, Kinds: conv})
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(v.Messages); fmt.Sprint(got) != "[4 7]" {
		t.Errorf("过滤后第二页 seq = %v, want [4 7]", got)
	}
	if v.Total != 4 || v.TotalAll != 8 {
		t.Errorf("Total=%d TotalAll=%d, want 4 / 8（Total 是可见集，不是全部）", v.Total, v.TotalAll)
	}
	if v.FromSeq != 2 {
		t.Errorf("FromSeq = %d, want 2（可见集偏移）", v.FromSeq)
	}
	want := map[string]int{"user": 2, "assistant": 2, "tool_use": 2, "tool_result": 2}
	if fmt.Sprint(v.KindCounts) != fmt.Sprint(want) {
		t.Errorf("KindCounts = %v, want %v", v.KindCounts, want)
	}

	// 不认识的类型名不算过滤条件；全不合法 = 全部
	if got := search.ParseKinds(" user,bogus,,assistant,user "); fmt.Sprint(got) != "[user assistant]" {
		t.Errorf("ParseKinds = %v", got)
	}
	if got := search.ParseKinds("bogus"); got != nil {
		t.Errorf("全不合法应得 nil，实际 %v", got)
	}
}

func TestGetSessionAtSeqLandsOnItsVisiblePage(t *testing.T) {
	st, e := newEngine(t)
	id := seedMixedKinds(t, st)
	conv := []string{"user", "assistant"}
	at := func(n int) *int { return &n }

	// seq 7 在可见集里排第 4（下标 3），每页 2 条 → 第二页，页首偏移 2
	v, err := e.GetSession(id, search.SessionQuery{Limit: 2, Kinds: conv, AtSeq: at(7), Align: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(v.Messages); fmt.Sprint(got) != "[4 7]" || v.FromSeq != 2 {
		t.Errorf("定位 seq 7：页 = %v from=%d, want [4 7] / 2", got, v.FromSeq)
	}
	if v.TargetKind != "assistant" {
		t.Errorf("TargetKind = %q, want assistant", v.TargetKind)
	}

	// 命中的那条是被隐藏的类型（seq 5 tool_use）：仍落到它前后可见消息所在的页，并报出它的类型
	v, err = e.GetSession(id, search.SessionQuery{Limit: 2, Kinds: conv, AtSeq: at(5), Align: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(v.Messages); fmt.Sprint(got) != "[4 7]" || v.TargetKind != "tool_use" {
		t.Errorf("定位被隐藏的 seq 5：页 = %v kind=%q, want [4 7] / tool_use", got, v.TargetKind)
	}

	// 不对齐 = 从那条起读（MCP 的 from_seq 语义）；不带 Kinds 时偏移就是 seq
	v, err = e.GetSession(id, search.SessionQuery{Limit: 3, AtSeq: at(3)})
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(v.Messages); fmt.Sprint(got) != "[3 4 5]" || v.FromSeq != 3 {
		t.Errorf("from_seq=3 不带过滤：%v from=%d, want [3 4 5] / 3", got, v.FromSeq)
	}
	// 带 Kinds 的 from_seq：从 seq 4 起的可见消息 = 4, 7
	v, err = e.GetSession(id, search.SessionQuery{Limit: 10, Kinds: conv, AtSeq: at(4)})
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(v.Messages); fmt.Sprint(got) != "[4 7]" {
		t.Errorf("from_seq=4 带过滤：%v, want [4 7]", got)
	}
}
