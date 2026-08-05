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
	// Live 返回当前生效的配置。
	//
	// 这里不存字段：拷成字段的那一刻，这些项就变成需重启才能改的了，
	// 而使用者从设置页上看不出区别（需求 4.3）。
	//
	// 返回结构体而不是多返回值：R11 一次要加 NumCtx 与 Window 两项，
	// 元组每加一项就要改全部调用点，且位置写反了编译器也未必拦得住。
	Live func() Settings

	paused atomic.Bool
}

// Settings 是 worker 每轮重新读取的那几项配置。
type Settings struct {
	Model      string
	ThrottleMS int
	Enabled    bool
	NumCtx     int
	Window     string // 生成时间窗口 "HH:MM-HH:MM"，空 = 不限
}

// cfg 取当前生效的配置；没接 Live 时（测试）用固定值。
func (w *Worker) cfg() Settings {
	if w.Live == nil {
		return Settings{Model: "test-model", Enabled: true, NumCtx: 8192}
	}
	return w.Live()
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
		set := w.cfg()
		if w.Paused() || !set.Enabled {
			if sleepCtx(ctx, pausedWait) {
				return
			}
			continue
		}
		// 时间窗口只管生成：窗口外索引扫描与检索照常，
		// 与「LLM 不可用不得整体不可用」同一条纪律。
		if in, _, err := InWindow(set.Window, time.Now()); err != nil {
			// 配置填错不该让摘要静默停摆——回退为不限，只告警
			slog.Warn("生成时间窗口配置非法，按不限处理", "window", set.Window, "err", err)
		} else if !in {
			if sleepCtx(ctx, idleWait) {
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

		throttle := w.cfg().ThrottleMS
		if sleepCtx(ctx, time.Duration(throttle)*time.Millisecond) {
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

// Summarize2 生成并落库，供测试直接驱动一次完整流程。
func (w *Worker) Summarize2(ctx context.Context, sessionID int64) error {
	return w.summarizeAndStore(ctx, sessionID)
}

func (w *Worker) summarizeAndStore(ctx context.Context, sessionID int64) error {
	text, msgCount, err := w.Summarize(ctx, sessionID)
	if err != nil {
		return err
	}
	if text == "" {
		return fmt.Errorf("模型返回空摘要")
	}
	model := w.cfg().Model
	return w.Store.SetSummary(sessionID, text, model, msgCount)
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

	set := w.cfg()
	text := Distill(msgs)
	chunks := Split(text, BudgetFor(set.NumCtx))
	// 分段与省略必须看得见：省略是正常降级，但「悄悄少看了一半」不是。
	elided := 0
	for _, c := range chunks {
		if c.Elided {
			elided++
		}
	}
	slog.Info("摘要分段", "session", sessionID, "抽稀字符", len([]rune(text)),
		"段数", len(chunks), "调用次数", len(chunks)+boolToInt(len(chunks) > 1),
		"发生省略的段", elided, "num_ctx", set.NumCtx)

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
	set := w.cfg()
	out, err := w.LLM.Generate(ctx, llm.GenerateRequest{
		Model: set.Model, System: system, Prompt: prompt,
		NumPredict: 256, NumCtx: set.NumCtx, NoThink: true,
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
