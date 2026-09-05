// Package config 加载 ~/.config/chatdex/config.json。
//
// 配置文件缺失是正常状态：全部字段都有默认值，chatdex 开箱即用。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cygmris/chatdex/internal/index"
)

// Config 是 chatdex 的全部可调项，对应 design.md 的配置章节。
type Config struct {
	Index   index.Config `json:"index"`
	Scan    Scan         `json:"scan"`
	Summary Summary      `json:"summary"`
	LLM     LLM          `json:"llm"`
	Chat    Chat         `json:"chat"`
	Ports   Ports        `json:"ports"`
	UI      UI           `json:"ui"`
	Backup  Backup       `json:"backup"`

	// Home 是解析器的家目录，仅测试会改。
	Home string `json:"home"`
	// DBPath 是索引库路径。
	DBPath string `json:"db_path"`
}

type Scan struct {
	IntervalSec int `json:"interval_sec"`
	// Roots 缩小扫描范围。空 = 用各解析器的默认会话目录。
	//
	// 非空时只 Walk 这些路径，不在范围内的已索引会话会被标为失效。
	// 每条必须是绝对路径，且必须是真实目录：filepath.WalkDir 不跟进
	// 目录符号链接，把 symlink 填进来等于没扫。
	Roots []string `json:"roots"`
}

type Summary struct {
	Enabled    bool   `json:"enabled"`
	ThrottleMS int    `json:"throttle_ms"`
	Model      string `json:"model"`
	// Window 是生成时间窗口 "HH:MM-HH:MM"，空 = 不限。支持跨零点。
	// 只管生成：窗口外索引与检索照常。
	Window string `json:"window"`
}

// LLM 的 Endpoint 只允许回环地址——会话内容含明文凭证，不留远端逃生口
// （需求 10.5，不可放宽）。校验在 internal/llm 里做，配置层只负责读。
type LLM struct {
	Endpoint string `json:"endpoint"`
	// NumCtx 是上下文窗口（token）。必须显式给：Ollama 默认只有 2048，
	// 与模型自身能力无关，超出部分静默截断，最先丢掉的是开头那句任务指令。
	NumCtx int `json:"num_ctx"`
}

// Backup 是与 restic 对接的配置。
//
// **没有 Password 字段。** 密码只存文件路径——它不该进 chatdex 的配置文件、
// 日志或界面。丢了备份就不可恢复，但那是用户自己的密码管理。
type Backup struct {
	// Repo 是 restic 仓库路径；空 = 未配置，备份功能整体置灰。
	Repo string `json:"repo"`
	// PasswordFile 是密码文件路径（对应 RESTIC_PASSWORD_FILE）。
	// 用文件而不是环境变量传密码：后者会让密码出现在 /proc/<pid>/environ。
	PasswordFile string `json:"password_file"`
	// ResticPath 空 = 从 PATH 找。
	//
	// 可配是必须的而不是灵活性：restic 常装在 ~/.local/bin，而 systemd --user
	// 起的服务其 PATH 未必包含它——写死会造成「命令行能跑、服务跑不了」。
	ResticPath string `json:"restic_path"`
	// Sources 是备份源列表，可任选其一或全部。
	Sources []BackupSource `json:"sources"`
	// AfterScan：每轮增量扫描之后顺手备一次。
	// 实测无变化时 restic 只要 767 ms 且仓库零增长，所以这个开关很便宜。
	AfterScan bool `json:"after_scan"`
	// Mirror 是异地副本（S3/R2 等）的配置；Repo 为空 = 没有异地副本。
	Mirror Mirror `json:"mirror"`
}

