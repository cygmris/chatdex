package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cygmris/chatdex/internal/backup"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/parser"
	"github.com/cygmris/chatdex/internal/search"
)

// archivedTimeout 是一次取回的上限。
//
// 实测 dump 229 MB 用 947 ms、find 用 729 ms，正常情况下远够；
// 给上限是因为这条路径同步挡在界面前面，仓库在慢盘/网络后端上不能无限等。
const archivedTimeout = 60 * time.Second

// handleArchived 从备份里取回原件并渲染。
//
// 为什么需要这个——索引里明明已经有消息体、`alive=0` 的会话现在照样能读：
// 因为**索引对工具结果是故意有损的**（超 tool_result_cap 截断、非文本清空），
// 实测 706362 个块里 43637 个被截断。备份里的原件才是完整的（需求 4.3）。
//
// 只读铁律对恢复同样成立：取回只用于展示，不落盘、不写回源目录（需求 4.5）。
func (s *Server) handleArchived(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		writeErr(w, http.StatusServiceUnavailable, "备份功能未启用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "会话 id 非法")
		return
	}
	q := r.URL.Query()
	from, limit := int(atoi64(q.Get("from"))), int(atoi64(q.Get("limit")))
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	// 复用回读视图的元信息（标题、项目、时间…），只把消息换成备份里的原件。
	// 这样前端的渲染器一行都不用改。
	view, err := s.Engine.GetSession(id, 0, 1)
	if err != nil {
		writeErr(w, http.StatusNotFound, "会话不存在")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), archivedTimeout)
	defer cancel()

	rc, err := s.Backup.Fetch(ctx, view.FilePath)
	if err != nil {
		// 「备份里也没有」要与「restic 挂了」分开：前者是这个会话真的没了，
		// 后者是备份本身有问题。需求 4.4 要求明确告知，不得显示空白。
		if errors.Is(err, backup.ErrNotInBackup) {
			writeErr(w, http.StatusNotFound, "备份里也没有这个会话——它在第一次备份之前就已经消失了")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()

	p := s.Reg.For(view.FilePath)
	if p == nil {
		writeErr(w, http.StatusInternalServerError, "认不出这个会话的格式："+view.FilePath)
		return
	}

	msgs := make([]search.Message, 0, limit)
	total := 0
	_, err = p.Parse(rc, parser.Cursor{}, func(b model.Block) error {
		total++
		if b.Seq >= from && len(msgs) < limit {
			msgs = append(msgs, search.Message{
				Seq: b.Seq, TS: b.TS, Kind: string(b.Kind),
				ToolName: b.ToolName, ToolUseID: b.ToolUseID,
				// 原件不截断——这正是从备份读的意义所在
				Truncated: false, Body: b.Body,
			})
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "解析备份里的原件失败："+err.Error())
		return
	}
	// dump 的退出码要在这里收：它中途失败只表现为流提前结束，
	// 不等进程退出就会把半个会话当成完整的发出去。
	if err := rc.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, "从备份取回中断："+err.Error())
		return
	}

	view.Total, view.FromSeq, view.Messages = total, from, msgs
	writeJSON(w, http.StatusOK, view)
}
