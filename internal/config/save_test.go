package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 只写改过的键。
//
// 把全部默认值固化进文件，等于把这份配置钉死在今天的默认值上——
// 将来调整默认值，已装的人不会跟随，而他们根本不知道自己「配置」过这些项。
func TestSaveWritesOnlyChangedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Summary.Model = "gemma4:e4b"

	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("文件里应只有 summary 一段，实得 %v", got)
	}
	sec, _ := got["summary"].(map[string]any)
	if len(sec) != 1 || sec["model"] != "gemma4:e4b" {
		t.Errorf("summary 段 = %v", sec)
	}
	// 默认值一个都不该出现
	if strings.Contains(string(raw), "5021") || strings.Contains(string(raw), "11434") {
		t.Errorf("默认值被固化进了文件:\n%s", raw)
	}
}

// 存了再读，值要能回来（只写差异不能变成丢配置）。
func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Summary.ThrottleMS = 0
	c.UI.LightTheme = "paper"
	c.Index.ToolResultBody = false

	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.ThrottleMS != 0 || got.UI.LightTheme != "paper" || got.Index.ToolResultBody {
		t.Errorf("往返后值不对: throttle=%d light=%s body=%v",
			got.Summary.ThrottleMS, got.UI.LightTheme, got.Index.ToolResultBody)
	}
	// 没改的项应回到默认
	if got.Ports.UI != Default().Ports.UI {
		t.Errorf("未改动的项没回到默认: %d", got.Ports.UI)
	}
}

// 配置文件含模型名等本机信息，与索引库同属私有配置。
func TestSavePermissionsAre0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("权限 = %o, want 600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("目录权限 = %o, want 700", di.Mode().Perm())
	}
}

// 原子写：失败时不得留下半个文件，也不得毁掉原有配置。
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	// 用非法配置触发失败
	bad := Default()
	bad.LLM.Endpoint = "https://api.example.com"
	if err := Save(path, bad); err == nil {
		t.Fatal("非法配置竟然保存成功了")
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Error("保存失败后原有配置被改动了")
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("留下了临时文件")
	}
}

// 界面上能填不等于能存（需求 4.5）。
func TestSaveRejectsRemoteLLMEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, ep := range []string{
		"https://api.openai.com",
		"http://192.168.1.10:11434",
		"http://ollama.internal:11434",
	} {
		c := Default()
		c.LLM.Endpoint = ep
		err := Save(path, c)
		if err == nil {
			t.Errorf("%s 竟然被保存了", ep)
			continue
		}
		if !strings.Contains(err.Error(), "回环") {
			t.Errorf("%s 的报错未说明原因: %v", ep, err)
		}
		if _, e := os.Stat(path); e == nil {
			t.Errorf("%s 被拒后仍写出了文件", ep)
		}
	}
}

// 非法取值的报错必须指出是**哪一项**。
func TestValidatePointsAtTheField(t *testing.T) {
	cases := []struct {
		mutate func(*Config)
		key    string
	}{
		{func(c *Config) { c.Summary.ThrottleMS = -1 }, "summary.throttle_ms"},
		{func(c *Config) { c.Chat.MaxToolRounds = 0 }, "chat.max_tool_rounds"},
		{func(c *Config) { c.Scan.IntervalSec = 1 }, "scan.interval_sec"},
		{func(c *Config) { c.Index.ToolResultCap = 0 }, "index.tool_result_cap"},
		{func(c *Config) { c.UI.LightTheme = "nope" }, "ui.light_theme"},
		{func(c *Config) { c.Ports.API = c.Ports.UI }, "ports.api"},
		{func(c *Config) { c.DBPath = "" }, "db_path"},
	}
	for _, tc := range cases {
		c := Default()
		tc.mutate(&c)
		errs := Validate(c)
		found := false
		for _, e := range errs {
			if e.Key == tc.key {
				found = true
			}
		}
		if !found {
			t.Errorf("期望 %s 报错，实得 %+v", tc.key, errs)
		}
	}
}

// 元信息必须覆盖 Config 的每个字段——漏一个，界面上就少一格，
// 而少的那一格不会有任何东西报错。
func TestFieldsCoverEveryConfigKey(t *testing.T) {
	c := Default()
	for _, f := range Fields() {
		if c.Get(f.Key) == nil {
			t.Errorf("元信息里的 %s 在 Config.Get 里取不到值", f.Key)
		}
		if f.Label == "" || f.Help == "" || f.Group == "" {
			t.Errorf("%s 的 label/help/group 不完整", f.Key)
		}
	}
	// 反向：Config 的字段数应与元信息条数一致
	var n int
	raw, _ := json.Marshal(c)
	var m map[string]any
	json.Unmarshal(raw, &m)
	for _, v := range m {
		if sub, ok := v.(map[string]any); ok {
			n += len(sub)
		} else {
			n++
		}
	}
	if n != len(Fields()) {
		t.Errorf("Config 有 %d 个可配置字段，但元信息只声明了 %d 个——新增配置项时忘了同步 meta.go", n, len(Fields()))
	}
}

// 配置里出现切片/map 这类**不可比较**的值时，Save 不得 panic。
//
// diffFromDefault 原本用 `cur != base` 比较 any —— 接口里装着切片时
// 那不是返回 false，是**直接崩**。在 backup.sources（[]BackupSource）加进来
// 之前所有配置值恰好都是标量，所以这个假设一直没被戳破。
func TestSaveHandlesNonComparableValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	// 改动一个切片字段：既要不 panic，也要真的被写进去
	c.Backup.Sources = []BackupSource{{Path: "/somewhere", Enabled: true}}

	if err := Save(path, c); err != nil {
		t.Fatalf("Save 不该失败：%v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	sec, ok := m["backup"].(map[string]any)
	if !ok {
		t.Fatalf("改过的切片字段没被写进配置：%s", raw)
	}
	if _, ok := sec["sources"]; !ok {
		t.Errorf("backup.sources 没被写进去：%s", raw)
	}

	// 反向：没改过就不该写（沿用「只写改过的键」那条纪律）
	path2 := filepath.Join(t.TempDir(), "c2.json")
	if err := Save(path2, Default()); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(path2)
	var m2 map[string]any
	json.Unmarshal(raw2, &m2)
	if sec, ok := m2["backup"].(map[string]any); ok {
		if _, has := sec["sources"]; has {
			t.Errorf("默认值不该被写进配置：%s", raw2)
		}
	}
}
