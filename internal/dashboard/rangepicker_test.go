package dashboard

import (
	"os"
	"os/exec"
	"testing"
)

// range-picker.js 的纯日期逻辑用 node --test 跑（static/range-picker_test.mjs）。
//
// node 只是**测试**依赖，不是构建依赖——仓库没有前端构建链，没装 node 的机器照常
// go build；但那台机器上这组用例会被跳过，所以 Skip 要把原因说出来。
// 固定 TZ=Asia/Bangkok：用例里有一条专门钉「本地日历 vs UTC」，不在 +7 下跑等于没测。
func TestRangePickerMath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("需要 node 才能跑 range-picker.js 的纯函数测试（static/range-picker_test.mjs）")
	}
	cmd := exec.Command(node, "--test", "static/range-picker_test.mjs")
	cmd.Env = append(os.Environ(), "TZ=Asia/Bangkok")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test 失败：%v\n%s", err, out)
	}
}
