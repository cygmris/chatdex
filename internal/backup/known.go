package backup

import (
	"os"
	"path/filepath"
	"sort"
)

// 「你漏备了什么」——这是覆盖率校验的另一半。
//
// 现有的 Coverage 回答的是「**我索引过的会话**备份里有没有」。但使用者
// 真正要问的是「**我这台机器上 agent 的东西**备了没有」，而那不止会话：
// 全局指令、自建 skill、subagent、钩子、以及 Codex 的记忆。
//
// restic 只看得见路径，它不知道 `~/.codex/memories` 是什么。chatdex 知道
// agent 的数据长什么样，所以这件事只有 chatdex 能做——与决策 28 划的分工
// 是同一条推理。
//
// 触发它的是一次真实的漏备：使用者手工配了两个会话目录就以为齐了，
// 实测本机 12 项该备没备，其中 `~/.codex/memories`（152 个文件）
// 虽是 git 仓却只有 1 条提交、没有远端——git 在那里等于零保护。

// KnownPath 是一条「agent 会长期保存、丢了要命」的路径。
//
// **这张表是数据不是逻辑**：加一项只改表，不改下面的判定代码。
type KnownPath struct {
	// Agent 是它属于谁，只用于分组展示。
	Agent string
	// Rel 是相对 home 的路径。
	Rel string
	// What 说清「它是什么、丢了会怎样」。
	// 只列一个路径让使用者自己猜，等于没说（需求 3.6）。
	What string
	// Secrets 表示这个文件里有明文凭据。
	//
	// 有的话必须在界面上标出来，不能无声建议备份进去——restic 会加密，
	// 但那是使用者该自己做的决定，不是我们替他做的。
	Secrets bool
	// Priority 越小越要命，决定展示顺序（需求：按丢了有多要命排，
	// 不按路径字母序）。
	Priority int
}

// knownPaths 是全部候选。
//
// **刻意不列派生数据**——缓存、历史、可从别处重建的 sqlite。这与
// 「索引库 index.db 不进备份源」是同一条纪律：备份只该存原件。
// 具体被排除的与理由见 knownExcluded（那份清单有测试守着）。
var knownPaths = []KnownPath{
	// —— 会话（默认就在源里，列出来是为了让人看见「这些是覆盖了的」）
	{"Claude Code", ".claude/projects", "全部会话，以及每个项目的 memory/", false, 10},
	{"Codex", ".codex/sessions", "全部会话", false, 11},

	// —— 最要命的那一个
	{"Codex", ".codex/memories", "Codex 的记忆：MEMORY.md、按会话的 rollout_summaries。" +
		"它虽是 git 仓，但通常只有本地提交、没有远端——丢了就真没了", false, 20},

	// —— 你写的东西：丢了要重写
	{"Claude Code", ".claude/CLAUDE.md", "全局指令（跨全部项目生效）", false, 30},
	{"Codex", ".codex/AGENTS.md", "全局指令", false, 31},
	{"Claude Code", ".claude/skills", "自建 skill", false, 40},
	{"Codex", ".codex/skills", "自建 skill", false, 41},
	{"Claude Code", ".claude/agents", "自建 subagent", false, 50},
	{"Codex", ".codex/agents", "自建 subagent", false, 51},
	{"Claude Code", ".claude/commands", "自建 slash command", false, 60},
	{"Claude Code", ".claude/hooks", "钩子脚本", false, 61},

	// —— 配置：重装能重配，但会很烦
	{"Claude Code", ".claude/settings.json", "设置（模型、钩子、插件开关）", false, 70},
	{"Codex", ".codex/config.toml", "配置。⚠️ 这个文件里通常有明文 API key", true, 71},
}

// knownExcluded 是**刻意不备**的东西，连同理由。
//
// 单独列出来而不是「不写进 knownPaths 就完了」，是为了让排除本身可被检验：
// 有测试断言 knownPaths 里不出现这些前缀。否则将来有人顺手加一条
// `~/.claude/cache`，没有任何东西会拦住他。
var knownExcluded = map[string]string{
	".claude/cache":                  "缓存，可重建",
	".claude/file-history":           "编辑历史，448 MB，可从 git 与会话还原",
	".claude/shell-snapshots":        "每次会话的 shell 快照，派生物",
	".codex/cache":                   "缓存，可重建",
	".codex/memories_1.sqlite":       "记忆流水线的工作队列与中间产物，可从 memories/ 的 markdown 与会话重建；且它是 WAL 模式，只备 .sqlite 不带 -wal/-shm 会拿到撕裂的副本",
	".codex/logs_2.sqlite":           "日志，247 MB，派生物",
	".codex/thread_history_1.sqlite": "会话历史的 sqlite 副本，原件在 sessions/",
}

// Suggestion 是一条建议，带上它当前是否已被覆盖。
type Suggestion struct {
	Path    string `json:"path"`
	Agent   string `json:"agent"`
	What    string `json:"what"`
	Secrets bool   `json:"secrets"`
	Covered bool   `json:"covered"`
}

// covers 判断路径 p 是否落在备份源 src 之下（或就是它）。
//
// **只有这一处实现**：前端不自己判路径包含关系，否则同一条规则两处写，
// 改一处就漂移（R13 的教训）。
func covers(src, p string) bool {
	src = filepath.Clean(src)
	p = filepath.Clean(p)
	if src == p {
		return true
	}
	return len(p) > len(src)+1 && p[:len(src)] == src && p[len(src)] == filepath.Separator
}

// Suggest 列出这台机器上**确实存在**的已知路径，并标出哪些还没被备份源覆盖。
//
// **不递归遍历**：`~/.claude` 下有 5.2 GB，走一遍要几秒钟，而判断
// 「这个路径在不在」只需要一次 os.Stat（需求：耗时要有上界）。
func Suggest(home string, sources []string) []Suggestion {
	if home == "" {
		return nil
	}
	out := make([]Suggestion, 0, len(knownPaths))
	for _, k := range knownPaths {
		p := filepath.Join(home, k.Rel)
		if _, err := os.Stat(p); err != nil {
			// 不存在、或读不了 —— 都当作没有。不猜。
			continue
		}
		s := Suggestion{Path: p, Agent: k.Agent, What: k.What, Secrets: k.Secrets}
		for _, src := range sources {
			if covers(src, p) {
				s.Covered = true
				break
			}
		}
		out = append(out, s)
	}
	// 按 Priority 排：先给最要命的。按路径字母序排会把
	// `.claude/agents` 排在 `.codex/memories` 前面，而后者才是丢了没救的。
	prio := map[string]int{}
	for _, k := range knownPaths {
		prio[filepath.Join(home, k.Rel)] = k.Priority
	}
	sort.SliceStable(out, func(i, j int) bool { return prio[out[i].Path] < prio[out[j].Path] })
	return out
}
