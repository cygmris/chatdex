package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cygmris/chatdex/internal/config"
)

// ConfigStore 是设置页需要的后端能力。
//
// 用最小接口而不是直接依赖具体类型：写入路径固定在服务端，
// 前端给不了任意路径（需求非功能 Security）。
type ConfigStore interface {
	Current() config.Config
	// Apply 校验 → 保存 → 热生效；返回逐字段错误供界面定位。
	Apply(c config.Config) []config.FieldError
	// Models 列出本地可用于文本生成的模型；LLM 不可用时返回空列表与原因。
	Models(ctx context.Context) ([]string, string)
}

type configField struct {
	config.FieldMeta
	Value   any `json:"value"`
	Default any `json:"default"`
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		writeErr(w, http.StatusServiceUnavailable, "配置服务未启用")
		return
	}
	cur := s.Config.Current()
	def := config.Default()
	models, modelErr := s.Config.Models(r.Context())

	fields := make([]configField, 0, len(config.Fields()))
	for _, f := range config.Fields() {
		// 模型类字段的可选项是运行时从 Ollama 拉的
		if f.Key == "summary.model" || f.Key == "chat.model" {
			f.Options = models
		}
		fields = append(fields, configField{FieldMeta: f, Value: cur.Get(f.Key), Default: def.Get(f.Key)})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"fields":     fields,
		"values":     cur,
		"path":       config.Path(),
		"model_note": modelErr,
	})
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		writeErr(w, http.StatusServiceUnavailable, "配置服务未启用")
		return
	}
	// 以当前配置为底，只覆盖请求里给出的键——前端不必回传全部
	cur := s.Config.Current()
	if err := json.NewDecoder(r.Body).Decode(&cur); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法配置 JSON")
		return
	}
	if errs := s.Config.Apply(cur); len(errs) > 0 {
		// 422 而不是 400：请求格式没问题，是取值不合法
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": errs})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "path": config.Path()})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		writeJSON(w, http.StatusOK, map[string]any{"models": []string{}, "note": "配置服务未启用"})
		return
	}
	models, note := s.Config.Models(r.Context())
	if models == nil {
		models = []string{}
	}
	// LLM 不可用是功能降级不是错误：返回空列表 + 原因，设置页其余部分照常可用
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "note": note})
}
