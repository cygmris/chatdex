package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// 覆盖率校验是 chatdex 在这件事上**唯一无法被 restic 取代**的作用。
//
// restic 只知道路径，不知道什么是会话。而 chatdex 库里有每个会话的
// `file_path`，于是它能回答一个 restic 永远答不出的问题：
// **「我索引过的那些会话，备份里到底有没有？」**
//
// 这也是本功能存在的理由：实测 183 个会话的源文件已经消失（日常清理，
// 不是硬件故障），而 chatdex 是唯一知道它们存在过的东西。

// coverageLimit 是每一类最多返回的条数。
//
// 界面上列一万条没有意义，而总数必须准——所以限量返回 + 总数单独给
// （R8 的 Children 栽过：拿返回条数当总数会永远显示 limit）。
const coverageLimit = 50

// Entry 是一条会话在备份里的状态。
type Entry struct {
	Path  string `json:"path"`
	Alive bool   `json:"alive"`
}

// Coverage 是覆盖率校验的结果。
//
// 三分类而不是「备了 / 没备」两分类：**「源已消失但备份里有」这一类
// 才是这个功能的价值证明**，把它混进「已覆盖」就看不见了。
type Coverage struct {
	CoveredTotal int `json:"covered_total"`
	MissingTotal int `json:"missing_total"`
	// LostTotal 是 MissingTotal 里**源也已经消失**的那部分：备份里没有、
	// 源文件也没了 —— 这些是真的没了，谁也救不回来。
	//
	// 它是 Missing 的**子集**而不是第四类，所以三类相加仍等于总数。
	// 单列出来是因为两者的含义天差地别：「没备但源还在」= 去勾上就好，
	// 「没备且源已没」= 永久丢失。实测首次备份时这两个数是 0 和 183 ——
	// 只报 MissingTotal 会把「已经永久丢失 183 个」说成「你漏备了 183 个」。
	//
	// 必须后端算：Missing 限量 50 条，前端数 len() 只会得到 50（R8 栽过）。
	LostTotal int `json:"lost_total"`
	// RescuedTotal 是源文件已经没了、但备份里还留着的——这些会话
	// 现在只能从备份读回来。
	RescuedTotal int `json:"rescued_total"`

	Missing []Entry `json:"missing"`
	Rescued []Entry `json:"rescued"`

	// SnapshotID 是校验依据的快照；空表示仓库里还没有快照。
	SnapshotID string `json:"snapshot_id,omitempty"`
	// SnapshotTime 是那个快照的时间。
	//
	// **不带它，上面四个数就会说谎**：覆盖率永远是拿「最新快照」比对的，
	// 而「最新」可能是一周前。那之后新建的会话一条都没备，却既不在
	// 「已覆盖」也不在「未覆盖」——它们压根不在比对基准里，于是
	// 界面上一片安好。实测撞到过：快照停在 08-10，界面在 08-17 仍报
	// 「已覆盖 3082」。这与「未覆盖 183」是同一族错误：算术上对、读起来反。
	SnapshotTime time.Time `json:"snapshot_time,omitempty"`
	// Stale 表示这个快照已经旧到不该再当作「你受保护了」的依据。
	//
	// 判定放后端而不是让前端拿时间自己减：阈值只能有一处，
	// 两处实现必然漂移（R13 的教训）。
	Stale bool `json:"stale"`
	// StaleFor 是人话的「距今多久」，如「7 天前」。
	StaleFor string `json:"stale_for,omitempty"`
}

// staleAfter 是快照旧到什么程度就该告警。
//
// 24 小时的依据：扫描每 30 秒一轮，自动备份的最小间隔是 30 分钟，
// 所以只要 after_scan 开着，一天之内必然备到。超过一天没备，
// 一定是哪里停了——不是「刚好没改动」能解释的。
const staleAfter = 24 * time.Hour

// staleness 判定一个快照是否已经旧到不该再当作「你受保护了」的依据。
//
// now 显式传入而不是在里面调 time.Now()：否则测试只能验「刚备完不过期」
// 那一侧，而那一侧永远绿（时间差接近 0），跨阈值那一侧根本走不到。
func staleness(snapshotTime, now time.Time) (stale bool, describe string) {
	age := now.Sub(snapshotTime)
	return age >= staleAfter, describeAge(age)
}

// describeAge 把时长说成人话。只用于展示，不参与判定。
func describeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
}

// IndexedSession 是索引里的一个会话，由上层从 sessions 表取。
//
// 让上层传进来而不是在这里查库：backup 包不该认识 SQL，
// 与「解析器认识 JSONL、索引层不认识」是同一条分层纪律。
type IndexedSession struct {
	Path  string
	Alive bool
}

// addMissing 记一条「备份里没有」。计数与限量返回只在这一处，
// 免得两个调用点各写一遍再漂移（R13 的教训）。
func (c *Coverage) addMissing(s IndexedSession) {
	c.MissingTotal++
	if !s.Alive {
		c.LostTotal++
	}
	if len(c.Missing) < coverageLimit {
		c.Missing = append(c.Missing, Entry{Path: s.Path, Alive: s.Alive})
	}
}

// Coverage 把「索引里有什么」与「备份里有什么」对一遍。
func (r *Runner) Coverage(ctx context.Context, sessions []IndexedSession) (Coverage, error) {
	snaps, err := r.Snapshots(ctx)
	if err != nil {
		return Coverage{}, err
	}
	if len(snaps) == 0 {
		// 没有快照不是错误，是「还没备过」——所有会话都算未覆盖。
		cov := Coverage{}
		for _, s := range sessions {
			cov.addMissing(s)
		}
		return cov, nil
	}
	latest := snaps[len(snaps)-1] // restic 按时间正序返回

	inBackup, err := r.filesInSnapshot(ctx, latest.ID)
	if err != nil {
		return Coverage{}, err
	}

	cov := Coverage{SnapshotID: latest.ID, SnapshotTime: latest.Time}
	cov.Stale, cov.StaleFor = staleness(latest.Time, time.Now())
	for _, s := range sessions {
		switch {
		case !inBackup[s.Path]:
			cov.addMissing(s)
		case !s.Alive:
			// 源没了但备份里有——这一类是本功能的价值证明
			cov.RescuedTotal++
			if len(cov.Rescued) < coverageLimit {
				cov.Rescued = append(cov.Rescued, Entry{Path: s.Path, Alive: false})
			}
		default:
			cov.CoveredTotal++
		}
	}
	sort.Slice(cov.Missing, func(i, j int) bool { return cov.Missing[i].Path < cov.Missing[j].Path })
	sort.Slice(cov.Rescued, func(i, j int) bool { return cov.Rescued[i].Path < cov.Rescued[j].Path })
	return cov, nil
}

// filesInSnapshot 列出某个快照里的全部文件路径。
//
// `restic ls --json` 每行一个对象（第一行是快照本身），不是一个大数组——
// 与 `snapshots --json` 的形态不同，这里必须逐行解析。
func (r *Runner) filesInSnapshot(ctx context.Context, id string) (map[string]bool, error) {
	out, err := r.run(ctx, "ls", "--json", id)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m struct {
			Type string `json:"type"`
			Path string `json:"path"`
		}
		if json.Unmarshal(line, &m) != nil || m.Type != "file" {
			continue
		}
		files[m.Path] = true
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("快照 %s 里一个文件都没有", id[:min(8, len(id))])
	}
	return files, nil
}
