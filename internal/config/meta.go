package config

// 配置项的元信息**只在这里声明一次**，经 /api/config 下发给前端渲染表单。
//
// 前端再写一份必然漂移：加了配置项忘了同步，界面上就少一格，
// 而少的那一格没有任何东西会报错。

// FieldMeta 描述一个配置项。
type FieldMeta struct {
	Key     string   `json:"key"` // 点分路径，如 summary.model
	Label   string   `json:"label"`
	Help    string   `json:"help"`
	Kind    string   `json:"kind"`           // string | int | bool | enum | bytes
	Hot     bool     `json:"hot"`            // 保存后立即生效？false = 需重启
	Note    string   `json:"note,omitempty"` // 额外提醒（如「对历史内容需重建索引」）
	Min     int64    `json:"min,omitempty"`
	Max     int64    `json:"max,omitempty"`
	Options []string `json:"options,omitempty"` // enum；模型列表在运行时填充
	// Optional 表示空串是这个字段的合法取值，而不是漏填。
	// 默认所有 string 都不许为空——那对绝大多数配置是对的（端点、模型名），
	// 但「生成时间窗口」空就是「不限」，是它最常见的取值。
	Optional bool   `json:"optional,omitempty"`
	Group    string `json:"group"`
}

// restartHint 是需重启项的统一提示。
const restartHint = "改完需重启：systemctl --user restart chatdex"

// Fields 是全部可配置项。顺序即界面上的顺序。
func Fields() []FieldMeta {
	return []FieldMeta{
		// ---- 外观 ----
		{Key: "ui.light_theme", Label: "亮色主题", Kind: "enum", Hot: true, Group: "外观",
			Help:    "顶栏切到「亮」或跟随系统判定为亮时用哪套",
			Options: []string{"desk", "paper"}},
		{Key: "ui.dark_theme", Label: "暗色主题", Kind: "enum", Hot: true, Group: "外观",
			Help:    "顶栏切到「暗」或跟随系统判定为暗时用哪套",
			Options: []string{"editor", "term"}},
		{Key: "ui.highlight", Label: "代码高亮配色", Kind: "enum", Hot: true, Group: "外观",
			Help:    "theme = 跟随界面主题（用同一组 token，四套主题下都协调）；其余为 highlight.js 官方配色",
			Options: []string{"theme", "github", "github-dark", "nord", "monokai", "atom-one-dark"}},
		{Key: "ui.mermaid_auto", Label: "自动渲染图表", Kind: "bool", Hot: true, Group: "外观",
			Help: "关闭时 mermaid 图表默认显示源码，点「渲染图表」才加载（该库 3.4 MB，不用就不加载）"},

		// ---- 本地 LLM ----
		{Key: "llm.endpoint", Label: "LLM 端点", Kind: "string", Hot: true, Group: "本地 LLM",
			Help: "只接受回环地址（127.0.0.1 / ::1 / localhost）。会话内容含明文凭证，不得发往本机之外。"},
		{Key: "llm.num_ctx", Label: "上下文窗口（token）", Kind: "int", Hot: true, Group: "本地 LLM",
			Help: "必须显式给：Ollama 默认只有 2048，与模型能力无关，超出部分会被静默截断（连开头的指令一起丢），模型于是答非所问却不报错。调大更吃显存。"},
		{Key: "summary.enabled", Label: "启用摘要生成", Kind: "bool", Hot: true, Group: "本地 LLM",
			Help: "关掉后后台不再生成新摘要，已有摘要保留"},
		{Key: "summary.window", Label: "生成时间窗口", Kind: "string", Hot: true, Optional: true, Group: "本地 LLM",
			Help: "如 02:00-08:00，可跨零点（22:00-06:00）。留空表示不限。只影响摘要生成，索引与检索照常。填错按不限处理。"},
		{Key: "summary.model", Label: "摘要模型", Kind: "enum", Hot: true, Group: "本地 LLM",
			Help: "从本地 Ollama 拉取；只列出支持文本生成的模型"},
		{Key: "summary.throttle_ms", Label: "摘要限速（毫秒）", Kind: "int", Hot: true, Group: "本地 LLM",
			Help: "每生成一条后的间隔。夜间挂机可设 0", Min: 0, Max: 60000},
		{Key: "chat.model", Label: "聊天模型", Kind: "enum", Hot: true, Group: "本地 LLM",
			Help: "问一问用的模型，需支持 tools"},
		{Key: "chat.max_tool_rounds", Label: "聊天最大轮次", Kind: "int", Hot: true, Group: "本地 LLM",
			Help: "单次提问最多调几轮检索工具", Min: 1, Max: 30},

		// ---- 索引 ----
		{Key: "scan.interval_sec", Label: "扫描间隔（秒）", Kind: "int", Hot: true, Group: "索引",
			Help: "多久扫一次会话目录找新内容", Min: 5, Max: 3600},
		{Key: "index.tool_result_cap", Label: "工具结果截断阈值（字节）", Kind: "int", Hot: true, Group: "索引",
			Help: "单条工具结果只索引前 N 字节。4096 覆盖 p90 完整", Min: 256, Max: 1 << 20,
			Note: "只影响之后新索引的内容；要让历史内容也改变，需停服务后跑 chatdex index"},
		{Key: "index.tool_result_body", Label: "索引工具结果正文", Kind: "bool", Hot: true, Group: "索引",
			Help: "关掉则工具结果只留元数据，索引库可小一半",
			Note: "同上：只影响之后新索引的内容"},
		{Key: "index.max_bytes", Label: "索引库体积上限（字节）", Kind: "bytes", Hot: true, Group: "索引",
			Help: "超限后停止索引新增内容并告警，绝不自动删除历史数据", Min: 1 << 30, Max: 1 << 42},

		// ---- 备份（对接 restic）----
		{Key: "backup.repo", Label: "restic 仓库路径", Kind: "string", Hot: true, Optional: true, Group: "备份",
			Help: "留空则备份功能整体置灰。restic 管「存得住、存得安全」，chatdex 管「该存什么、存了没有、怎么读回来」。"},
		{Key: "backup.password_file", Label: "密码文件", Kind: "string", Hot: true, Optional: true, Group: "备份",
			// help 一律按纯文本转义后显示，写 markdown 只会把星号原样吐出来
			Help: "只填路径，密码不会存进 chatdex 的配置、日志或界面。⚠️ 密码丢失 = 备份不可恢复。"},
		{Key: "backup.restic_path", Label: "restic 可执行文件", Kind: "string", Hot: true, Optional: true, Group: "备份",
			Help: "留空则从 PATH 找。装在 ~/.local/bin 时通常要填——systemd --user 起的服务其 PATH 未必包含它。"},
		{Key: "backup.sources", Label: "备份哪些目录", Kind: "sources", Hot: true, Optional: true, Group: "备份",
			Help: "勾选要备份的目录，可只备其中一个也可全部。默认列出会话目录，也可加任意其它路径。"},
		{Key: "backup.after_scan", Label: "扫描后顺手备一次", Kind: "bool", Hot: true, Group: "备份",
			Help: "实测无变化时 restic 只需 767 ms 且仓库零增长，所以这个开关很便宜。"},

		// ---- 需重启 ----
		{Key: "ports.ui", Label: "dashboard 端口", Kind: "int", Hot: false, Group: "服务",
			Help: "前端监听端口", Min: 1024, Max: 65535, Note: restartHint},
		{Key: "ports.api", Label: "API + MCP 端口", Kind: "int", Hot: false, Group: "服务",
			Help: "API 与 MCP 端点的监听端口", Min: 1024, Max: 65535, Note: restartHint},
		{Key: "db_path", Label: "索引库路径", Kind: "string", Hot: false, Group: "服务",
			Help: "SQLite 索引库位置", Note: restartHint},
		{Key: "home", Label: "会话来源家目录", Kind: "string", Hot: false, Group: "服务",
			Help: "从这里找 .claude/projects 与 .codex/sessions", Note: restartHint},
	}
}

