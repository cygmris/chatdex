package search_test

import (
	"testing"

	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
)

// 近义词扩展接入检索（spec query-synonym-expansion 需求 1、3）。

func synEngine(t *testing.T, table string) (*search.Engine, map[string]int64) {
	t.Helper()
	st, e := newEngine(t)
	syn := search.ParseSynonyms(table)
	e.Syn = func() *search.Synonyms { return syn }
	ids := seedSessions(t, st,
		seed{uid: "literal", project: "/p", blocks: []model.Block{
			{Kind: model.KindAssistant, Body: "镜像 tag 就是 hash 本身"},
		}},
		seed{uid: "synonym", project: "/p", blocks: []model.Block{
			{Kind: model.KindAssistant, Body: "徽章里显示前端短提交号"},
		}},
		seed{uid: "unrelated", project: "/p", blocks: []model.Block{
			{Kind: model.KindAssistant, Body: "完全无关的内容"},
		}},
	)
	return e, ids
}

func hitByID(res search.Result) map[int64]search.SessionHit {
	m := map[int64]search.SessionHit{}
	for _, h := range res.Sessions {
		m[h.ID] = h
	}
	return m
}

func TestSearchExpandsSynonymsAndMarksVia(t *testing.T) {
	e, ids := synEngine(t, "hash 提交号 SHA")
	res, err := e.SearchSessions(search.Query{Text: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	got := hitByID(res)
	if len(got) != 2 {
		t.Fatalf("应命中原词与近义词两个会话，得到 %d 个", len(got))
	}
	if h, ok := got[ids["literal"]]; !ok || h.ViaSynonym {
		t.Errorf("原词命中的会话不该标近义词：%+v", h)
	}
	if h, ok := got[ids["synonym"]]; !ok || !h.ViaSynonym {
		t.Errorf("只含「提交号」的会话应标近义词命中：%+v", h)
	}
	if len(res.Expanded) != 1 || res.Expanded[0].Term != "hash" || len(res.Expanded[0].Also) != 2 {
		t.Errorf("扩展清单 = %+v", res.Expanded)
	}
}

func TestSearchWithoutSynonymsUnchanged(t *testing.T) {
	e, ids := synEngine(t, "")
	res, err := e.SearchSessions(search.Query{Text: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].ID != ids["literal"] || res.Sessions[0].ViaSynonym {
		t.Fatalf("无近义词时应只有原词命中、且不标注：%+v", res.Sessions)
	}
	if res.Expanded != nil {
		t.Errorf("无扩展时 expanded 应为空：%+v", res.Expanded)
	}
}

// 多词：扩展只发生在词内部，词间仍是 AND。
// 这条同时守着「词间必须是显式 AND」——括号组后接隐式 AND 在 FTS5 里是语法错误。
func TestSearchMultiTermStillAnd(t *testing.T) {
	e, ids := synEngine(t, "hash 提交号 SHA")
	res, err := e.SearchSessions(search.Query{Text: "hash 徽章"})
	if err != nil {
		t.Fatalf("扩展后的多词查询报错：%v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].ID != ids["synonym"] {
		t.Fatalf("应只命中同时含（hash 或近义词）与「徽章」的会话：%+v", res.Sessions)
	}
	res, err = e.SearchSessions(search.Query{Text: "徽章 hash"})
	if err != nil {
		t.Fatalf("扩展组在后时报错：%v", err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("词序不该影响结果：%+v", res.Sessions)
	}
}

func TestSearchSynonymSpecialChars(t *testing.T) {
	// 词表项与查询词走同一条转义；FTS5 特殊字符不得让查询报错
	e, _ := synEngine(t, `hash foo* a"b NEAR(x col:val rate-limit`)
	for _, q := range []string{"hash", "foo*", `a"b`, "col:val"} {
		if _, err := e.SearchSessions(search.Query{Text: q}); err != nil {
			t.Errorf("查询 %q 报错: %v", q, err)
		}
	}
}

func TestSearchBlocksExpands(t *testing.T) {
	e, ids := synEngine(t, "hash 提交号")
	bs, err := e.SearchBlocks(search.Query{Text: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, b := range bs {
		seen[b.SessionID] = true
	}
	if !seen[ids["synonym"]] || !seen[ids["literal"]] {
		t.Fatalf("块检索也应扩展：%+v", bs)
	}
}

func TestTimelineKeywordFilterExpands(t *testing.T) {
	e, ids := synEngine(t, "hash 提交号")
	res, err := e.Timeline(search.Query{Text: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, g := range res.Groups {
		for _, s := range g.Sessions {
			seen[s.ID] = true
		}
	}
	if !seen[ids["synonym"]] || !seen[ids["literal"]] || seen[ids["unrelated"]] {
		t.Fatalf("时间线关键词过滤应与检索一致地扩展：%v", seen)
	}
}

func TestExpandMatchShape(t *testing.T) {
	syn := search.ParseSynonyms("hash SHA")
	m, lit, exp := search.ExpandMatch("hash 徽章", syn)
	if m != `("hash" OR "SHA") AND "徽 章"` {
		t.Errorf("match = %s", m)
	}
	if lit != `"hash" AND "徽 章"` {
		t.Errorf("literal = %s", lit)
	}
	if len(exp) != 1 {
		t.Errorf("exp = %+v", exp)
	}
}

// via_synonym 的判据是「最佳块只用原词匹配不上」，不是「块里一个原词都没有」：
// 搜「hash 徽章」，块里有「徽章」原词、hash 靠「提交号」补上，仍算近义词命中。
func TestViaSynonymWhenOnlySomeTermsLiteral(t *testing.T) {
	e, ids := synEngine(t, "hash 提交号")
	res, err := e.SearchSessions(search.Query{Text: "hash 徽章"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].ID != ids["synonym"] || !res.Sessions[0].ViaSynonym {
		t.Fatalf("块里有部分原词、其余靠近义词补上，应标近义词命中：%+v", res.Sessions)
	}
}
