package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
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
// errNoOriginSource：源文件不在磁盘上，而备份又没启用 —— 两条路都断了。
//
// 单独一个哨兵而不是普通 error：这是**功能未启用**（503），
// 与「备份里也没有这个会话」（404）和「restic 挂了」（500）是三件不同的事，
// 混成一个会让人不知道下一步该干什么（需求 4.4 的同一条纪律）。
var errNoOriginSource = errors.New("源文件已不在，且未启用备份")

// openOriginal 打开会话的原件：**源文件还在就直接读磁盘，没了才走备份**。
//
// 直接读磁盘不是优化，是正确性：restic 里那份是某个快照时刻的副本，磁盘上那份
// 是现在的。会话还在写的时候，备份必然落后。
//
// 用 os.Open 成功与否判断，而不是先 Stat 再开：中间那一瞬文件可能被删，
// **「查一次再用」本身就是个竞态**。开成功了就是能读。
//
// 只读打开。路径来自索引（sessions.file_path），使用者只提供会话 id，
// 不接受任何来自请求的路径——这是「绝不写/改/删任何会话原始文件」那条铁律的一半，
// 另一半是这里从头到尾没有任何写操作。
func (s *Server) openOriginal(ctx context.Context, path string) (io.ReadCloser, string, error) {
	if f, err := os.Open(path); err == nil {
		return f, "disk", nil
	}
	// 到这里说明源文件读不到了，才轮到备份。守卫放在这一刻而不是函数入口：
	// 否则没配 restic 的人连看自己磁盘上的文件都不行。
	if s.Backup == nil {
		return nil, "", errNoOriginSource
	}
	rc, err := s.Backup.Fetch(ctx, path)
	return rc, "backup", err
}

func (s *Server) handleArchived(w http.ResponseWriter, r *http.Request) {
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

	rc, origin, err := s.openOriginal(ctx, view.FilePath)
	if err != nil {
		if errors.Is(err, errNoOriginSource) {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
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
	_, err = p.Parse(rc, view.FilePath, parser.Cursor{}, func(b model.Block) error {
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
	// **不能让使用者猜这份内容是从哪来的**：磁盘上那份是当前内容，
	// 备份里那份是某个快照时刻的，两者可能不同。不说清楚就是让人拿旧的当新的。
	writeJSON(w, http.StatusOK, struct {
		search.SessionView
		Origin string `json:"origin"`
	}{view, origin})
}
