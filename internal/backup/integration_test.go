package backup

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 用真 restic + 临时仓库跑一遍：init → backup → snapshots。
//
// 备份是「错了就完蛋」的功能，只测解析函数不够——命令行拼装、环境变量、
// 退出码处理这几处任何一个错了，单测都发现不了。
func TestRealResticRoundTrip(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("会话内容一二三\n")
	if err := os.WriteFile(filepath.Join(src, "a.jsonl"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	r := newRunner(Config{
		Repo: repo, ResticPath: bin, PasswordFile: writePassFile(t),
		Sources: []Source{{Path: src, Enabled: true}},
	})
	ctx := context.Background()

	// 初始化前应当判为不可用，且原因要能与「没装 restic」区分开
	if st := r.Available(ctx); st.Available || st.Version == "" {
		t.Errorf("未初始化时：应不可用但能报出 restic 版本，实得 %+v", st)
	}
	if err := r.Init(ctx); err != nil {
		t.Fatalf("初始化仓库失败：%v", err)
	}
	if st := r.Available(ctx); !st.Available || !st.RepoReady {
		t.Fatalf("初始化后应当可用，实得 %+v", st)
	}

	res, err := r.Backup(ctx)
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	if res.SnapshotID == "" {
		t.Error("没拿到快照 id")
	}
	if res.FilesNew < 1 {
		t.Errorf("应当至少备了 1 个新文件，实得 %+v", res)
	}

	snaps, err := r.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("应当有 1 个快照，实得 %d", len(snaps))
	}
	if !strings.HasPrefix(snaps[0].ID, res.SnapshotID[:8]) {
		t.Errorf("快照 id 对不上：列表 %s vs 备份返回 %s", snaps[0].ID, res.SnapshotID)
	}
	if snaps[0].Time.IsZero() {
		t.Error("快照时间没解析出来")
	}

	// 第二次备份：内容没变，不该产生新文件
	res2, err := r.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res2.FilesNew != 0 {
		t.Errorf("内容没变时不该有新文件，实得 %+v", res2)
	}
}

// 没勾任何源时要明确报错，而不是跑一条没有路径的 restic 命令
// （那会备当前工作目录——一个静默且后果很大的错误）。
func TestBackupWithoutSourcesFails(t *testing.T) {
	bin := findRestic(t)
	r := newRunner(Config{Repo: t.TempDir(), ResticPath: bin, PasswordFile: writePassFile(t)})
	_, err := r.Backup(context.Background())
	if err == nil {
		t.Fatal("没勾选任何源时应当报错")
	}
	// 光断言「有报错」不够：restic 自己也会因参数不足报错，
	// 那样区分不出我们到底有没有在调用它**之前**就拦住。要断言是我们的错误。
	if !strings.Contains(err.Error(), "勾选") {
		t.Errorf("应当在调用 restic 之前拦住并说明原因，实得：%v", err)
	}
}

// 覆盖率必须三分类，而不是「备了 / 没备」两分类。
//
// 「源已消失但备份里有」那一类是本功能的价值证明——实测本机已有 183 个
// 会话的源文件消失（日常清理）。把它混进「已覆盖」就看不见了。
func TestCoverageThreeWaySplit(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(src, "kept.jsonl")
	gone := filepath.Join(src, "gone.jsonl")
	for _, p := range []string{kept, gone} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := newRunner(Config{
		Repo: repo, ResticPath: bin, PasswordFile: writePassFile(t),
		Sources: []Source{{Path: src, Enabled: true}},
	})
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// 备之前：全部未覆盖，且不该报错（「还没备过」不是错误）
	sessions := []IndexedSession{{Path: kept, Alive: true}, {Path: gone, Alive: true}}
	cov, err := r.Coverage(ctx, sessions)
	if err != nil {
		t.Fatalf("还没有快照时不该报错：%v", err)
	}
	if cov.MissingTotal != 2 || cov.CoveredTotal != 0 {
		t.Errorf("备份前应当全部未覆盖，实得 %+v", cov)
	}

	if _, err := r.Backup(ctx); err != nil {
		t.Fatal(err)
	}

	// 备之后：全部已覆盖
	cov, err = r.Coverage(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if cov.CoveredTotal != 2 || cov.MissingTotal != 0 || cov.RescuedTotal != 0 {
		t.Errorf("备份后应当全覆盖，实得 %+v", cov)
	}
	if cov.SnapshotID == "" {
		t.Error("没记下校验依据的快照 id")
	}

	// 源文件消失（模拟日常清理）→ 归入「源已消失但备份里有」这一类
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	cov, err = r.Coverage(ctx, []IndexedSession{
		{Path: kept, Alive: true}, {Path: gone, Alive: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cov.RescuedTotal != 1 || cov.CoveredTotal != 1 {
		t.Errorf("消失的会话应当归入 rescued，实得 %+v", cov)
	}
	if len(cov.Rescued) != 1 || cov.Rescued[0].Path != gone {
		t.Errorf("rescued 清单不对：%+v", cov.Rescued)
	}

	// 索引里有、备份里没有的 → missing
	cov, err = r.Coverage(ctx, append(sessions, IndexedSession{Path: filepath.Join(src, "never.jsonl"), Alive: true}))
	if err != nil {
		t.Fatal(err)
	}
	if cov.MissingTotal != 1 {
		t.Errorf("没备过的会话应当算 missing，实得 %+v", cov)
	}
}

// 「没备但源还在」与「没备且源已没」必须分得开。
//
// 前者是「去设置里勾上就好」，后者是**永久丢失，谁也救不回来**。
// 实测首次备份后这两个数是 0 和 183——只报一个 MissingTotal，
// 等于把「已经丢了 183 个」说成「你漏备了 183 个」。
//
// 顺带钉死 LostTotal 是 Missing 的**子集**：三类相加必须仍等于总数，
// 否则以后有人把它改成第四类，界面上的数就会对不上。
func TestLostIsCountedAndIsASubsetOfMissing(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	r := newRunner(Config{
		Repo: filepath.Join(dir, "repo"), ResticPath: bin, PasswordFile: writePassFile(t),
	})
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// 条数都超过 coverageLimit：拿返回条数当总数会被当场抓住
	const live, dead = coverageLimit + 7, coverageLimit + 3
	var sessions []IndexedSession
	for i := 0; i < live; i++ {
		sessions = append(sessions, IndexedSession{Path: filepath.Join(dir, "live", strconv.Itoa(i)+".jsonl"), Alive: true})
	}
	for i := 0; i < dead; i++ {
		sessions = append(sessions, IndexedSession{Path: filepath.Join(dir, "dead", strconv.Itoa(i)+".jsonl")})
	}

	cov, err := r.Coverage(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if cov.LostTotal != dead {
		t.Errorf("永久丢失 = %d, want %d（源已消失且备份里也没有的条数）", cov.LostTotal, dead)
	}
	if cov.MissingTotal != live+dead {
		t.Errorf("未覆盖 = %d, want %d（lost 是它的子集，不是第四类）", cov.MissingTotal, live+dead)
	}
	if sum := cov.CoveredTotal + cov.MissingTotal + cov.RescuedTotal; sum != len(sessions) {
		t.Errorf("三类相加 = %d, want %d —— lost 被算成第四类了", sum, len(sessions))
	}
}

// 总数必须单独算，不能用返回条数——限量返回时拿 len() 当总数
// 会永远显示 limit（R8 的 Children 栽过同一处）。
func TestCoverageTotalIsNotPageSize(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	r := newRunner(Config{
		Repo: filepath.Join(dir, "repo"), ResticPath: bin, PasswordFile: writePassFile(t),
	})
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	var sessions []IndexedSession
	for i := 0; i < coverageLimit*3; i++ {
		sessions = append(sessions, IndexedSession{Path: filepath.Join(dir, "s", string(rune('a'+i%26))+string(rune('a'+i/26))+".jsonl"), Alive: true})
	}
	cov, err := r.Coverage(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if cov.MissingTotal != len(sessions) {
		t.Errorf("总数 = %d, want %d", cov.MissingTotal, len(sessions))
	}
	if len(cov.Missing) != coverageLimit {
		t.Errorf("返回条数 = %d, want %d（限量）", len(cov.Missing), coverageLimit)
	}
}