// Get 按点分路径取值，供 API 下发当前值与默认值。
func (c Config) Get(key string) any {
	switch key {
	case "ui.light_theme":
		return c.UI.LightTheme
	case "ui.dark_theme":
		return c.UI.DarkTheme
	case "ui.highlight":
		return c.UI.Highlight
	case "ui.mermaid_auto":
		return c.UI.MermaidAuto
	case "llm.endpoint":
		return c.LLM.Endpoint
	case "llm.num_ctx":
		return c.LLM.NumCtx
	case "summary.enabled":
		return c.Summary.Enabled
	case "summary.window":
		return c.Summary.Window
	case "summary.model":
		return c.Summary.Model
	case "summary.throttle_ms":
		return c.Summary.ThrottleMS
	case "chat.model":
		return c.Chat.Model
	case "chat.max_tool_rounds":
		return c.Chat.MaxToolRounds
	case "scan.interval_sec":
		return c.Scan.IntervalSec
	case "index.tool_result_cap":
		return c.Index.ToolResultCap
	case "index.tool_result_body":
		return c.Index.ToolResultBody
	case "index.max_bytes":
		return c.Index.MaxBytes
	case "backup.repo":
		return c.Backup.Repo
	case "backup.password_file":
		return c.Backup.PasswordFile
	case "backup.restic_path":
		return c.Backup.ResticPath
	case "backup.sources":
		return c.Backup.Sources
	case "backup.after_scan":
		return c.Backup.AfterScan
	case "ports.ui":
		return c.Ports.UI
	case "ports.api":
		return c.Ports.API
	case "db_path":
		return c.DBPath
	case "home":
		return c.Home
	}
	return nil
}
