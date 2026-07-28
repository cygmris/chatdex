package summary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/llm"
	"github.com/cygmris/chatdex/internal/search"
)

const (
	// pageSize 是从库里分页取消息的每页条数。
	pageSize = 500
	// maxSummaryChars 是摘要本身的长度上限，防止模型不听话写成小作文。
	maxSummaryChars = 120
	// idleWait 是队列空 / LLM 不可用时的等待间隔。
	idleWait = 30 * time.Second
	// pausedWait 比 idleWait 短得多：用户点了「继续」不该再等半分钟才动。
	pausedWait = time.Second
)

// errNoContent 表示会话里没有可摘要的消息——这是终态，不是可重试的失败。
var errNoContent = errors.New("会话无可摘要内容")

// Worker 是摘要的后台生成器。
//
// 并发固定 1：本地 7B 抢同一块 GPU/CPU，并发只会互相拖慢，
// 还会把前台检索的延迟一起带坏。
type Worker struct {
	Store  *index.Store
	Engine *search.Engine
	LLM    llm.Client
	Model  string
	// ThrottleMS 是每次生成后的间隔，夜间挂机可设 0。
	ThrottleMS int

	paused atomic.Bool
}

func (w *Worker) Pause()       { w.paused.Store(true) }
func (w *Worker) Resume()      { w.paused.Store(false) }
func (w *Worker) Paused() bool { return w.paused.Load() }

// Run 一直跑到 ctx 取消。
//
// 这是个十几小时量级的任务（3176 个会话 × 每个若干次本地生成），
// 所以队列落在库里而不是内存里：进程随时可能被 kill，重启后必须接着跑。
func (w *Worker) Run(ctx context.Context) {
	// 上次退出时卡在 running 的任务放回队列，避免僵尸
	if err := w.Store.RequeueRunning(); err != nil {
		slog.Error("摘要队列恢复失败", "err", err)
	}

	for {
		if ctx.Err() != nil {
			return
		}
		if w.Paused() {
			if sleepCtx(ctx, pausedWait) {
				return
			}
			continue
		}
		if !w.LLM.Available(ctx) {
			// LLM 不可用不是错误，是功能降级：空转重探，索引与检索照常
			if sleepCtx(ctx, idleWait) {
				return
			}
			continue
		}
		if n, err := w.Store.EnqueueMissing(); err != nil {
			slog.Error("摘要入队失败", "err", err)
		} else if n > 0 {
			slog.Info("摘要入队", "新增", n)
		}

		id, ok, err := w.Store.NextSummary()
		if err != nil {
			slog.Error("取摘要任务失败", "err", err)
			if sleepCtx(ctx, idleWait) {
				return
			}
			continue
		}
		if !ok {
			if sleepCtx(ctx, idleWait) {
				return
			}
			continue
		}

		start := time.Now()
		if err := w.summarizeAndStore(ctx, id); errors.Is(err, errNoContent) {
			// 只有元数据、没有消息的会话：重试多少次都一样，直接终结
			if e := w.Store.SkipSummary(id); e != nil {
				slog.Error("跳过空会话时出错", "session", id, "err", e)
			}
		} else if err != nil {
			slog.Warn("生成摘要失败", "session", id, "err", err)
			if e := w.Store.FailSummary(id, err.Error()); e != nil {
				slog.Error("记录摘要失败状态时出错", "err", e)
			}
		} else {
			slog.Info("摘要完成", "session", id, "耗时", time.Since(start).Round(time.Millisecond))
		}

		if sleepCtx(ctx, time.Duration(w.ThrottleMS)*time.Millisecond) {
			return
		}
	}
}

// sleepCtx 等待 d，被取消时返回 true。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() != nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

func (w *Worker) summarizeAndStore(ctx context.Context, sessionID int64) error {
	text, msgCount, err := w.Summarize(ctx, sessionID)
	if err != nil {
		return err
	}
	if text == "" {
		return fmt.Errorf("模型返回空摘要")
	}
	return w.Store.SetSummary(sessionID, text, w.Model, msgCount)
}

// Summarize 为一个会话生成摘要，返回摘要与生成时的消息数。
func (w *Worker) Summarize(ctx context.Context, sessionID int64) (string, int, error) {
	msgs, total, err := w.loadMessages(sessionID)
	if err != nil {
		return "", 0, err
	}
	if len(msgs) == 0 {
		return "", 0, errNoContent
	}

	chunks := Split(Distill(msgs))
	if len(chunks) == 1 {
		sys, prompt := SinglePrompt(chunks[0].Text)
		out, err := w.gen(ctx, sys, prompt)
		return out, total, err
	}

	// map-reduce：先逐段提要，再汇总成一句
	parts := make([]string, 0, len(chunks))
	for i, c := range chunks {
		sys, prompt := MapPrompt(c, i, len(chunks))
		p, err := w.gen(ctx, sys, prompt)
		if err != nil {
			return "", 0, fmt.Errorf("第 %d 段提要: %w", i+1, err)
		}
		parts = append(parts, p)
	}
	sys, prompt := ReducePrompt(parts)
	out, err := w.gen(ctx, sys, prompt)
	return out, total, err
}

func (w *Worker) gen(ctx context.Context, system, prompt string) (string, error) {
	out, err := w.LLM.Generate(ctx, llm.GenerateRequest{
		Model: w.Model, System: system, Prompt: prompt, NumPredict: 256,
	})
	if err != nil {
		return "", err
	}
	return tidy(out), nil
}

// tidy 收拾模型输出：去掉常见的引号包裹与多余换行，并按上限截断。
func tidy(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Trim(s, `"“”'`)
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > maxSummaryChars {
		s = string(r[:maxSummaryChars])
	}
	return s
}

// loadMessages 分页取全部消息（回读接口单次有上限）。
func (w *Worker) loadMessages(sessionID int64) ([]search.Message, int, error) {
	var all []search.Message
	from, total := 0, 0
	for {
		v, err := w.Engine.GetSession(sessionID, from, pageSize)
		if err != nil {
			return nil, 0, err
		}
		total = v.Total
		if len(v.Messages) == 0 {
			return all, total, nil
		}
		all = append(all, v.Messages...)
		from = v.Messages[len(v.Messages)-1].Seq + 1
		if len(all) >= total {
			return all, total, nil
		}
	}
}
