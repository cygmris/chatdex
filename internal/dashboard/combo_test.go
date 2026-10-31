package dashboard

import (
	"os/exec"
	"testing"
)

// combo.js 的纯函数（过滤排序 / 高亮移动 / 文案回退 / 自定义项）用 node --test 跑（static/combo_test.mjs）。
// node 只是测试依赖，没装的机器跳过并说明——与 rangepicker_test.go 同一约定。
func TestComboMath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("需要 node 才能跑 combo.js 的纯函数测试（static/combo_test.mjs）")
	}
	out, err := exec.Command(node, "--test", "static/combo_test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("node --test 失败：%v\n%s", err, out)
	}
}
