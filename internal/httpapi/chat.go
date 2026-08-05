package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cygmris/chatdex/internal/chat"
)

func (s *Server) handleChatStatus(w http.ResponseWriter, r *http.Request) {
	reason := s.ChatUnavailableReason
	available := s.Chat != nil
	if available && !s.Chat.Available(r.Context()) {
		// 端点配得对不等于服务起着
		available, reason = false, "本地 LLM 未响应（Ollama 没在跑？）"
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": available, "reason": reason})
}

// handleChat 以 SSE 流式推送 agent 的每一步。
//
// 用 SSE 而不是 WebSocket：这里是单向推送，SSE 够用且不引额外依赖。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if s.Chat == nil {
		writeErr(w, http.StatusServiceUnavailable, "聊天不可用："+s.ChatUnavailableReason)
		return
	}
	if !s.Chat.Available(r.Context()) {
		writeErr(w, http.StatusServiceUnavailable, "聊天不可用：本地 LLM 未响应")
		return
	}
	var body struct {
		Question string `json:"question"`
		// Project 是用户在界面上划的检索范围；空 = 全部项目。
		// 服务端会把它作为硬约束注入检索工具，不只是提示词。
		Project string `json:"project"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Question == "" {
		writeErr(w, http.StatusBadRequest, "缺少 question")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "该连接不支持流式推送")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(e chat.Event) {
		b, err := json.Marshal(e)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	if err := s.Chat.Ask(r.Context(), body.Question, chat.Scope{Project: body.Project}, send); err != nil {
		send(chat.Event{Type: "note", Text: "出错了：" + err.Error()})
	}
	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}
