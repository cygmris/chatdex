package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newMirrorPair 造一对仓：本地备三次（3 个快照），异地只同步了前两次。
//
// 🔴 **两边必须不一致**，这是这组测试的全部意义所在。用「两边一样」的样本，
// 一个恒返回 Behind=0 的实现也能全绿 —— R23 刚在「项目路径取哪个来源」上
// 栽过完全同样的一次（testdata 里两个来源恰好给出同一个答案，断言毫无鉴别力）。
func newMirrorPair(t *testing.T) (*Mirror, string, string) {
	t.Helper()
	bin := findRestic(t)
	dir := t.TempDir()
	localRepo := filepath.Join(dir, "local")
	remoteRepo := filepath.Join(dir, "remote")
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	pass := writePassFile(t)
	ctx := context.Background()

	local := newRunner(Config{
		Repo: localRepo, ResticPath: bin, PasswordFile: pass,
		Sources: []Source{{Path: src, Enabled: true}},
	})
	if err := local.Init(ctx); err != nil {
		t.Fatal(err)
	}
	remote := newRunner(Config{Repo: remoteRepo, ResticPath: bin, PasswordFile: pass})
	if err := remote.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// 本地三个快照，异地只拷前两个（用文件系统 cp 模拟同步，restic 仓是可拷的）
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(src, "a.jsonl"),
			[]byte(strings.Repeat("x", i+1)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := local.Backup(ctx); err != nil {
			t.Fatal(err)
		}
		if i == 1 { // 第二次备份之后同步一次，之后本地又备了一次
			mirrorCopy(t, localRepo, remoteRepo)
		}
	}

	m := &Mirror{Local: local, Cfg: func() MirrorConfig {
		return MirrorConfig{Repo: remoteRepo}
	}}
	return m, localRepo, remoteRepo
}

// mirrorCopy 把本地仓整个拷到异地（顺序无所谓，测试里不并发）。
func mirrorCopy(t *testing.T, from, to string) {
	t.Helper()
	if err := os.RemoveAll(to); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatal(err)
	}
}

// 落后多少要算得准 —— 且必须用两边不一致的样本才证明得了。
func TestMirrorStatusCountsSnapshotsOnlyOnLocal(t *testing.T) {
	m, localRepo, _ := newMirrorPair(t)
	ctx := context.Background()

	st := m.Status(ctx)
	if st.Err != "" {
		t.Fatalf("取状态失败：%s", st.Err)
	}
	if !st.Configured {
		t.Error("Configured = false，而异地仓是配了的")
	}
	if st.Snapshots != 2 {
		t.Errorf("异地快照数 = %d, want 2", st.Snapshots)
	}
	if st.Behind != 1 {
		t.Errorf("Behind = %d, want 1 —— 本地 3 个快照、异地 2 个", st.Behind)
	}

	// 对照：把本地整个再同步一遍，Behind 必须变成 0。
	// 没有这条，一个恒返回 1 的实现也能通过上面的断言。
	mirrorCopy(t, localRepo, m.Cfg().Repo)
	if after := m.Status(ctx); after.Behind != 0 {
		t.Errorf("同步之后 Behind = %d, want 0", after.Behind)
	} else if !after.Configured {
		t.Error("同步之后 Configured 变成 false 了")
	}
}

// 🔴 「没配异地副本」与「配了且是最新的」必须分得开。
//
// 两者的 Behind 都是 0，而含义相反：前者是「机器没了就全没了」，
// 后者是「你受保护」。只看数字分不出来 —— Configured 是唯一的区分信号。
func TestMirrorDistinguishesUnconfiguredFromUpToDate(t *testing.T) {
	m, localRepo, _ := newMirrorPair(t)
	ctx := context.Background()
	mirrorCopy(t, localRepo, m.Cfg().Repo)

	upToDate := m.Status(ctx)
	if upToDate.Behind != 0 || !upToDate.Configured {
		t.Fatalf("同步齐平时应当 Configured=true Behind=0，实得 %+v", upToDate)
	}

	// 换成没配
	none := &Mirror{Local: m.Local, Cfg: func() MirrorConfig { return MirrorConfig{} }}
	st := none.Status(ctx)
	if st.Configured {
		t.Error("没配异地仓时 Configured 应为 false")
	}
	if st.Behind != upToDate.Behind {
		t.Fatalf("对照失效：两种情形的 Behind 不同（%d vs %d），"+
			"这条测试就证明不了「光看 Behind 分不出来」了", st.Behind, upToDate.Behind)
	}
}

// 异地仓连不上时必须报错，**不得**静默显示成「落后 0」。
//
// 「查不到」与「查过了，是齐的」在 Behind 上同形 —— 这正是本项目反复栽的
// 那一族（决策 30/33/35/37）。
func TestMirrorReportsErrorInsteadOfPretendingUpToDate(t *testing.T) {
	m, _, _ := newMirrorPair(t)
	broken := &Mirror{Local: m.Local, Cfg: func() MirrorConfig {
		return MirrorConfig{Repo: filepath.Join(t.TempDir(), "does-not-exist")}
	}}
	st := broken.Status(context.Background())
	if st.Err == "" {
		t.Error("异地仓不存在却没有报错 —— 这会被读成「落后 0」")
	}
	if !st.Configured {
		t.Error("配了但连不上，Configured 仍应为 true（与「没配」是两回事）")
	}
}

