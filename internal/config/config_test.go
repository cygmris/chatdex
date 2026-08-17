package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 「扫描后顺手备一次」默认开着。
//
// 做成默认关的那一版在真机上**一次都没跑过**——备份从配好那天起就静默
// 停了，而覆盖率页的「已覆盖 3082」看起来一切正常，实测停了 7 天才被发现。
// 代价很小：无变化时 restic 只要 767 ms 且仓库零增长。
func TestAfterScanDefaultsOn(t *testing.T) {
	if !Default().Backup.AfterScan {
		t.Error("after_scan 默认应当开着 —— 默认关的那一版在真机上一次都没跑过")
	}
}

// 但改默认值**不得动到已有配置里的显式取值**。
//
// 用户显式关掉它，就该一直关着；升级把它重新打开，等于替用户改主意。
func TestExplicitFalseSurvivesTheNewDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	// 只写这一个键，其余全走默认——模拟真实的存量配置文件
	if err := os.WriteFile(p, []byte(`{"backup":{"after_scan":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.AfterScan {
		t.Error("配置文件里显式写着 false，却被新默认值覆盖成了 true")
	}
	// 反向对照：没写这个键时才该拿到新默认值。
	// 没有这条，上面那句无法排除「Load 根本没读文件」这种可能。
	p2 := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(p2, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(p2)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg2.Backup.AfterScan {
		t.Error("对照组失效：没写该键时应当拿到新默认值 true")
	}
}
