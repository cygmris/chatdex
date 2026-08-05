package summary

import (
	"testing"
	"time"
)

func at(h, m int) time.Time {
	return time.Date(2026, 8, 3, h, m, 0, 0, time.Local)
}

// 跨零点是这里唯一的难点，也是最容易写错的一格：
// 起点 > 终点时区间是「≥起点 或 ≤终点」，而不是普通闭区间。
func TestInWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		win  string
		now  time.Time
		want bool
	}{
		{"空 = 不限", "", at(13, 0), true},
		{"同日窗口内", "02:00-08:00", at(3, 0), true},
		{"同日窗口外", "02:00-08:00", at(13, 0), false},
		{"同日起点当刻算在内", "02:00-08:00", at(2, 0), true},
		{"同日终点当刻算在外", "02:00-08:00", at(8, 0), false},

		{"跨零点·深夜在内", "22:00-06:00", at(23, 30), true},
		{"跨零点·凌晨在内", "22:00-06:00", at(1, 0), true},
		{"跨零点·白天在外", "22:00-06:00", at(13, 0), false},
		{"跨零点·起点当刻在内", "22:00-06:00", at(22, 0), true},
		{"跨零点·终点当刻在外", "22:00-06:00", at(6, 0), false},

		{"起止相同视为不限", "03:00-03:00", at(13, 0), true},
	} {
		in, _, err := InWindow(c.win, c.now)
		if err != nil {
			t.Errorf("%s：不该报错 %v", c.name, err)
			continue
		}
		if in != c.want {
			t.Errorf("%s：InWindow(%q, %s) = %v, want %v",
				c.name, c.win, c.now.Format("15:04"), in, c.want)
		}
	}
}

// 非法值必须报错，好让调用方回退为「不限」并记 warn——
// 不得让服务因为一个填错的配置起不来。
func TestInWindowRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"乱写", "02:00", "25:00-08:00", "02:60-08:00",
		"02:00-", "-08:00", "aa:bb-cc:dd"} {
		in, _, err := InWindow(bad, at(13, 0))
		if err == nil {
			t.Errorf("%q 应当报错", bad)
		}
		// 报错时仍返回 in=true：调用方即使忽略 err 也是「不限」，
		// 不会静默停摆（宁可多跑，不要悄悄不跑）
		if !in {
			t.Errorf("%q 报错时应返回 in=true 作为安全默认", bad)
		}
	}
}

// 窗口外要能说出「下次几点开始」，否则界面只能显示一句没用的「未在窗口内」。
func TestNextStart(t *testing.T) {
	_, next, err := InWindow("02:00-08:00", at(13, 0))
	if err != nil {
		t.Fatal(err)
	}
	// 13:00 时下一次是明天 02:00
	if next.Hour() != 2 || next.Minute() != 0 {
		t.Errorf("下次开始时刻 = %s, want 02:00", next.Format("15:04"))
	}
	if next.Day() != 4 {
		t.Errorf("13:00 时下一次应当是明天，得到 %s", next.Format("01-02 15:04"))
	}

	// 01:00 时下一次是今天 02:00
	_, next, _ = InWindow("02:00-08:00", at(1, 0))
	if next.Day() != 3 || next.Hour() != 2 {
		t.Errorf("01:00 时下一次应当是今天 02:00，得到 %s", next.Format("01-02 15:04"))
	}
}
