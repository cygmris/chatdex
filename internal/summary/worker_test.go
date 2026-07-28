package summary_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/llm"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
	"github.com/cygmris/chatdex/internal/summary"
)

// fakeLLM 让测试能控制可用性、失败与调用计数。
type fakeLLM struct {
	available atomic.Bool
	calls     atomic.Int32
	fail      atomic.Bool
	reply     string
}

func newFakeLLM(reply string) *fakeLLM {
	f := &fakeLLM{reply: reply}
	f.available.Store(true)
	return f
}

func (f *fakeLLM) Available(context.Context) bool { return f.available.Load() }
func (f *fakeLLM) Generate(_ context.Context, _ llm.GenerateRequest) (string, error) {
	f.calls.Add(1)
	if f.fail.Load() {
		return "", fmt.Errorf("模型炸了")
	}
	return f.reply, nil
}
func (f *fakeLLM) Chat(context.Context, string, []llm.Message, []llm.ToolDef) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, nil
}

func newEnv(t *testing.T, reply string) (*index.Store, *summary.Worker, *fakeLLM) {
	t.Helper()
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := newFakeLLM(reply)
	w := &summary.Worker{
		Store: st, Engine: search.NewEngine(st.DB()), LLM: f,
		Model: "test-model", ThrottleMS: 0,
	}
	return st, w, f
}

func seed(t *testing.T, st *index.Store, uid string, n int) int64 {
	t.Helper()
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: uid, FilePath: "/s/" + uid + ".jsonl",
		ProjectPath: "/proj", StartedAt: 1000, EndedAt: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := make([]model.Block, 0, n)
	for i := range n {
		blocks = append(blocks, model.Block{
			Seq: i, TS: 1000, Kind: model.KindUser,
			Body: fmt.Sprintf("第 %d 条：做一个类似 timemachine 的管理工具", i),
		})
	}
	if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	return id
}

