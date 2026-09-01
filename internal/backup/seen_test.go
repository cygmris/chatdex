package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// memSeen 是内存里的 SeenStore，顺便数一数每个快照被扫了几次。
type memSeen struct {
	paths   map[string]bool
	scanned map[string]bool
	calls   map[string]int // snapshot_id → 被记录次数
}

func newMemSeen() *memSeen {
	return &memSeen{paths: map[string]bool{}, scanned: map[string]bool{}, calls: map[string]int{}}
}

func (m *memSeen) ScannedSnapshots() (map[string]bool, error) {
	out := map[string]bool{}
	for k := range m.scanned {
		out[k] = true
	}
	return out, nil
}

func (m *memSeen) RecordSnapshotFiles(id string, paths []string) error {
	m.calls[id]++
	m.scanned[id] = true
	for _, p := range paths {
		m.paths[p] = true
	}
	return nil
}

// 造一个「文件先在、后被删掉」的仓库：老快照里有它，最新快照里没有。
//
// 这正是真机上 1973 个「永久丢失」的形状 —— 而它们里 86% 其实还在旧快照里。
func seedVanished(t *testing.T) (*Runner, string, string) {
	t.Helper()
	bin := findRestic(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(src, "keep.jsonl")
	gone := filepath.Join(src, "gone.jsonl")
	for _, p := range []string{keep, gone} {
		if err := os.WriteFile(p, []byte("内容\n"), 0o644); err != nil {
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
	// 快照 1：两个文件都在
	if _, err := r.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	// 删掉一个，快照 2：只剩 keep
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	return r, keep, gone
}

// 消失的文件必须能在并集索引里找到 —— 而它**不在最新快照里**。
//
// 配了一条旧口径对照：同一套数据只看最新快照就找不到它。没有这条对照，
// 这个断言证明不了「跨快照」真的发生了（一个恒返回 true 的实现也能通过）。
func TestSeenScannerFindsFilesOnlyInOlderSnapshots(t *testing.T) {
	r, keep, gone := seedVanished(t)
	ctx := context.Background()
	store := newMemSeen()

	if err := NewSeenScanner(r, store, nil).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if !store.paths[gone] {
		t.Errorf("并集索引里没有 %s —— 它只存在于旧快照，而这正是本期要能找到的那一类", gone)
	}
	if !store.paths[keep] {
		t.Errorf("并集索引里没有 %s", keep)
	}

	// 🔴 旧口径对照：只看最新快照，那个消失的文件找不到。
	// 这条一旦也能找到，说明测试数据没造对（文件没真的从最新快照里消失），
	// 上面那条断言就失去了鉴别力。
	snaps, err := r.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := r.filesInSnapshot(ctx, snaps[len(snaps)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest[gone] {
		t.Fatal("对照失败：最新快照里还有 gone.jsonl —— 测试数据没造出「已消失」的形状")
	}
	if !latest[keep] {
		t.Fatal("对照失败：最新快照里没有 keep.jsonl")
	}
}

// 已经扫过的快照不重复扫 —— 566 个快照 × 1 秒，重扫一遍就是白等 9 分钟。
func TestSeenScannerIsIncremental(t *testing.T) {
	r, _, _ := seedVanished(t)
	ctx := context.Background()
	store := newMemSeen()
	sc := NewSeenScanner(r, store, nil)

	if err := sc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	first := map[string]int{}
	for k, v := range store.calls {
		first[k] = v
	}
	if len(first) != 2 {
		t.Fatalf("首轮应扫 2 个快照，实得 %d", len(first))
	}

	if err := sc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	for id, n := range store.calls {
		if n != first[id] {
			t.Errorf("快照 %s 被扫了 %d 次（首轮 %d 次）—— 增量失效了", id[:8], n, first[id])
		}
	}
}

// 进度要能报，且「扫完了」与「还没扫」必须分得开。
func TestSeenScannerProgress(t *testing.T) {
	r, _, _ := seedVanished(t)
	ctx := context.Background()
	store := newMemSeen()
	sc := NewSeenScanner(r, store, nil)

	p, err := sc.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Scanned != 0 || p.Total != 2 || p.Ready {
		t.Errorf("扫之前应是 0/2 且未就绪，实得 %+v", p)
	}

	if err := sc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	p, err = sc.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Scanned != 2 || p.Total != 2 || !p.Ready {
		t.Errorf("扫完应是 2/2 且就绪，实得 %+v", p)
	}
}

// testSeen / testProgress 给既有测试提供真实的并集查询与进度。
//
// 测试仓里快照只有个位数，直接现扫；生产上走索引表（9.4 分钟不可能现扫）。
func testSeen(t *testing.T, r *Runner) func([]string) (map[string]bool, error) {
	t.Helper()
	return func(paths []string) (map[string]bool, error) {
		store := newMemSeen()
		if err := NewSeenScanner(r, store, nil).Scan(context.Background()); err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, p := range paths {
			if store.paths[p] {
				out[p] = true
			}
		}
		return out, nil
	}
}

func testProgress(t *testing.T, r *Runner) SeenProgress {
	t.Helper()
	store := newMemSeen()
	sc := NewSeenScanner(r, store, nil)
	if err := sc.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := sc.Progress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 只活在旧快照里的会话，必须归入 Rescued 而不是 Lost。
//
// 这是本期的核心命题。**配了旧口径对照**：同一套数据用「只看最新快照」的
// 判定会把它算成 Lost —— 没有这条对照，一个恒把所有消失会话都算 Rescued 的
// 实现也能通过上面的断言。
func TestCoverageCountsVanishedFilesFromOlderSnapshots(t *testing.T) {
	r, keep, gone := seedVanished(t)
	ctx := context.Background()
	sessions := []IndexedSession{
		{Path: keep, Alive: true},
		{Path: gone, Alive: false}, // 源已消失
	}

	cov, err := r.Coverage(ctx, sessions, testSeen(t, r), testProgress(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if cov.RescuedTotal != 1 {
		t.Errorf("RescuedTotal = %d, want 1 —— 它只在旧快照里，但确实取得回来", cov.RescuedTotal)
	}
	if cov.LostTotal != 0 {
		t.Errorf("LostTotal = %d, want 0 —— 「永久丢失」只该包含任何快照里都没有的", cov.LostTotal)
	}
	if cov.CoveredTotal != 1 {
		t.Errorf("CoveredTotal = %d, want 1", cov.CoveredTotal)
	}

	// 🔴 旧口径对照：并集查询恒返回空（＝只看最新快照的效果），同一套数据
	// 会把它算成永久丢失。这条证明上面的断言真的在验「跨快照」。
	empty := func([]string) (map[string]bool, error) { return map[string]bool{}, nil }
	old, err := r.Coverage(ctx, sessions, empty, SeenProgress{Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if old.RescuedTotal != 0 || old.LostTotal != 1 {
		t.Fatalf("对照失败：旧口径本该报 rescued=0 lost=1，实得 rescued=%d lost=%d"+
			" —— 说明这两条断言分不出新旧口径", old.RescuedTotal, old.LostTotal)
	}
}

// 并集索引没建完时，不许给出「永久丢失」这个结论。
//
// 「还没查完」与「查过了，没有」在数字上完全同形 —— 这正是本期要修的那个谎，
// 而它最容易以「先给个数总比没有强」的形式复活。
func TestCoverageWithholdsLostUntilIndexReady(t *testing.T) {
	r, keep, gone := seedVanished(t)
	ctx := context.Background()
	sessions := []IndexedSession{{Path: keep, Alive: true}, {Path: gone, Alive: false}}
	empty := func([]string) (map[string]bool, error) { return map[string]bool{}, nil }

	notReady, err := r.Coverage(ctx, sessions, empty, SeenProgress{Scanned: 1, Total: 2, Ready: false})
	if err != nil {
		t.Fatal(err)
	}
	if notReady.LostTotal != 0 {
		t.Errorf("索引没建完时 LostTotal = %d, want 0 —— 没查完就不构成「永久丢失」这个结论",
			notReady.LostTotal)
	}
	if notReady.Ready || notReady.Scanned != 1 || notReady.Total != 2 {
		t.Errorf("进度没有随结论下发：%+v", notReady.SeenProgress)
	}
	// MissingTotal 照常给：那说的是「备份里没有」，与「永久丢失」不是一回事
	if notReady.MissingTotal != 1 {
		t.Errorf("MissingTotal = %d, want 1", notReady.MissingTotal)
	}

	// 对照：同样的数据，标为已建完就该给出结论
	ready, err := r.Coverage(ctx, sessions, empty, SeenProgress{Scanned: 2, Total: 2, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if ready.LostTotal != 1 {
		t.Fatalf("对照失败：索引建完后 LostTotal 应为 1，实得 %d —— 这两条分不出就绪与否",
			ready.LostTotal)
	}
}
