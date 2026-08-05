package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newRunner(c Config) *Runner {
	return &Runner{Cfg: func() Config { return c }}
}

// 三种不可用各有各的 reason。
//
// 混成一个「备份不可用」等于让人自己猜下一步：没配仓库要去设置页、
// restic 没装要去装、仓库没初始化点一下就好——是三件不同的事。
func TestAvailableDistinguishesReasons(t *testing.T) {
	ctx := context.Background()

	// ① 没配仓库：不该去执行任何命令
	if st := newRunner(Config{}).Available(ctx); st.Available || st.Reason == "" {
		t.Errorf("未配仓库应不可用且给出原因，实得 %+v", st)
	}

	// ② restic 不存在
	st := newRunner(Config{
		Repo: t.TempDir(), ResticPath: "/definitely/not/here/restic",
	}).Available(ctx)
	if st.Available {
		t.Error("restic 不存在时不该判为可用")
	}
	if st.RepoReady {
		t.Error("restic 都没有，不该说仓库就绪")
	}

	// ③ restic 在但仓库没初始化 —— 这一种要能与 ② 区分开，
	// 因为它是「点一下初始化就好」，而不是「去装个软件」
	bin := findRestic(t)
	st = newRunner(Config{
		Repo: filepath.Join(t.TempDir(), "empty-repo"), ResticPath: bin,
		PasswordFile: writePassFile(t),
	}).Available(ctx)
	if st.Available {
		t.Error("空仓库不该判为可用")
	}
	if st.Version == "" {
		t.Error("restic 在的情况下应当报出版本号（用于区分「没装」与「仓库没好」）")
	}
}

// 只有勾选启用的源才进命令行。
//
// 用户明确要求「可以只备份其中一个，也可以全部备份」——
// 没勾的源出现在命令行里就是备了不该备的东西。
func TestOnlyEnabledSourcesAreUsed(t *testing.T) {
	c := Config{Sources: []Source{
		{Path: "/a", Enabled: true},
		{Path: "/b"},                 // 没勾
		{Path: "   ", Enabled: true}, // 空白路径：勾了也不算
		{Path: "/c", Enabled: true},
	}}
	got := c.EnabledSources()
	if len(got) != 2 || got[0] != "/a" || got[1] != "/c" {
		t.Errorf("启用的源 = %v, want [/a /c]", got)
	}
}

// 密码只能经由文件传，且绝不出现在环境变量的值里。
//
// RESTIC_PASSWORD 会让密码出现在 /proc/<pid>/environ（同用户可读）；
// 而配置文件里存明文密码更糟——它会被备份、被同步、被截图。
func TestPasswordNeverInEnvOrConfig(t *testing.T) {
	pf := writePassFile(t)
	env := Config{Repo: "/repo", PasswordFile: pf}.env()

	var sawFile bool
	for _, kv := range env {
		if len(kv) > 17 && kv[:17] == "RESTIC_PASSWORD_F" {
			sawFile = true
		}
		if len(kv) > 16 && kv[:16] == "RESTIC_PASSWORD=" {
			t.Error("用了 RESTIC_PASSWORD —— 密码会出现在进程环境里，必须用 _FILE")
		}
		if kv != "RESTIC_PASSWORD_FILE="+pf && contains(kv, "hunter2") {
			t.Errorf("密码明文出现在环境变量里：%q", kv)
		}
	}
	if !sawFile {
		t.Error("没有传 RESTIC_PASSWORD_FILE")
	}
}

// restic 的错误常带一大段使用说明，整段塞进界面会把真正的原因淹掉。
func TestFirstLineKeepsOnlyTheReason(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{
			"纯文本错误：只取第一行并去掉 Fatal 前缀",
			"Fatal: unable to open config file: stat /x/config: no such file\nIs there a repository at the following location?\n/x\n",
			"unable to open config file: stat /x/config: no such file",
		},
		{
			// restic 0.19 在 --json 模式下把错误也吐成 JSON。直接展示这坨东西，
			// 等于把「仓库还没初始化」写成一行日志噪声甩给用户（需求 6.2 明确禁止）。
			"JSON 错误：提炼出 message 而不是整坨 JSON",
			`{"message_type":"exit_error","code":10,"message":"Fatal: repository does not exist: unable to open config file\nIs there a repository at the following location?"}`,
			"repository does not exist: unable to open config file",
		},
	} {
		if got := firstLine(c.in); got != c.want {
			t.Errorf("%s：firstLine = %q, want %q", c.name, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func findRestic(t *testing.T) string {
	t.Helper()
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".local", "bin", "restic"),
		"/usr/bin/restic",
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	t.Skip("本机没有 restic，跳过需要真实二进制的用例")
	return ""
}

func writePassFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(p, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// 备份源不得包含仓库自身——否则每次备份都把上次的备份再备一遍，无限膨胀。
//
// 这个错误发生时用户**完全看不出来**：备份「成功」了，只是仓库越来越大。
// 探测时实测误备一次就多了 5 GB。
func TestRepoInsideSourcesIsRejected(t *testing.T) {
	for _, c := range []struct {
		name       string
		repo       string
		srcs       []string
		wantReject bool
	}{
		{"仓库就在源里", "/data/backups/repo", []string{"/data/backups"}, true},
		{"源就是仓库本身", "/data/repo", []string{"/data/repo"}, true},
		{"末尾斜杠不影响判定", "/data/repo/", []string{"/data/"}, true},
		{"同级目录不算包含", "/data/repo", []string{"/data/repo-other"}, false},
		{"前缀相同但不是父目录", "/data/repository", []string{"/data/repo"}, false},
		{"正常情形", "/backup/repo", []string{"/home/u/.claude/projects"}, false},
	} {
		got := repoInsideSources(c.repo, c.srcs) != ""
		if got != c.wantReject {
			t.Errorf("%s：repoInsideSources(%q, %v) 拒绝=%v, want %v",
				c.name, c.repo, c.srcs, got, c.wantReject)
		}
	}
}

// restic 退出码 3 = 部分源数据读不了，其余都备成功了。
//
// 把它当成失败，会让一个权限不足的文件把整次备份显示成红的——
// 用户据此以为什么都没备，实际上绝大多数都备好了。
func TestPartialBackupIsWarningNotFailure(t *testing.T) {
	out := []byte(`{"message_type":"status","percent_done":0.5}
{"message_type":"summary","files_new":10,"files_changed":2,"total_files_processed":12,"data_added":1024,"total_duration":1.5,"snapshot_id":"abc123"}`)
	res, ok := parseSummary(out)
	if !ok {
		t.Fatal("没解析出 summary")
	}
	if res.SnapshotID != "abc123" || res.FilesNew != 10 || res.FilesTotal != 12 {
		t.Errorf("summary 解析错：%+v", res)
	}
	if res.BytesAdded != 1024 || res.Seconds != 1.5 {
		t.Errorf("字节数/耗时解析错：%+v", res)
	}
}

// status 进度行不得被当成结果。只有 summary 那一行才是。
func TestOnlySummaryLineIsParsed(t *testing.T) {
	out := []byte(`{"message_type":"status","percent_done":0.1}
{"message_type":"status","percent_done":0.9}`)
	if _, ok := parseSummary(out); ok {
		t.Error("只有进度行时不该认为拿到了备份结果")
	}
}
