package summary

import (
	"fmt"
	"strings"
	"time"
)

// 生成时间窗口。摘要是十几小时量级、吃满本地 GPU 的活，
// 使用者要能说「只在夜里挂机时跑」。
//
// 窗口只管**生成**：窗口外索引扫描与检索照常，与「LLM 不可用不得整体不可用」
// 是同一条纪律。

// InWindow 判断此刻是否在生成窗口内，并给出下一次开始时间。
//
// win 为 "HH:MM-HH:MM"；空串表示不限（永远在窗口内）。
// **跨零点**（如 22:00-06:00）是这里唯一的难点：起点 > 终点时，
// 区间是「≥起点 或 ≤终点」，而不是普通的闭区间。
//
// 按本地时区判断——使用者说「夜里跑」指的是他所在时区的夜里。
//
// 非法值返回 err，调用方应当回退为「不限」并记 warn（与主题名、
// 高亮配色名同一条降级纪律），不得让服务起不来。
func InWindow(win string, now time.Time) (in bool, next time.Time, err error) {
	win = strings.TrimSpace(win)
	if win == "" {
		return true, time.Time{}, nil
	}
	start, end, err := parseWindow(win)
	if err != nil {
		return true, time.Time{}, err
	}
	if start == end {
		// 起止相同：视为整天开放而不是零长度窗口。
		// 零长度窗口意味着摘要永远不跑，那多半不是使用者的本意，
		// 而是他填错了——宁可多跑，不要静默停摆。
		return true, time.Time{}, nil
	}

	cur := now.Hour()*60 + now.Minute()
	if start < end {
		in = cur >= start && cur < end
	} else {
		in = cur >= start || cur < end // 跨零点
	}
	if in {
		return true, time.Time{}, nil
	}
	return false, nextAt(now, start), nil
}

func parseWindow(win string) (start, end int, err error) {
	a, b, ok := strings.Cut(win, "-")
	if !ok {
		return 0, 0, fmt.Errorf("窗口格式应为 HH:MM-HH:MM，得到 %q", win)
	}
	if start, err = parseHM(a); err != nil {
		return 0, 0, err
	}
	if end, err = parseHM(b); err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func parseHM(s string) (int, error) {
	s = strings.TrimSpace(s)
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("时刻应为 HH:MM，得到 %q", s)
	}
	var hh, mm int
	if _, err := fmt.Sscanf(h, "%d", &hh); err != nil {
		return 0, fmt.Errorf("时不是数字：%q", s)
	}
	if _, err := fmt.Sscanf(m, "%d", &mm); err != nil {
		return 0, fmt.Errorf("分不是数字：%q", s)
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("时刻超出范围：%q", s)
	}
	return hh*60 + mm, nil
}

// nextAt 返回今天或明天的 minutes 时刻——取还没过去的那个。
func nextAt(now time.Time, minutes int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(),
		minutes/60, minutes%60, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}
