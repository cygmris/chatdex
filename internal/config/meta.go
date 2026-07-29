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
	Group   string   `json:"group"`
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

		// ---- 本地 LLM ----
		{Key: "llm.endpoint", Label: "LLM 端点", Kind: "string", Hot: true, Group: "本地 LLM",
			Help: "只接受回环地址（127.0.0.1 / ::1 / localhost）。会话内容含明文凭证，不得发往本机之外。"},
		{Key: "summary.enabled", Label: "启用摘要生成", Kind: "bool", Hot: true, Group: "本地 LLM",
			Help: "关掉后后台不再生成新摘要，已有摘要保留"},
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
	case "llm.endpoint":
		return c.LLM.Endpoint
	case "summary.enabled":
		return c.Summary.Enabled
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