// Mirror 是异地副本的配置。
//
// **本地仓是主，异地是它的镜像**，不是把 restic 仓直接放在对象存储上。
// 依据是实测而非偏好：R20 的全快照索引要对每个快照跑一次 restic ls，
// 实测本地 1.06s / R2 3.91s（3.7 倍），689 个快照下是 12 分钟 vs 45 分钟，
// 而快照每 30 分钟涨一个——主仓放远端会让整个产品最慢的一环再慢一倍不止。
type Mirror struct {
	// Repo 是异地 restic 仓地址，如
	// s3:https://<account>.r2.cloudflarestorage.com/<bucket>/<prefix>。
	// 空 = 没有异地副本（与「有但落后 0」是两回事，界面上必须分开显示）。
	Repo string `json:"repo"`
	// EnvFile 是一个 KEY=VALUE 文件，用来传 AWS_ACCESS_KEY_ID /
	// AWS_SECRET_ACCESS_KEY。
	//
	// 🔴 **凭据不进 config.json**：这个文件本身会被 /api/config 读出来给界面，
	// 也会进备份。密钥写在这里等于把它交给所有能看到配置的人。
	// 与 PasswordFile 用文件而不用环境变量是同一条纪律的延伸。
	EnvFile string `json:"env_file"`
	// AfterBackup：每次备份完成后顺手同步一次。
	AfterBackup bool `json:"after_backup"`
	// RclonePath 空 = 从 PATH 找。理由同 ResticPath。
	RclonePath string `json:"rclone_path"`
}

type BackupSource struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type Chat struct {
	Model         string `json:"model"`
	MaxToolRounds int    `json:"max_tool_rounds"`
}

type Ports struct {
	UI  int `json:"ui"`
	API int `json:"api"`
}

// UI 是跟人走的界面偏好（哪套主题）。
// 跟浏览器走的状态（当前明暗、左栏、当前视图）在 localStorage，不进这里。
type UI struct {
	LightTheme string `json:"light_theme"`
	DarkTheme  string `json:"dark_theme"`
	// Highlight 是代码高亮配色。"theme" 表示跟随界面主题（用既有 token 生成），
	// 其余是 highlight.js 的官方配色。
	Highlight string `json:"highlight"`
	// MermaidAuto 决定是否自动渲染图表。默认关——mermaid 有 3.4 MB，
	// 只在用户点击时才动态加载。
	MermaidAuto bool `json:"mermaid_auto"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Index:   index.DefaultConfig(),
		Scan:    Scan{IntervalSec: 30},
		Summary: Summary{Enabled: true, ThrottleMS: 500, Model: "qwen2.5:7b-instruct"},
		LLM:     LLM{Endpoint: "http://127.0.0.1:11434", NumCtx: 32768},
		Chat:    Chat{Model: "qwen2.5:7b-instruct", MaxToolRounds: 8},
		Ports:   Ports{UI: 5021, API: 5022},
		UI:      UI{LightTheme: "desk", DarkTheme: "editor", Highlight: "theme"},
		// 默认列出 chatdex 自己认识的两处，但都不启用——备份要用户明确开启，
		// 不能因为装了 chatdex 就悄悄开始往某处写数据。
		Backup: Backup{
			Sources: []BackupSource{
				{Path: filepath.Join(home, ".claude", "projects")},
				{Path: filepath.Join(home, ".codex", "sessions")},
			},
			// 默认开着。做成默认关的那一版，在真机上**一次都没跑过**——
			// 备份于是从配好那天起就静默停了，而界面上的「已覆盖 3082」
			// 看起来一切正常（实测停了 7 天才被发现）。
			// 代价很小：实测无变化时 restic 只要 767 ms 且仓库零增长。
			//
			// 只对新装生效——已有 config.json 里的显式取值不会被覆盖
			// （Load 是先填默认再用文件覆盖），存量用户靠覆盖率页的
			// 过期告警兜住。
			AfterScan: true,
		},
		Home:   home,
		DBPath: filepath.Join(home, ".local", "share", "chatdex", "index.db"),
	}
}

// Path 是配置文件的默认位置。
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "chatdex", "config.json")
}

// Load 读配置；文件不存在则返回全默认值。
// 文件存在但内容坏了则报错——静默退回默认会让用户以为配置生效了。
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	return cfg, nil
}
