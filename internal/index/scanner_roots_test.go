package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWalkDirDoesNotFollowDirectorySymlink(t *testing.T) {
	s, home := newScanner(t)

	realDir := filepath.Join(home, ".claude/projects/-real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(realDir, "aaa.jsonl")
	writeFile(t, realFile, userLine("2026-07-28T10:00:00.000Z", "真实目录里的会话"))

	hidden := t.TempDir()
	hiddenFile := filepath.Join(hidden, "bbb.jsonl")
	writeFile(t, hiddenFile, userLine("2026-07-28T10:00:00.000Z", "只在 symlink 目标里"))

	link := filepath.Join(home, ".claude/projects/-via-link")
	if err := os.Symlink(hidden, link); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(home, ".claude/projects/-broken")
	if err := os.Symlink(filepath.Join(home, "no-such-chatdex-target"), broken); err != nil {
		t.Fatal(err)
	}

	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatalf("失效 symlink 不该让整轮扫描失败: %v", err)
	}
	if n := countBlocks(t, s, realFile); n != 1 {
		t.Errorf("真实目录应被索引, blocks=%d report=%+v", n, rep)
	}
	var sessions int
	if err := s.Store.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Errorf("目录 symlink 被跟进去了，会话数=%d want 1", sessions)
	}
}

func TestScanRootsLimitsWalk(t *testing.T) {
	s, home := newScanner(t)
	a := filepath.Join(home, ".claude/projects/-a")
	b := filepath.Join(home, ".claude/projects/-b")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	af := filepath.Join(a, "a.jsonl")
	bf := filepath.Join(b, "b.jsonl")
	writeFile(t, af, userLine("2026-07-28T10:00:00.000Z", "项目A"))
	writeFile(t, bf, userLine("2026-07-28T10:00:00.000Z", "项目B"))

	s.ScanRoots = []string{a}
	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if n := countBlocks(t, s, af); n != 1 {
		t.Errorf("scan.roots 内的真实目录应被索引, blocks=%d report=%+v", n, rep)
	}
	if n := countBlocks(t, s, bf); n != 0 {
		t.Errorf("不在 scan.roots 里的项目不该被索引, blocks=%d", n)
	}
}

func TestScanRootSymlinkIsNotFollowed(t *testing.T) {
	s, home := newScanner(t)
	real := filepath.Join(home, ".claude/projects/-real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(real, "aaa.jsonl"), userLine("2026-07-28T10:00:00.000Z", "真实目录"))

	link := filepath.Join(home, "link-to-real")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s.ScanRoots = []string{link}
	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesIndexed != 0 || rep.FilesSeen != 0 {
		t.Errorf("把目录 symlink 当 scan.roots 不该索引到东西: %+v", rep)
	}
}

func TestScanRootsMarksOutOfRangeDead(t *testing.T) {
	s, home := newScanner(t)
	a := filepath.Join(home, ".claude/projects/-a")
	b := filepath.Join(home, ".claude/projects/-b")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a, "a.jsonl"), userLine("2026-07-28T10:00:00.000Z", "项目A"))
	writeFile(t, filepath.Join(b, "b.jsonl"), userLine("2026-07-28T10:00:00.000Z", "项目B"))
	if _, err := s.ScanOnce(); err != nil {
		t.Fatal(err)
	}

	s.ScanRoots = []string{a}
	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.MarkedDead != 1 {
		t.Errorf("缩小 scan.roots 后，范围外的已索引会话应被标失效: %+v", rep)
	}
	var alive int
	if err := s.Store.DB().QueryRow(`SELECT alive FROM sessions WHERE file_path = ?`, filepath.Join(b, "b.jsonl")).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 0 {
		t.Error("范围外会话未被标记为失效")
	}
}
