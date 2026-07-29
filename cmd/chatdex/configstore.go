package main

import (
	"context"
	"log/slog"

	"github.com/cygmris/chatdex/internal/config"
	"github.com/cygmris/chatdex/internal/llm"
)

// configStore 把「保存配置」与「让它立刻生效」接在一起。
//
// 热生效的关键在别处：各处必须**每次用时**从 Live 取值，而不是启动时拷成字段。
// 拷成字段的那一刻，这个配置项就悄悄变成需重启的了。
type configStore struct {
	live    *config.Live
	path    string
	newLLM  func(endpoint string) (*llm.Ollama, error)
	onApply func(config.Config)
}

func (s *configStore) Current() config.Config { return s.live.Get() }

func (s *configStore) Apply(c config.Config) []config.FieldError {
	if errs := config.Validate(c); len(errs) > 0 {
		return errs
	}
	if err := config.Save(s.path, c); err != nil {
		return []config.FieldError{{Key: "_", Msg: err.Error()}}
	}
	s.live.Set(c)
	if s.onApply != nil {
		s.onApply(c)
	}
	slog.Info("配置已更新并生效", "path", s.path)
	return nil
}

func (s *configStore) Models(ctx context.Context) ([]string, string) {
	client, err := s.newLLM(s.live.Get().LLM.Endpoint)
	if err != nil {
		return nil, "LLM 端点不可用：" + err.Error()
	}
	ms, err := client.Models(ctx)
	if err != nil {
		return nil, "取模型列表失败：" + err.Error() + "（Ollama 没在跑？）"
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	if len(out) == 0 {
		return out, "本地没有可用于文本生成的模型"
	}
	return out, ""
}
