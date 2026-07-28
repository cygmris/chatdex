package index

// Config 是索引行为的可调项。默认值来自 design.md 的实测采样：
// tool_result 占语料字节的 75.5%，截断到 4096 覆盖 p90 完整、只保留其总字节的 26.5%。
type Config struct {
	// ToolResultCap 是单条工具结果的截断阈值（字节）。
	ToolResultCap int `json:"tool_result_cap"`

	// ToolResultBody 为 false 时工具结果只留元数据（工具名、时间、体积），
	// 不索引正文——需求 7.7 要求提供的开关，实测可把索引库压到约 1/3。
	ToolResultBody bool `json:"tool_result_body"`

	// MaxBytes 是索引库体积上限。超限只告警并停止索引新增内容，
	// 检索照常工作，**绝不自动删除历史数据**——删掉的是不可再生的内容。
	MaxBytes int64 `json:"max_bytes"`
}

func DefaultConfig() Config {
	return Config{
		ToolResultCap:  4096,
		ToolResultBody: true,
		MaxBytes:       12 << 30, // 12 GB
	}
}
