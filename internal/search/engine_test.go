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
			Source: src, SessionUID: s.uid, FilePath: "/sessions/" + s.uid + ".jsonl",
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
			{Kind: model.KindAssistant, Body: "好，先确认 restic 的 profile 持久化能力"},
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
		{Kind: model.KindUser, Body: "做一个类似 timemachine 的管理工具"},
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
