package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingRestic 造一个把每次调用的 argv 追加记录下来的假 restic（调用之间用 --- 分隔）。
//
// 判据必须落在 argv 上而不是「Backup 成功」：不做索引快照时 Backup 同样成功，
// 只是 4 GB 的库静默不在仓库里——那正是本功能要修的洞（R27）。
func recordingRestic(t *testing.T, dir string) (bin, argvFile string) {
	t.Helper()
	argvFile = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "restic")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argvFile + "\necho --- >> " + argvFile + `
echo '{"message_type":"summary","snapshot_id":"deadbeef","total_files_processed":1,"data_added":7,"total_duration":0.1}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

func invocations(t *testing.T, argvFile string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("假 restic 没记下参数：%v", err)
	}
	var out [][]string
	for _, blk := range strings.Split(strings.TrimSpace(string(raw)), "---") {
		blk = strings.TrimSpace(blk)
		if blk == "" {
			continue
		}
		out = append(out, strings.Split(blk, "\n"))
	}
	return out
}

func has(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// snapshotFixture 组一套：真源目录、独立的快照目录、一个把「收到的路径」记下来并真写文件的 hook。
func snapshotFixture(t *testing.T, enabled bool) (r *Runner, argvFile, snapDir string, got *[]string) {
	t.Helper()
	dir := t.TempDir()
	bin, argvFile := recordingRestic(t, dir)
	src := filepath.Join(dir, "src")
	snapDir = filepath.Join(dir, "snapshot")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Repo: filepath.Join(dir, "repo"), PasswordFile: pw, ResticPath: bin,
		Sources:           []Source{{Path: src, Enabled: true}},
		ExcludeLargerThan: "1M",
		IndexSnapshot:     enabled,
		IndexSnapshotDir:  snapDir,
	}
	got = &[]string{}
	r = &Runner{
		Cfg: func() Config { return cfg },
		IndexSnapshot: func(_ context.Context, dst string) error {
			*got = append(*got, dst)
			return os.WriteFile(dst, []byte("sqlite"), 0o644)
		},
	}
	return r, argvFile, snapDir, got
}

// 需求 1.1 / 1.2：主备份之后**第二次**调 restic，带 tag、不带大小上限；快照先写 .tmp 再改名，0600。
func TestIndexSnapshotIsASecondResticRunWithoutSizeFilter(t *testing.T) {
	r, argvFile, snapDir, got := snapshotFixture(t, true)
	if _, err := r.Backup(context.Background()); err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	inv := invocations(t, argvFile)
	if len(inv) != 2 {
		t.Fatalf("应有两次 restic 调用（主备份 + 索引快照），实际 %d：%v", len(inv), inv)
	}
	main, idx := inv[0], inv[1]
	if !has(main, "--exclude-larger-than") {
		t.Errorf("主备份丢了大小上限：%v", main)
	}
	if has(idx, "--exclude-larger-than") {
		t.Errorf("索引快照那次不能带大小上限，否则 4 GB 的库会被静默过滤：%v", idx)
	}
	if !has(idx, "--tag") || !has(idx, indexTag) || !has(idx, snapDir) {
		t.Errorf("索引快照那次应带 --tag %s 与快照目录：%v", indexTag, idx)
	}
	if len(*got) != 1 || !strings.HasSuffix((*got)[0], ".tmp") {
		t.Errorf("hook 应恰好收到一次 .tmp 路径，实际 %v", *got)
	}
	final := filepath.Join(snapDir, indexSnapshotName)
	st, err := os.Stat(final)
	if err != nil {
		t.Fatalf("改名后的快照不存在：%v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("快照权限应 0600，实际 %o", st.Mode().Perm())
	}
	if _, err := os.Stat(final + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".tmp 改名后不该残留")
	}
	li := r.LastIndex()
	if li == nil || li.Error != "" || li.BytesAdded != 7 {
		t.Errorf("LastIndex 应记下成功结果，实际 %+v", li)
	}
}

// 24 小时门：连续两次备份只做一次快照；把时钟拨回 25 小时前再做一次。
func TestIndexSnapshotRespectsDailyGap(t *testing.T) {
	r, _, _, got := snapshotFixture(t, true)
	for range 2 {
		if _, err := r.Backup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("24 小时内两次备份应只做一次快照，实际 %d", len(*got))
	}
	r.mu.Lock()
	r.lastIndex = time.Now().Add(-25 * time.Hour)
	r.mu.Unlock()
	if _, err := r.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Errorf("过了 24 小时应再做一次，实际共 %d", len(*got))
	}
}

// 需求 1.3：快照失败不改变主备份的返回值；失败后一小时内不重试、一小时后重试。
func TestIndexSnapshotFailureDoesNotFailBackup(t *testing.T) {
	r, argvFile, _, _ := snapshotFixture(t, true)
	calls := 0
	r.IndexSnapshot = func(context.Context, string) error { calls++; return errors.New("磁盘满") }
	res, err := r.Backup(context.Background())
	if err != nil || res.SnapshotID != "deadbeef" {
		t.Fatalf("主备份应照常成功，实际 res=%+v err=%v", res, err)
	}
	if li := r.LastIndex(); li == nil || !strings.Contains(li.Error, "磁盘满") {
		t.Errorf("失败原因应进 LastIndex，实际 %+v", li)
	}
	if n := len(invocations(t, argvFile)); n != 1 {
		t.Errorf("快照没生成就不该再调 restic，实际 %d 次", n)
	}
	if _, err := r.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("失败后一小时内不该重试，实际 hook 被调 %d 次", calls)
	}
	r.mu.Lock()
	r.lastIndex = r.lastIndex.Add(-indexRetryGap)
	r.mu.Unlock()
	if _, err := r.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("过了重试间隔应再试，实际 %d 次", calls)
	}
}

// 需求 1.4：关掉开关 → 什么都不做、不记状态；开着但没有句柄 → 记原因、restic 只调一次。
func TestIndexSnapshotSkippedWhenDisabledOrNoHook(t *testing.T) {
	r, argvFile, _, got := snapshotFixture(t, false)
	if _, err := r.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 || r.LastIndex() != nil || len(invocations(t, argvFile)) != 1 {
		t.Errorf("关着时不该做快照、不该记状态、restic 只该调一次：got=%v last=%+v", *got, r.LastIndex())
	}

	r2, argvFile2, _, _ := snapshotFixture(t, true)
	r2.IndexSnapshot = nil
	if _, err := r2.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if li := r2.LastIndex(); li == nil || !strings.Contains(li.Error, "句柄") {
		t.Errorf("没有句柄应记下原因，实际 %+v", li)
	}
	if n := len(invocations(t, argvFile2)); n != 1 {
		t.Errorf("没有快照就不该第二次调 restic，实际 %d", n)
	}
}

// 需求 1.5：快照目录在备份源之下、或包含仓库，都拒绝——两种都是「两边都成功却什么都没备」。
func TestIndexSnapshotDirMustBeOutsideSourcesAndRepo(t *testing.T) {
	r, _, _, got := snapshotFixture(t, true)
	base := r.cfg()
	inside := base
	inside.IndexSnapshotDir = filepath.Join(base.Sources[0].Path, "snapshot")
	r.Cfg = func() Config { return inside }
	if _, err := r.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if li := r.LastIndex(); li == nil || !strings.Contains(li.Error, "之下") || len(*got) != 0 {
		t.Errorf("在源之下应拒绝且不调 hook，实际 last=%+v got=%v", li, *got)
	}

	r2, _, _, got2 := snapshotFixture(t, true)
	b2 := r2.cfg()
	b2.IndexSnapshotDir = filepath.Dir(b2.Repo) // 快照目录包含了仓库
	r2.Cfg = func() Config { return b2 }
	if _, err := r2.Backup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if li := r2.LastIndex(); li == nil || !strings.Contains(li.Error, "仓库") || len(*got2) != 0 {
		t.Errorf("包含仓库应拒绝且不调 hook，实际 last=%+v got=%v", li, *got2)
	}
}
