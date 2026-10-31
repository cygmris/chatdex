package search

import (
	_ "embed"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// 近义词表（spec query-synonym-expansion）。
//
// 同一样东西在会话里常有几种叫法——hash / 提交号 / SHA / commit——字面检索只认其中一个。
// 词表让查询词在检索时扩展成「原词或任一近义词」，索引不动。

//go:embed synonyms_default.txt
var defaultSynonymsText string

// Synonyms 是解析后的词表：小写词 → 同组的其他词（去重，保持表内首次出现的顺序）。
// nil 合法，表示没有任何近义词。
type Synonyms struct{ m map[string][]string }

// ParseSynonyms 解析词表文本：一行一组，组内空白分隔，`#` 起头为注释。
// 同一个词出现在多组时取各组的并集。
func ParseSynonyms(text string) *Synonyms {
	s := &Synonyms{m: map[string][]string{}}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s.addGroup(strings.Fields(line))
	}
	return s
}

func (s *Synonyms) addGroup(words []string) {
	for _, w := range words {
		key := strings.ToLower(w)
		for _, o := range words {
			if strings.EqualFold(o, w) {
				continue
			}
			s.m[key] = appendUnique(s.m[key], o)
		}
	}
}

func appendUnique(list []string, w string) []string {
	for _, x := range list {
		if strings.EqualFold(x, w) {
			return list
		}
	}
	return append(list, w)
}

// Merge 返回 s 与 o 的并集，两者都不被修改。
func (s *Synonyms) Merge(o *Synonyms) *Synonyms {
	out := &Synonyms{m: map[string][]string{}}
	for _, src := range []*Synonyms{s, o} {
		if src == nil {
			continue
		}
		for k, vs := range src.m {
			for _, v := range vs {
				out.m[k] = appendUnique(out.m[k], v)
			}
		}
	}
	return out
}

// Lookup 返回 term 的近义词（不含 term 本身），不区分大小写、按整词查。
func (s *Synonyms) Lookup(term string) []string {
	if s == nil {
		return nil
	}
	return s.m[strings.ToLower(term)]
}

// SynonymSource 是「内置默认表 + 用户文件」的当前词表。
//
// 每次 Get 都 stat 一次用户文件，mtime 变了才重读——改完文件下一次检索即生效，
// 不必重启（与 config.Live「每次用时取」同一条热生效纪律）。
type SynonymSource struct {
	Path string // 空 = 只用默认表

	mu    sync.Mutex
	mtime time.Time
	size  int64
	cur   *Synonyms
	// 读失败日志对同一个 mtime 只记一次，免得每次检索刷一条
	failLogged bool
	failMtime  time.Time
}

var builtinSynonyms = ParseSynonyms(defaultSynonymsText)

func (s *SynonymSource) Get() *Synonyms {
	if s.Path == "" {
		return builtinSynonyms
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := os.Stat(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		s.cur, s.mtime, s.size = nil, time.Time{}, 0
		return builtinSynonyms
	}
	if err == nil && s.cur != nil && st.ModTime().Equal(s.mtime) && st.Size() == s.size {
		return s.cur
	}
	var b []byte
	if err == nil {
		b, err = os.ReadFile(s.Path)
	}
	if err != nil {
		var mt time.Time
		if st != nil {
			mt = st.ModTime()
		}
		if !s.failLogged || !mt.Equal(s.failMtime) {
			slog.Warn("读近义词表失败，只用内置表", "path", s.Path, "err", err)
			s.failLogged, s.failMtime = true, mt
		}
		return builtinSynonyms
	}
	s.cur = builtinSynonyms.Merge(ParseSynonyms(string(b)))
	s.failLogged = false
	s.mtime, s.size = st.ModTime(), st.Size()
	return s.cur
}
