package backup

import (
	"context"
	"log/slog"
	"sync"
)

// 全快照路径并集的增量扫描。
//
// 覆盖率此前只跟**最新一个快照**比对，而源文件已消失的会话按定义就不在最新
// 快照里——它只活在旧快照。于是「还救得回来」恒为 0，「永久丢失」把能救的
// 全算成没了：实测 1973 个里随机 29 个样本有 25 个（86%）在旧快照里找得到。
//
// 为什么要缓存：实测单个快照 `restic ls` 约 1 秒 / 7000 个文件，当前 566 个快照
// —— 全扫 9.4 分钟。请求路径上做不了，只能后台增量。

// SeenStore 是路径并集索引的存储。
//
// 用接口而不是直接依赖 index.Store：backup 包不认识 SQL，
// 与「解析器认识 JSONL、索引层不认识」是同一条分层纪律。
type SeenStore interface {
	ScannedSnapshots() (map[string]bool, error)
	RecordSnapshotFiles(snapshotID string, paths []string) error
}

// SeenProgress 是索引进度。
//
// **它必须随结论一起下发。** 在扫完之前，「没找到」只意味着「还没在已扫的那部分里
// 找到」，而它与「查过了，确实没有」在数字上完全同形——这正是本期要修的那个谎。
type SeenProgress struct {
	Scanned int  `json:"seen_scanned"`
	Total   int  `json:"seen_total"`
	Ready   bool `json:"seen_ready"`
}

// SeenScanner 增量地把每个快照里的文件路径并进索引。
type SeenScanner struct {
	r     *Runner
	store SeenStore
	log   *slog.Logger

	mu      sync.Mutex // 同一时刻只允许一轮扫描
	running bool
}

func NewSeenScanner(r *Runner, store SeenStore, log *slog.Logger) *SeenScanner {
	if log == nil {
		log = slog.Default()
	}
	return &SeenScanner{r: r, store: store, log: log}
}

// Progress 报告已扫 / 总数。
func (s *SeenScanner) Progress(ctx context.Context) (SeenProgress, error) {
	snaps, err := s.r.Snapshots(ctx)
	if err != nil {
		return SeenProgress{}, err
	}
	done, err := s.store.ScannedSnapshots()
	if err != nil {
		return SeenProgress{}, err
	}
	n := 0
	for _, sp := range snaps {
		if done[sp.ID] {
			n++
		}
	}
	// 仓库里一个快照都没有时，「什么都没备」是确凿结论，不是「还没查」。
	return SeenProgress{Scanned: n, Total: len(snaps), Ready: n >= len(snaps)}, nil
}

// Scan 扫掉所有还没扫过的快照。已经在跑就直接返回（不排队、不并发）。
//
// **逐快照提交**：566 个快照要跑 9 分钟，中途重启是常态；每扫完一个就落库，
// 下次从断点续，而不是全部重来。
func (s *SeenScanner) Scan(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	snaps, err := s.r.Snapshots(ctx)
	if err != nil {
		return err
	}
	done, err := s.store.ScannedSnapshots()
	if err != nil {
		return err
	}

	todo := 0
	for _, sp := range snaps {
		if !done[sp.ID] {
			todo++
		}
	}
	if todo == 0 {
		return nil
	}
	s.log.Info("全快照路径索引开始", "待扫", todo, "总数", len(snaps))

	n := 0
	for _, sp := range snaps {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if done[sp.ID] {
			continue
		}
		files, err := s.filePaths(ctx, sp.ID)
		if err != nil {
			// 单个快照失败不中断整轮，**也不记为已扫**——下次会重试。
			// 记为已扫才是危险的：那些路径就此永远不会入库，
			// 而「永久丢失」会因此多出一批假的。
			s.log.Warn("快照扫描失败，跳过", "snapshot", sp.ID[:min(8, len(sp.ID))], "err", err)
			continue
		}
		if err := s.store.RecordSnapshotFiles(sp.ID, files); err != nil {
			return err
		}
		n++
	}
	s.log.Info("全快照路径索引完成", "本轮新扫", n, "总数", len(snaps))
	return nil
}

// filePaths 列出一个快照里的全部文件路径。
func (s *SeenScanner) filePaths(ctx context.Context, id string) ([]string, error) {
	m, err := s.r.filesInSnapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	return out, nil
}
