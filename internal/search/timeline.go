package search

import "strings"

// timelineLabelChars 是时间线条目辨识文本的长度上限。
const timelineLabelChars = 160

// TimelineSession 是时间线上的一个会话条目。
type TimelineSession struct {
	ID         int64  `json:"id"`
	Source     string `json:"source"`
	AgentLabel string `json:"agent_label,omitempty"`
	FilePath   string `json:"file_path"`
	StartedAt  int64  `json:"started_at"`
	EndedAt    int64  `json:"ended_at"`
	MsgCount   int    `json:"msg_count"`
	HasSummary bool   `json:"has_summary"`
	// Label 是可供辨认的文字：优先用摘要，没有就退回首条用户消息（需求 9.2）。
	Label string `json:"label"`
}

// ProjectGroup 是按项目聚合的一组会话。
type ProjectGroup struct {
	ProjectPath string            `json:"project_path"`
	Sessions    []TimelineSession `json:"sessions"`
	Total       int               `json:"total"` // 该项目在当前过滤条件下的会话总数
	LastAt      int64             `json:"last_at"`
}

// sessionFilters 是时间线用的过滤片段。
//
// 与检索侧不同：这里没有块级条件（kind / tool_name 属于块），
// 时间范围按「会话在该区间内有活动」判定，即区间与 [started_at, ended_at] 相交。
func (q Query) sessionFilters() (string, []any) {
	var sb strings.Builder
	var args []any
	if q.Source != "" {
		sb.WriteString(" AND s.source = ?")
		args = append(args, q.Source)
	}
	if q.Project != "" {
		sb.WriteString(" AND (s.project_path = ? OR s.project_path LIKE ?)")
		args = append(args, q.Project, q.Project+"/%")
	}
	if q.From > 0 {
		sb.WriteString(" AND s.ended_at >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		sb.WriteString(" AND s.started_at <= ?")
		args = append(args, q.To)
	}
	return sb.String(), args
}

// maxPerProject 是每个项目默认展开的会话数，其余折叠（需求 9.5）。
const maxPerProject = 20

// Timeline 返回按项目聚合、按时间倒序的会话列表。
//
// 过滤条件与检索共用同一个 Query（需求 9.4）：dashboard 上换视图不用重设条件。
func (e *Engine) Timeline(q Query) ([]ProjectGroup, error) {
	where, args := q.sessionFilters()
	limit, offset := q.limit()
	if q.Limit <= 0 {
		limit = 200 // 时间线一屏要看多个项目，默认比检索给得多
	}

	rows, err := e.db.Query(`
SELECT s.id, s.source, s.agent_label, s.file_path, s.project_path,
       s.started_at, s.ended_at, s.msg_count,
       COALESCE(NULLIF(s.summary, ''), '') AS summary,
       COALESCE((SELECT b.body FROM blocks b
                 WHERE b.session_id = s.id AND b.kind = 'user' AND b.seq >= 0
                 ORDER BY b.seq LIMIT 1), '') AS first_user
FROM sessions s
WHERE s.alive = 1`+where+`
ORDER BY s.started_at DESC
LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var order []string
	byProject := map[string]*ProjectGroup{}
	for rows.Next() {
		var t TimelineSession
		var project, summary, firstUser string
		if err := rows.Scan(&t.ID, &t.Source, &t.AgentLabel, &t.FilePath, &project,
			&t.StartedAt, &t.EndedAt, &t.MsgCount, &summary, &firstUser); err != nil {
			return nil, err
		}
		if summary != "" {
			t.HasSummary = true
			t.Label = summary
		} else {
			t.Label = clipRunes(Strip(firstUser), timelineLabelChars)
		}
		if project == "" {
			project = "（未知项目）"
		}
		g, ok := byProject[project]
		if !ok {
			g = &ProjectGroup{ProjectPath: project}
			byProject[project] = g
			order = append(order, project)
		}
		g.Total++
		if g.LastAt < t.EndedAt {
			g.LastAt = t.EndedAt
		}
		if len(g.Sessions) < maxPerProject {
			g.Sessions = append(g.Sessions, t)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 保持「最近活动的项目在前」——order 已经是按 started_at 倒序首次出现的顺序
	out := make([]ProjectGroup, 0, len(order))
	for _, p := range order {
		out = append(out, *byProject[p])
	}
	return out, nil
}

func clipRunes(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
