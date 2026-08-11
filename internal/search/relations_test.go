package search_test

import (
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

// Children 的总数必须是**总数**，不是返回条数。
//
// 实测有主会话挂着 350 个子代理，而列表限 50 条。拿 len(items) 当总数的话，
// 界面会永远显示「共 50 个」——一个看起来完全合理、因而没人会去核对的错值。
func TestChildrenTotalIsNotPageSize(t *testing.T) {
	st, e := newEngine(t)
	seeds := []seed{{uid: "main", project: "/p", blocks: repeatBlocks("assistant", "父", 1)}}
	for i := 0; i < 60; i++ {
		seeds = append(seeds, seed{
			uid:     "sub" + string(rune('A'+i%26)) + string(rune('a'+i/26)),
			project: "/p", parent: "main",
			blocks: repeatBlocks("assistant", "子", 1),
		})
	}
	ids := seedSessions(t, st, seeds...)

	items, total, err := e.Children(ids["main"], 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 60 {
		t.Errorf("总数应为 60，实际 %d", total)
	}
	if len(items) != 50 {
		t.Errorf("默认应返回 50 条，实际 %d", len(items))
	}
	for _, it := range items {
		if !it.IsSub {
			t.Errorf("子代理列表里的 %d 没标 IsSub", it.ID)
		}
	}
	// limit 生效，且不能超过上限
	if items, _, _ := e.Children(ids["main"], 3); len(items) != 3 {
		t.Errorf("limit=3 应返回 3 条，实际 %d", len(items))
	}
	if items, _, _ := e.Children(ids["main"], 999); len(items) != 50 {
		t.Errorf("limit 超上限应压回 50，实际 %d", len(items))
	}
}

// 没有子代理的会话返回 0 与空列表，不报错。
func TestChildrenOfLeafIsEmpty(t *testing.T) {
	st, e := newEngine(t)
	ids := seedSessions(t, st, seed{uid: "solo", project: "/p", blocks: repeatBlocks("assistant", "独", 1)})
	items, total, err := e.Children(ids["solo"], 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(items) != 0 {
		t.Errorf("应为空，实际 total=%d items=%d", total, len(items))
	}
}

// Parent 的三种情形。中间那种（parent_uid 指向不存在的会话）是本期唯一
// 需要写降级路径的地方：实测 1554 个子代理 100% 能对上，但索引可以被部分删除。
func TestParentThreeCases(t *testing.T) {
	st, e := newEngine(t)
	ids := seedSessions(t, st,
		seed{uid: "main", project: "/p", blocks: repeatBlocks("assistant", "父", 1)},
		seed{uid: "kid", project: "/p", parent: "main", blocks: repeatBlocks("assistant", "子", 1)},
		seed{uid: "orphan", project: "/p", parent: "已经不存在了", blocks: repeatBlocks("assistant", "孤", 1)},
	)

	p, err := e.Parent(ids["kid"])
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.ID != ids["main"] {
		t.Fatalf("子代理应当找到主会话，实际 %v", p)
	}

	if p, err := e.Parent(ids["main"]); err != nil || p != nil {
		t.Errorf("主会话不该有父，实际 p=%v err=%v", p, err)
	}

	// 关键的一格：库里存在一个 session_uid 为空的会话时，主会话的 parent_uid（也是空串）
	// 会 JOIN 上它——**全部 1653 个主会话集体认下同一个爹**。schema 只要求
	// session_uid NOT NULL，空串是合法的，所以这不是假想。
	// 少了 `c.parent_uid != ''` 那条件就会这样，而界面上看不出任何异常。
	seedSessions(t, st, seed{uid: "", project: "/p", blocks: repeatBlocks("assistant", "空uid", 1)})
	if p, err := e.Parent(ids["main"]); err != nil || p != nil {
		t.Errorf("库里有空 uid 的会话时，主会话仍然不该有父，实际 p=%v err=%v", p, err)
	}
	if p, err := e.Parent(ids["orphan"]); err != nil || p != nil {
		t.Errorf("父会话缺失应返回 nil 而不是报错，实际 p=%v err=%v", p, err)
	}
}

// GetSession 要带上父子信息，两个方向都对。
func TestGetSessionCarriesRelations(t *testing.T) {
	st, e := newEngine(t)
	ids := seedSessions(t, st,
		seed{uid: "main", project: "/p", blocks: []model.Block{{Kind: "assistant", Body: "父"}}},
		seed{uid: "kid", project: "/p", parent: "main", blocks: []model.Block{{Kind: "assistant", Body: "子"}}},
	)

	v, err := e.GetSession(ids["main"], 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if v.IsSub || v.Parent != nil {
		t.Error("主会话不该是子代理、也不该有父")
	}
	if v.ChildCount != 1 {
		t.Errorf("主会话应有 1 个子代理，实际 %d", v.ChildCount)
	}

	v, err = e.GetSession(ids["kid"], 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsSub {
		t.Error("子代理的 IsSub 应为真")
	}
	if v.Parent == nil || v.Parent.ID != ids["main"] {
		t.Errorf("子代理应带上主会话，实际 %v", v.Parent)
	}
	if v.ChildCount != 0 {
		t.Errorf("子代理不该有子代理，实际 %d", v.ChildCount)
	}
}