// 凭据从 env 文件读，且能容忍 export 前缀与注释。
//
// 用文件而不是 config.json：后者会被 /api/config 下发给界面、也会进备份。
func TestLoadEnvFileHandlesRealWorldShapes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r2.env")
	if err := os.WriteFile(p, []byte(
		"# 注释行\n\nexport AWS_ACCESS_KEY_ID=abc\nAWS_SECRET_ACCESS_KEY=def\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadEnvFile(p)
	want := map[string]bool{"AWS_ACCESS_KEY_ID=abc": true, "AWS_SECRET_ACCESS_KEY=def": true}
	if len(got) != 2 {
		t.Fatalf("读出 %d 条，want 2：%v", len(got), got)
	}
	for _, kv := range got {
		if !want[kv] {
			t.Errorf("读出了意料之外的一条：%q", kv)
		}
	}
	// 读不到就返回 nil，让 restic 自己报「没有凭据」——
	// 伪造空值会让它得到一个看起来像权限不足的 403
	if loadEnvFile(filepath.Join(t.TempDir(), "nope.env")) != nil {
		t.Error("文件不存在时应返回 nil")
	}
}

// 同步顺序里 snapshots 必须最后。
//
// 先传 snapshots，中途断网就留下一个「指向不存在数据」的仓——
// 那种仓 restic snapshots 列得出来，restore 却会失败，比没有更糟。
func TestMirrorOrderPutsSnapshotsLast(t *testing.T) {
	if len(mirrorOrder) == 0 {
		t.Fatal("同步顺序是空的 —— 先怀疑取样")
	}
	if last := mirrorOrder[len(mirrorOrder)-1]; last != "snapshots" {
		t.Errorf("最后一个是 %q, want snapshots", last)
	}
	pos := map[string]int{}
	for i, d := range mirrorOrder {
		pos[d] = i
	}
	for _, d := range []string{"data", "index"} {
		if pos[d] >= pos["snapshots"] {
			t.Errorf("%s 排在 snapshots 之后 —— 快照会引用还没传上去的 pack", d)
		}
	}
	// locks 不该在同步列表里：本机的锁对异地没有意义，
	// 传过去反而会挡住将来直接对异地用 restic
	for _, d := range mirrorOrder {
		if d == "locks" {
			t.Error("locks 进了同步列表")
		}
	}
}

// 🔴 仓库密码绝不能被同步到异地 —— 那等于白加密。
func TestMirrorNeverSyncsThePassword(t *testing.T) {
	m, localRepo, _ := newMirrorPair(t)
	pass := m.Local.Cfg().PasswordFile
	if pass == "" {
		t.Fatal("测试仓没有密码文件 —— 这条断言就没有意义了")
	}
	// 密码文件必须在仓库目录之外：在里面的话，同步 data/ 就会把它带走
	if strings.HasPrefix(pass, localRepo+string(os.PathSeparator)) {
		t.Fatalf("密码文件在仓库目录里（%s）—— 同步会把它一起传到异地", pass)
	}
	// 同步列表里也不能有任何指向密码的项
	for _, d := range mirrorOrder {
		if strings.Contains(d, "pass") || strings.Contains(d, "key") && d != "keys" {
			t.Errorf("同步列表里有可疑项：%q", d)
		}
	}
}

// rclone 目标与凭据：凭据走环境变量，不进命令行（命令行在 /proc 里人人可见）。
func TestRcloneTargetKeepsSecretsOutOfArgs(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "e.env")
	if err := os.WriteFile(envFile,
		[]byte("AWS_ACCESS_KEY_ID=AKID\nAWS_SECRET_ACCESS_KEY=SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, env, err := rcloneTarget(MirrorConfig{
		Repo:    "s3:https://acc.r2.cloudflarestorage.com/bucket/chatdex/repo",
		EnvFile: envFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dst != "mirror:bucket/chatdex/repo" {
		t.Errorf("目标 = %q", dst)
	}
	// 🔴 目标字符串（会进命令行）里绝不能出现凭据
	if strings.Contains(dst, "AKID") || strings.Contains(dst, "SECRET") {
		t.Error("凭据出现在了目标字符串里 —— 它会进命令行，/proc/<pid>/cmdline 人人可读")
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"RCLONE_CONFIG_MIRROR_ENDPOINT=https://acc.r2.cloudflarestorage.com",
		"RCLONE_CONFIG_MIRROR_ACCESS_KEY_ID=AKID", "RCLONE_CONFIG_MIRROR_SECRET_ACCESS_KEY=SECRET"} {
		if !strings.Contains(joined, want) {
			t.Errorf("环境变量里缺 %q", want)
		}
	}
	// 非 s3 地址要明确拒绝，不能默默当成路径
	if _, _, err := rcloneTarget(MirrorConfig{Repo: "/tmp/local"}); err == nil {
		t.Error("非 s3: 地址应当报错")
	}
}
