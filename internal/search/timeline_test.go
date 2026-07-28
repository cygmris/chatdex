package search_test

import (
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

	gs, err := e.Timeline(search.Query{})
	if err != nil {
		t.Fatal(err)
	}
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
	gs, err := e.Timeline(search.Query{Project: "/proj/alpha"})
	if err != nil {
		t.Fatal(err)
	}

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
			gs, err := e.Timeline(c.q)
			if err != nil {
				t.Fatal(err)
			}
			if len(gs) != c.want {
				t.Errorf("项目组数 = %d, want %d: %+v", len(gs), c.want, gs)
			}
		})
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

	gs, err := e.Timeline(search.Query{})
	if err != nil {
		t.Fatal(err)
	}
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
	gs, err := e.Timeline(search.Query{Project: "/proj/beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 0 {
		t.Errorf("失效会话仍在时间线上: %+v", gs)
	}
}
