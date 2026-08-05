package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// 取回的内容必须与备份时逐字节相同（需求 4.3），且必须来自**最后一个**
// 含该文件的快照。
//
// 后半句是这个测试真正在防的东西：一个会话在被删掉之前会被备份很多次，
// 每次内容都不同（会话在增长）。取错快照 = 拿回一个更早的、不完整的版本，
// 而且**看起来完全正常**——不比对内容根本发现不了。
//
// 用合成的源目录而不是真实会话：本机那 183 个已消失的会话全都消失在
// 第一次备份之前，备份里一个都没有；而为了造一个就去删用户的真实文件
// 是绝对不行的。
func TestFetchReturnsLatestVersionByteForByte(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(src, "grows.jsonl")

	r := newRunner(Config{
		Repo: filepath.Join(dir, "repo"), ResticPath: bin, PasswordFile: writePassFile(t),
		Sources: []Source{{Path: src, Enabled: true}},
	})
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// 三次快照：内容一路增长，最后一次删掉它（模拟日常清理）
	v1 := []byte(`{"seq":1,"body":"第一轮"}` + "\n")
	v2 := append(append([]byte{}, v1...), []byte(`{"seq":2,"body":"第二轮，更长的内容"}`+"\n")...)
	for _, content := range [][]byte{v1, v2} {
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Backup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backup(ctx); err != nil {
		t.Fatal(err)
	}

	rc, err := r.Fetch(ctx, target)
	if err != nil {
		t.Fatalf("源文件已删除，但备份里有，应当能取回：%v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("dump 退出异常：%v", err)
	}
	if string(got) != string(v2) {
		t.Errorf("取回的不是最后一个版本。\n实得 %q\n应为 %q", got, v2)
	}
}

// 备份里也没有时要明确告知，不能显示空白或假装还在（需求 4.4）。
func TestFetchSaysSoWhenNotInBackup(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRunner(Config{
		Repo: filepath.Join(dir, "repo"), ResticPath: bin, PasswordFile: writePassFile(t),
		Sources: []Source{{Path: src, Enabled: true}},
	})
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backup(ctx); err != nil {
		t.Fatal(err)
	}

	_, err := r.Fetch(ctx, filepath.Join(src, "从来没备过.jsonl"))
	// 必须是可识别的那一种错误：上层要靠它区分「备份里没有」（告诉用户
	// 这个会话真的没了）与「restic 挂了」（告诉用户去修备份）
	if !errors.Is(err, ErrNotInBackup) {
		t.Errorf("备份里没有该文件时应当返回 ErrNotInBackup，实得：%v", err)
	}
}

// 取回不得写回源目录——只读铁律对恢复同样成立（需求 4.5）。
//
// 这条错了不会有任何报错：restic dump 到 stdout 是对的，dump 到文件也「成功」，
// 区别只在于用户的源目录被悄悄改了。所以只能靠断言目录没变来守。
func TestFetchNeverWritesBackToSource(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(src, "gone.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRunner(Config{
		Repo: filepath.Join(dir, "repo"), ResticPath: bin, PasswordFile: writePassFile(t),
		Sources: []Source{{Path: src, Enabled: true}},
	})
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	rc, err := r.Fetch(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(rc)
	rc.Close()

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("取回把文件写回了源目录 —— 只读铁律对恢复同样成立")
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		names := make([]string, len(ents))
		for i, e := range ents {
			names[i] = e.Name()
		}
		t.Errorf("取回在源目录里留下了东西：%v", names)
	}
}
