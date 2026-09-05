package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	got := runVersion()
	if got == "" {
		t.Fatal("version 不能为空")
	}
	if !strings.Contains(got, "dev") && !strings.Contains(got, ".") {
		t.Errorf("version = %q，应是 dev 或带点的版本号", got)
	}
}

func TestDoctorReportsMissingConfigAndIndex(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"home":"` + dir + `","db_path":"` + filepath.Join(dir, "index.db") + `","ports":{"ui":15921,"api":15922}}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = runDoctor([]string{"-config", cfgPath})
	w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	for _, want := range []string{"版本", "配置", "索引库", "尚不存在", "HTTP"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor 输出缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tool_result") || strings.Contains(out, "sessionId") {
		t.Error("doctor 把会话内容写进了输出")
	}
}