// 摘要必须作为文本进 FTS5，使搜索能命中原文没出现过的概念词（需求 11.2）——
// 这正是需求 8 的向量检索被降为门控的依据。
func TestSummaryIsSearchableAsText(t *testing.T) {
	st, w, _ := newEnv(t, "讨论基于 restic 做增量备份工具")
	id := seed(t, st, "s1", 3)

	text, n, err := w.Summarize(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("消息数 = %d", n)
	}
	if err := st.SetSummary(id, text, "test-model", n); err != nil {
		t.Fatal(err)
	}

	e := search.NewEngine(st.DB())
	// 「增量备份」只在摘要里出现，原文写的是「类似 timemachine 的管理工具」
	res, err := e.SearchSessions(search.Query{Text: "增量备份"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("摘要未被检索命中: %+v", res)
	}
	if res.Sessions[0].BestKind != "summary" {
		t.Errorf("命中的不是摘要块: %s", res.Sessions[0].BestKind)
	}
	if res.Sessions[0].Summary == "" {
		t.Error("检索结果未带上摘要")
	}

	// 摘要块不该混进会话回读（它不是对话的一部分）
	v, err := e.GetSession(id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range v.Messages {
		if m.Kind == "summary" {
			t.Error("摘要块混进了回读视图")
		}
	}
}

// 队列落库 + 可中断续跑：kill 掉进程后重启，必须从中断处继续而不是从头。
func TestQueueResumesAfterInterruption(t *testing.T) {
	st, w, f := newEnv(t, "一句摘要")
	for i := range 5 {
		seed(t, st, fmt.Sprintf("s%d", i), 2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	// 等到至少 2 个完成，然后「杀掉」worker
	waitFor(t, func() bool {
		p, _ := st.SummaryProgress()
		return p.Done >= 2
	}, "前 2 个摘要未完成")
	cancel()
	<-done

	mid, err := st.SummaryProgress()
	if err != nil {
		t.Fatal(err)
	}
	if mid.Done == 0 || mid.Done == 5 {
		t.Skipf("中断时机不合适（done=%d），跳过", mid.Done)
	}
	callsBefore := f.calls.Load()

	// 重启：新的 worker 接着跑
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go w.Run(ctx2)
	waitFor(t, func() bool {
		p, _ := st.SummaryProgress()
		return p.Done == 5 && p.Pending == 0 && p.Running == 0
	}, "重启后未把剩余任务跑完")

	// 已完成的不该重做：总调用次数不应等于「从头再来」
	if got := f.calls.Load(); got > callsBefore+int32(5-mid.Done)+1 {
		t.Errorf("重启后重做了已完成的任务：中断前 %d 次调用，之后又 %d 次（剩余 %d 个）",
			callsBefore, got-callsBefore, 5-mid.Done)
	}
}

// 暂停后不得再发请求；恢复后继续（需求 11.5，夜间挂机要能随时叫停）。
func TestPauseStopsWorkAndResumeContinues(t *testing.T) {
	st, w, f := newEnv(t, "一句摘要")
	for i := range 4 {
		seed(t, st, fmt.Sprintf("p%d", i), 2)
	}

	w.Pause()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(300 * time.Millisecond)
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("暂停期间仍发了 %d 次请求", n)
	}

	w.Resume()
	waitFor(t, func() bool {
		p, _ := st.SummaryProgress()
		return p.Done == 4
	}, "恢复后未继续处理")
}

// LLM 不可用时：索引与检索照常，只是没有摘要，绝不阻断（需求 11.6）。
func TestLLMUnavailableDoesNotBlockSearch(t *testing.T) {
	st, w, f := newEnv(t, "一句摘要")
	id := seed(t, st, "u1", 3)
	f.available.Store(false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	if n := f.calls.Load(); n != 0 {
		t.Errorf("LLM 不可用时仍发了 %d 次请求", n)
	}
	e := search.NewEngine(st.DB())
	res, err := e.SearchSessions(search.Query{Text: "timemachine"})
	if err != nil {
		t.Fatalf("检索受影响: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].ID != id {
		t.Errorf("检索结果不对: %+v", res.Sessions)
	}
	if res.Sessions[0].Summary != "" {
		t.Error("不该凭空有摘要")
	}
}

// 连续失败 3 次后置 failed，不再占着队列拖住后面的任务。
func TestFailureStopsAfterThreeAttempts(t *testing.T) {
	st, w, f := newEnv(t, "一句摘要")
	id := seed(t, st, "f1", 2)
	f.fail.Store(true)

	for range 3 {
		sid, ok, err := st.NextSummary()
		if err != nil || !ok {
			// 队列可能还没填，先补
			if _, err := st.EnqueueMissing(); err != nil {
				t.Fatal(err)
			}
			sid, ok, err = st.NextSummary()
			if err != nil || !ok {
				t.Fatalf("取任务失败 ok=%v err=%v", ok, err)
			}
		}
		if _, _, err := w.Summarize(context.Background(), sid); err == nil {
			t.Fatal("期望生成失败")
		}
		if err := st.FailSummary(sid, "模型炸了"); err != nil {
			t.Fatal(err)
		}
	}

	p, err := st.SummaryProgress()
	if err != nil {
		t.Fatal(err)
	}
	if p.Failed != 1 || p.Pending != 0 {
		t.Errorf("三次失败后应置 failed: %+v", p)
	}
	_ = id
}

// 会话在摘要后继续增长要能重做（需求 11.8）——
// session_id 是主键，重复入队若被默认忽略，摘要会永远停在旧版本。
func TestGrownSessionIsRequeued(t *testing.T) {
	st, w, _ := newEnv(t, "一句摘要")
	id := seed(t, st, "g1", 10)

	text, n, err := w.Summarize(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSummary(id, text, "test-model", n); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueMissing(); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.SummaryProgress(); p.Pending != 0 {
		t.Fatalf("没长大的会话不该重新入队: %+v", p)
	}

	// 追加消息，让消息数涨过 20%
	more := make([]model.Block, 0, 60)
	for i := range 60 {
		more = append(more, model.Block{Seq: 10 + i, TS: 2000, Kind: model.KindUser, Body: "新增内容"})
	}
	if err := st.AppendBlocks(id, more, index.Watermark{Size: 2, MTime: 2, Offset: 2}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.EnqueueMissing(); err != nil {
		t.Fatal(err)
	}
	p, _ := st.SummaryProgress()
	if p.Pending != 1 {
		t.Errorf("长大的会话应重新入队: %+v", p)
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// 超长会话走 map-reduce，调用次数有界。
func TestLongSessionUsesBoundedMapReduce(t *testing.T) {
	st, w, f := newEnv(t, "一段提要")
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "long", FilePath: "/s/long.jsonl",
		ProjectPath: "/proj", StartedAt: 1000, EndedAt: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 造出远超 24000 字的抽稀文本
	blocks := make([]model.Block, 0, 400)
	for i := range 400 {
		blocks = append(blocks, model.Block{
			Seq: i, TS: 1000, Kind: model.KindUser, Body: strings.Repeat("很长的一段用户输入。", 30),
		})
	}
	if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := w.Summarize(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// 最多 12 段提要 + 1 次汇总
	if n := f.calls.Load(); n > 13 {
		t.Errorf("调用了 %d 次，超过 12 段 + 1 汇总的上限", n)
	}
	if n := f.calls.Load(); n < 2 {
		t.Errorf("超长会话应走 map-reduce，实际只调用 %d 次", n)
	}
}

// 只有元数据、没有消息的会话是终态，不该被反复重试成「失败」——
// 进度条上挂一个永远修不好的失败数，比没有这个数字更糟。
func TestEmptySessionIsSkippedNotFailed(t *testing.T) {
	st, w, _ := newEnv(t, "一句摘要")
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "empty", FilePath: "/s/empty.jsonl",
		ProjectPath: "/proj", StartedAt: 1000, EndedAt: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 手动入队（EnqueueMissing 会因 msg_count=0 直接跳过它）
	if err := st.EnqueueSummary(id, 0); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	waitFor(t, func() bool {
		p, _ := st.SummaryProgress()
		return p.Pending == 0 && p.Running == 0
	}, "空会话未被处理完")

	p, _ := st.SummaryProgress()
	if p.Failed != 0 {
		t.Errorf("空会话被记成失败: %+v", p)
	}
	if p.Done != 1 {
		t.Errorf("空会话未被终结: %+v", p)
	}
}

// 没有消息的会话根本不该进队列。
func TestEnqueueMissingSkipsEmptySessions(t *testing.T) {
	st, _, _ := newEnv(t, "x")
	if _, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "e2", FilePath: "/s/e2.jsonl",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueMissing(); err != nil {
		t.Fatal(err)
	}
	p, _ := st.SummaryProgress()
	if p.Pending != 0 {
		t.Errorf("空会话被入队了: %+v", p)
	}
}
