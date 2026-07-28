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

	// Home 是解析器的家目录，仅测试会改。
	Home string `json:"home"`
	// DBPath 是索引库路径。
	DBPath string `json:"db_path"`
}

type Scan struct {
	IntervalSec int `json:"interval_sec"`
}

type Summary struct {
	Enabled    bool   `json:"enabled"`
	ThrottleMS int    `json:"throttle_ms"`
	Model      string `json:"model"`
}

// LLM 的 Endpoint 只允许回环地址——会话内容含明文凭证，不留远端逃生口
// （需求 10.5，不可放宽）。校验在 internal/llm 里做，配置层只负责读。
type LLM struct {
	Endpoint string `json:"endpoint"`
}

type Chat struct {
	Model         string `json:"model"`
	MaxToolRounds int    `json:"max_tool_rounds"`
}

type Ports struct {
	UI  int `json:"ui"`
	API int `json:"api"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Index:   index.DefaultConfig(),
		Scan:    Scan{IntervalSec: 30},
		Summary: Summary{Enabled: true, ThrottleMS: 500, Model: "qwen2.5:7b-instruct"},
		LLM:     LLM{Endpoint: "http://127.0.0.1:11434"},
		Chat:    Chat{Model: "qwen2.5:7b-instruct", MaxToolRounds: 8},
		Ports:   Ports{UI: 5021, API: 5022},
		Home:    home,
		DBPath:  filepath.Join(home, ".local", "share", "chatdex", "index.db"),
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
