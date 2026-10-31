package search_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cygmris/chatdex/internal/search"
)

func TestParseSynonymsGroupsAndUnion(t *testing.T) {
	s := search.ParseSynonyms(`
# 注释行
hash 提交号 SHA

hash commit
`)
	if got, want := s.Lookup("hash"), []string{"提交号", "SHA", "commit"}; !reflect.DeepEqual(got, want) {
		t.Errorf("hash 的近义词 = %v，期望两组并集 %v", got, want)
	}
	if got, want := s.Lookup("提交号"), []string{"hash", "SHA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("提交号 的近义词 = %v，期望 %v", got, want)
	}
	if got := s.Lookup("注释行"); got != nil {
		t.Errorf("注释被当成了词：%v", got)
	}
}

func TestSynonymsLookupCaseInsensitiveWholeWord(t *testing.T) {
	s := search.ParseSynonyms("hash SHA")
	if got := s.Lookup("HASH"); len(got) != 1 || got[0] != "SHA" {
		t.Errorf("大写查找 = %v", got)
	}
	if got := s.Lookup("sha"); len(got) != 1 || got[0] != "hash" {
		t.Errorf("小写查表里的大写词 = %v", got)
	}
	if got := s.Lookup("has"); got != nil {
		t.Errorf("词的一部分不该扩展：%v", got)
	}
	var nilSyn *search.Synonyms
	if got := nilSyn.Lookup("hash"); got != nil {
		t.Errorf("nil 词表应无近义词：%v", got)
	}
}

func TestSynonymSourceMissingFileUsesBuiltin(t *testing.T) {
	src := &search.SynonymSource{Path: filepath.Join(t.TempDir(), "synonyms.txt")}
	if len(src.Get().Lookup("hash")) == 0 {
		t.Fatal("文件不存在时应退回内置表，内置表里 hash 有近义词")
	}
}

func TestSynonymSourceHotReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "synonyms.txt")
	src := &search.SynonymSource{Path: p}

	if err := os.WriteFile(p, []byte("徽章 badge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := src.Get().Lookup("徽章"); len(got) != 1 || got[0] != "badge" {
		t.Fatalf("第一次读 = %v", got)
	}
	if len(src.Get().Lookup("hash")) == 0 {
		t.Fatal("用户表应与内置表合并，而不是替换")
	}

	if err := os.WriteFile(p, []byte("徽章 badge 角标\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 同一秒内改写时 mtime 可能不变；size 变了也要重读，这里再把 mtime 推后一点以覆盖两条判据
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	if got := src.Get().Lookup("徽章"); len(got) != 2 {
		t.Fatalf("改文件后下一次读应生效，得到 %v", got)
	}

	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got := src.Get().Lookup("徽章"); got != nil {
		t.Fatalf("文件删掉后应只剩内置表，得到 %v", got)
	}
}

func TestSynonymSourceUnreadableFallsBack(t *testing.T) {
	// 路径是目录：stat 成功、读失败 ⇒ 退回内置表，不报错
	src := &search.SynonymSource{Path: t.TempDir()}
	if len(src.Get().Lookup("hash")) == 0 {
		t.Fatal("读失败时应退回内置表")
	}
}
