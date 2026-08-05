package index

import (
	"errors"
	"testing"
)

// 体积上限判定必须是三态。
//
// 这条断言守的是一个**没有任何其它信号**的失效：原写法在 Stats() 出错时
// 与「没超限」同义，于是守卫静默关闭、索引继续无限增长，日志里一个字都没有。
// 而这个守卫存在的全部意义就是防无限增长。
func TestSizeGuardIsThreeState(t *testing.T) {
	boom := errors.New("库读不到")

	for _, c := range []struct {
		name                  string
		bytes, max            int64
		err                   error
		wantCapped, wantUnkwn bool
	}{
		{"没超限", 100, 1000, nil, false, false},
		{"正好到上限", 1000, 1000, nil, true, false},
		{"超了", 2000, 1000, nil, true, false},
		{"读不到体积", 0, 1000, boom, false, true},
		// 关键一格：出错时哪怕字节数看起来超了，也不能给出「已封顶」的结论——
		// 那个字节数本身就是不可信的
		{"出错时字节数不可信", 9999, 1000, boom, false, true},
	} {
		capped, unknown := classifySize(c.bytes, c.max, c.err)
		if capped != c.wantCapped || unknown != c.wantUnkwn {
			t.Errorf("%s：得到 capped=%v unknown=%v，应为 capped=%v unknown=%v",
				c.name, capped, unknown, c.wantCapped, c.wantUnkwn)
		}
		// 两个结论不能同时成立——「已封顶」是个结论，「不知道」是没有结论
		if capped && unknown {
			t.Errorf("%s：capped 与 unknown 同时为真", c.name)
		}
	}
}
