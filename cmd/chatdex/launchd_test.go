package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdPlistTemplate(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "deploy", "launchd", "dev.cygmris.chatdex.plist"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "~") {
		t.Error("plist 模板不得含 ~：launchd 不展开它")
	}
	for _, key := range []string{
		"Label", "ProgramArguments", "RunAtLoad", "KeepAlive",
		"ThrottleInterval", "StandardOutPath", "StandardErrorPath",
		"EnvironmentVariables", "--config", "__BINARY__", "__CONFIG__",
		"__HOME__", "__LOG_DIR__", "__PATH__",
	} {
		if !strings.Contains(s, key) {
			t.Errorf("plist 模板缺少 %s", key)
		}
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("找不到仓库根")
	return ""
}
