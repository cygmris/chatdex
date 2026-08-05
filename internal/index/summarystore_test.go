package index

import (
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

func seedForSummary(t *testing.T, st *Store, uid string, msgs int) int64 {
	t.Helper()
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: uid,
		FilePath: "/sessions/" + uid + ".jsonl", ProjectPath: "/p", StartedAt: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := make([]model.Block, msgs)
	for i := range blocks {
		blocks[i] = model.Block{Kind: "user", Body: "内容", Seq: i, TS: 1500}
	}
	if err := st.AppendBlocks(id, blocks, Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	return id
}

// ⭐ 本期的核心 bug：失败任务被 EnqueueMissing 无限复活。
//
// 原来的 NOT EXISTS 只排除 pending / running，于是 failed 会被选中，
// 再被 ON CONFLICT 重置成 pending 且 attempts 清零 ——
// FailSummary 那句「重试 3 次后置 failed，不再占着队列」被完全抵消。
//
// 后果实测过：一条生成不了的会话每 2 分钟复活一次、永远排在最前面，
// 其余 92 条饿死两天；而 failed 计数因为活不过一轮循环恒为 0，界面上看不出来。
func TestFailedTasksAreNotResurrected(t *testing.T) {
	st := openTemp(t)
	bad := seedForSummary(t, st, "bad", 3)
	good := seedForSummary(t, st, "good", 3)

	if _, err := st.EnqueueMissing(); err != nil {
		t.Fatal(err)
	}
	// 连失败三次 → 应当进入 failed
	for i := 0; i < 3; i++ {
		if err := st.FailSummary(bad, "模型返回空摘要"); err != nil {
			t.Fatal(err)
		}
	}
	p, err := st.SummaryProgress()
	if err != nil {
		t.Fatal(err)
	}
	if p.Failed != 1 {
		t.Fatalf("三次失败后应有 1 条 failed，实际 %+v", p)
	}

	// 再入队若干轮：failed 不得被复活
	for i := 0; i < 3; i++ {
		if _, err := st.EnqueueMissing(); err != nil {
			t.Fatal(err)
		}
	}
	p, err = st.SummaryProgress()
	if err != nil {
		t.Fatal(err)
	}
	if p.Failed != 1 {
		t.Errorf("failed 被复活了：%+v", p)
	}

	// 而没失败的那条仍然排得上——一条失败不得阻塞其它任务
	if p.Pending < 1 {
		t.Errorf("正常任务不该被牵连：%+v", p)
	}
	_ = good
}

// 失败清单的总数必须单独查，不能用返回条数（R8 的 Children 栽过同一处）。
func TestFailuresTotalIsNotPageSize(t *testing.T) {
	st := openTemp(t)
	for i := 0; i < 5; i++ {
		id := seedForSummary(t, st, string(rune('a'+i)), 2)
		if _, err := st.EnqueueMissing(); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 3; j++ {
			if err := st.FailSummary(id, "错误 X"); err != nil {
				t.Fatal(err)
			}
		}
	}
	items, total, err := st.Failures(2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Errorf("总数应为 5，实际 %d", total)
	}
	if len(items) != 2 {
		t.Errorf("limit=2 应返回 2 条，实际 %d", len(items))
	}
	if items[0].Err != "错误 X" || items[0].Attempts != 3 {
		t.Errorf("失败详情不对：%+v", items[0])
	}
}

// 重试是失败任务唯一的出路——不再有自动复活，所以它必须真的管用。
func TestRetryPutsFailedBack(t *testing.T) {
	st := openTemp(t)
	id := seedForSummary(t, st, "x", 2)
	if _, err := st.EnqueueMissing(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := st.FailSummary(id, "boom"); err != nil {
			t.Fatal(err)
		}
	}

	ok, err := st.RetrySummary(id)
	if err != nil || !ok {
		t.Fatalf("重试应当成功，得到 ok=%v err=%v", ok, err)
	}
	p, _ := st.SummaryProgress()
	if p.Failed != 0 || p.Pending != 1 {
		t.Errorf("重试后应回到 pending：%+v", p)
	}

	// 重试一个不存在 / 不处于 failed 的会话：返回 false 而不是报错
	if ok, err := st.RetrySummary(id); err != nil || ok {
		t.Errorf("已不在 failed 的任务不该重试成功：ok=%v err=%v", ok, err)
	}
	if ok, err := st.RetrySummary(99999); err != nil || ok {
		t.Errorf("不存在的会话不该报错也不该成功：ok=%v err=%v", ok, err)
	}
}
