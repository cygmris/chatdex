package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 建议列表只列**确实存在**的路径。
//
// 列一堆这台机器上没有的东西，等于让人自己分辨哪些是真缺口——
// 那就把「chatdex 告诉你漏了什么」变回了「你自己去看」。
func TestSuggestOnlyListsWhatActuallyExists(t *testing.T) {
	home := t.TempDir()
	// 只造两个：一个会话目录、一个 Codex 记忆目录
	for _, rel := range []string{".claude/projects", ".codex/memories"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := Suggest(home, nil)
	if len(got) != 2 {
		var paths []string
		for _, s := range got {
			paths = append(paths, s.Path)
		}
		t.Fatalf("只造了 2 个路径，却列出 %d 条：%v", len(got), paths)
	}
	// 表里有十几条，只有存在的两条该出现——这同时证明了它没把整张表原样吐出来
	for _, s := range got {
		if _, err := os.Stat(s.Path); err != nil {
			t.Errorf("列出了不存在的路径：%s", s.Path)
		}
	}
}

// 已经在备份源里的要标出来，没被覆盖的也要标出来。
//
// 判定必须基于**当前生效的配置**，不是默认值——使用者改过配置之后，
// 拿默认值判断会给出一份与现实无关的清单。
func TestSuggestMarksWhatIsAlreadyCovered(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{".claude/projects", ".codex/memories", ".codex/skills"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sources := []string{filepath.Join(home, ".claude/projects")}

	got := Suggest(home, sources)
	byPath := map[string]Suggestion{}
	for _, s := range got {
		byPath[s.Path] = s
	}
	if s := byPath[filepath.Join(home, ".claude/projects")]; !s.Covered {
		t.Error(".claude/projects 就是备份源本身，应当标为已覆盖")
	}
	for _, rel := range []string{".codex/memories", ".codex/skills"} {
		if s := byPath[filepath.Join(home, rel)]; s.Covered {
			t.Errorf("%s 不在任何源里，却被标成已覆盖", rel)
		}
	}
}

// 含明文凭据的要标出来。
//
// 不标的话就等于替使用者做了「把 API key 备份进去」这个决定。
// restic 会加密，但那是他该自己知情的事。
func TestSuggestFlagsPathsWithSecrets(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 实测本机 ~/.codex/config.toml 里有 BRAVE_API_KEY 与 EXA_API_KEY
	if err := os.WriteFile(filepath.Join(home, ".codex/config.toml"), []byte("x=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude/settings.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Suggest(home, nil)
	var toml, settings *Suggestion
	for i := range got {
		switch filepath.Base(got[i].Path) {
		case "config.toml":
			toml = &got[i]
		case "settings.json":
			settings = &got[i]
		}
	}
	if toml == nil || !toml.Secrets {
		t.Error("~/.codex/config.toml 含明文 API key，必须标出来")
	}
	// 对照：settings.json 实测干净（无 env 块、无密钥形状），不该被误标。
	// 没有这条对照，上面那句无法排除「所有项都被标成含凭据」。
	if settings == nil || settings.Secrets {
		t.Error("~/.claude/settings.json 实测干净，不该标成含凭据")
	}
}

// 按「丢了有多要命」排，不按路径字母序。
//
// 字母序会把 `.claude/agents` 排在 `.codex/memories` 前面，
// 而后者才是丢了没救的那个（git 仓但通常无远端）。
func TestSuggestSortsByHowBadlyItHurts(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{".claude/agents", ".claude/commands", ".codex/memories"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := Suggest(home, nil)
	if len(got) == 0 {
		t.Fatal("一条都没列出来")
	}
	if filepath.Base(got[0].Path) != "memories" {
		var order []string
		for _, s := range got {
			order = append(order, filepath.Base(s.Path))
		}
		t.Errorf("最要命的 .codex/memories 应当排第一，实得顺序 %v", order)
	}
}

// 派生数据一律不进这张表。
//
// 备份只该存**原件**——这与「索引库 index.db 不进备份源」是同一条纪律。
// 单独维护一份 knownExcluded 并在这里对照，是为了让「排除」本身可被检验：
// 否则将来有人顺手加一条 `~/.claude/cache`，没有任何东西会拦住他。
func TestKnownTableHasNoDerivedData(t *testing.T) {
	for _, k := range knownPaths {
		for bad, why := range knownExcluded {
			if k.Rel == bad {
				t.Errorf("表里出现了派生数据 %s —— %s", bad, why)
			}
		}
		// sqlite 的 WAL 旁路文件更是绝对不能单独备
		for _, suffix := range []string{"-wal", "-shm"} {
			if strings.HasSuffix(k.Rel, suffix) {
				t.Errorf("表里出现了 %s 文件：%s —— 单独备它没有意义", suffix, k.Rel)
			}
		}
	}
	if len(knownExcluded) == 0 {
		t.Error("排除清单是空的 —— 那这条断言就成了摆设")
	}
}

// 路径包含判定：六种关系各验一次。
//
// 与 repoInsideSources 那组同一形状——前缀相同但不是父目录，是最容易错的。
func TestCoversHandlesEveryPathRelation(t *testing.T) {
	for _, c := range []struct {
		name string
		src  string
		p    string
		want bool
	}{
		{"就是源本身", "/h/.claude/projects", "/h/.claude/projects", true},
		{"在源之下", "/h/.claude", "/h/.claude/skills", true},
		{"深层嵌套", "/h/.claude", "/h/.claude/projects/x/memory", true},
		{"前缀相同但不是父目录", "/h/.claude", "/h/.claudex", false},
		{"同级", "/h/.claude", "/h/.codex", false},
		{"末尾斜杠不影响", "/h/.claude/", "/h/.claude/skills", true},
	} {
		if got := covers(c.src, c.p); got != c.want {
			t.Errorf("%s：covers(%q, %q) = %v, want %v", c.name, c.src, c.p, got, c.want)
		}
	}
}

// home 取不到时返回空，不报错——与「可选依赖不可用就降级」同一模式。
func TestSuggestDegradesWithoutHome(t *testing.T) {
	if got := Suggest("", nil); got != nil {
		t.Errorf("home 为空时应当返回 nil，实得 %v", got)
	}
}
