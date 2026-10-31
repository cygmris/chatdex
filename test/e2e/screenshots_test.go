package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// scripts/screenshots.mjs 的纯函数（黑名单编译、画面文字扫描、PNG 块判定、演示 HOME 判定）
// 用 node --test 测；没有 node 就跳过并说明，不假装测过。
func TestScreenshotScriptPureFunctions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("需要 node 才能跑 scripts/screenshots.mjs 的纯函数测试（scripts/screenshots_test.mjs）")
	}
	cmd := exec.Command(node, "--test", "scripts/screenshots_test.mjs")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test 失败：%v\n%s", err, out)
	}
}

// README 引用的每张图都得在磁盘上、都得是拍摄脚本拍出来的（在 manifest 里）；
// manifest 列的每张也得在磁盘上。守的是「引了一张没人拍的图」与「清单说有其实没有」。
// 刻意不断言 manifest 的 ui_digest 等于当前界面——那会让每次改 CSS 都红，最后被 skip；
// 过期检查放在发布脚本里，发布才是过期开始扩散的时刻。
func TestReadmeImagesCoveredByManifest(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	refs := regexp.MustCompile(`docs/images/([A-Za-z0-9_-]+\.png)`).FindAllStringSubmatch(readme, -1)
	if len(refs) == 0 {
		t.Fatal("README 里没有引用任何 docs/images/*.png，正则或文件位置不对")
	}
	var m struct {
		UIDigest string `json:"ui_digest"`
		Shots    []struct {
			File string `json:"file"`
		} `json:"shots"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "docs/images/manifest.json")), &m); err != nil {
		t.Fatalf("manifest.json 解析失败：%v", err)
	}
	if len(m.UIDigest) != 16 {
		t.Fatalf("manifest.ui_digest 应为 16 位：%q", m.UIDigest)
	}
	shot := map[string]bool{}
	for _, s := range m.Shots {
		shot[s.File] = true
		if _, err := os.Stat(filepath.Join("..", "..", "docs", "images", s.File)); err != nil {
			t.Errorf("manifest 列了 %s，磁盘上没有：%v", s.File, err)
		}
	}
	for _, r := range refs {
		name := r[1]
		if _, err := os.Stat(filepath.Join("..", "..", "docs", "images", name)); err != nil {
			t.Errorf("README 引用 %s，磁盘上没有：%v", name, err)
		}
		if !shot[name] {
			t.Errorf("README 引用 %s，但 manifest 里没有——它不是 scripts/screenshots.mjs 拍的", name)
		}
	}
}
