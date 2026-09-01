package search

import "strings"

// timelineLabelChars 是时间线条目辨识文本的长度上限。
const timelineLabelChars = 160

// TimelineSession 是时间线上的一个会话条目。
type TimelineSession struct {
	ID         int64  `json:"id"`
	Source     string `json:"source"`
	AgentLabel string `json:"agent_label,omitempty"`
	IsSub      bool   `json:"is_sub,omitempty"`
	FilePath   string `json:"file_path"`
	StartedAt  int64  `json:"started_at"`
	EndedAt    int64  `json:"ended_at"`
	MsgCount   int    `json:"msg_count"`
	HasSummary bool   `json:"has_summary"`
	Title      string `json:"title,omitempty"` // 用户 /rename 的名字
	TargetSeq  int    `json:"target_seq"`      // 日期范围内第一条消息；未按日期筛选时为 0
	// Label 是可供辨认的文字：优先用摘要，没有就退回首条用户消息（需求 9.2）。
	Label string `json:"label"`
}

// ProjectGroup 是按项目聚合的一组会话。
type ProjectGroup struct {
	ProjectPath string            `json:"project_path"`
	Sessions    []TimelineSession `json:"sessions"`
	// Total 是该项目在当前过滤条件下的会话总数，**与本次返回了多少条无关**。
	//
	// 这句话一度是假的：旧实现先按时间倒序取全局最新 200 条会话，再在这 200 条里
	// 分组并 `Total++`，于是它的真实含义是「最新 200 条里落在这个项目的有几条」。
	// 实测 project-a 因此在界面上显示 10 个会话，而它真的有 182 个。
	// 现在 Total 来自分页**之前**的 COUNT(*)，名副其实。
	Total  int   `json:"total"`
	LastAt int64 `json:"last_at"`
}

// sessionFilters 是时间线用的过滤片段。
//
// 与检索侧的分工：会话级条件（source / project / agent）走共用的
// sessionScopeFilters；`q` / `kind` / `tool` 在这里的语义是
// **「这个会话里存在满足条件的块」**（EXISTS），而不是检索侧的「这个块满足」——
// 时间线的行是会话，不是块。
//
// 🔴 这三条此前**被静默忽略**：parseQuery 把它们完整解析进 Query，Timeline 也
// 完整收到，然后这里从不读。前端筛选条照常显示、照常可选，使用者选了
// kind=summary——下拉变了、URL 变了、列表重载了，四个正反馈齐全，而结果一条没变。
// 实测（带阳性对照）：q=<不可能的词> 与 kind=summary 的响应与无筛选**逐字节相同**，
// 换 source=codex 则从 6 组变 23 组，证明接口本身没坏。
//
// 时间范围仍按真实消息活动判定：只比较 sessions.started_at / ended_at 会把长会话
// 中间完全没消息的日期也算进去，且点击后无法给出落在过滤范围内的回读位置。
func (q Query) sessionFilters() (string, []any) {
	scope, args := q.sessionScopeFilters()
	var sb strings.Builder
	sb.WriteString(scope)

	// q：会话里存在命中该关键词的块。
	//
	// 🔴 **用 IN 物化一次，不用相关子查询 EXISTS。** FTS 没有 session_id 那一侧的
	// 索引，写成相关子查询等于对每个候选会话重跑一次全文查询：实测 515 ms vs
	// 0.37 ms，差三个数量级。这与紧接着的 kind/tool 恰好相反——那两条能吃到
	// blocks_session(session_id, seq) 的前缀，相关 EXISTS 反而快一倍多
	// （111 ms vs 249 ms）。**不存在「哪种写法更好」的一刀切答案。**
	if m := BuildMatch(q.Text); m != "" {
		sb.WriteString(` AND s.id IN (SELECT fb.session_id FROM blocks_fts f
                 JOIN blocks fb ON fb.id = f.rowid WHERE blocks_fts MATCH ?)`)
		args = append(args, m)
	}

	// kind / tool：会话里存在该类型的块 / 该工具的调用。
	if len(q.Kinds) > 0 || q.ToolName != "" {
		var cond strings.Builder
		if len(q.Kinds) > 0 {
			cond.WriteString(" AND kb.kind IN (" + strings.TrimRight(strings.Repeat("?,", len(q.Kinds)), ",") + ")")
			for _, k := range q.Kinds {
				args = append(args, k)
			}
		}
		if q.ToolName != "" {
			cond.WriteString(" AND kb.tool_name = ?")
			args = append(args, q.ToolName)
		}
		sb.WriteString(" AND EXISTS (SELECT 1 FROM blocks kb WHERE kb.session_id = s.id" +
			cond.String() + ")")
	}

	if q.From > 0 || q.To > 0 {
		where, timeArgs := q.blockTimeFilter("activity")
		sb.WriteString(" AND EXISTS (SELECT 1 FROM blocks activity" +
			" WHERE activity.session_id = s.id AND activity.seq >= 0" + where + ")")
		args = append(args, timeArgs...)
	}
	return sb.String(), args
}

// blockTimeFilter 生成时间线入选与 target_seq 共用的 block 时间条件。
// alias 只由本文件的固定 SQL 标识符传入，不接收用户输入。
func (q Query) blockTimeFilter(alias string) (string, []any) {
	var sb strings.Builder
	var args []any
	if q.From > 0 {
		sb.WriteString(" AND " + alias + ".ts >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		sb.WriteString(" AND " + alias + ".ts <= ?")
		args = append(args, q.To)
	}
	return sb.String(), args
}

// TimelineResult 是时间线一页的结果。
//
// 之所以不再直接返回 []ProjectGroup：**没有地方放「一共多少个项目」**。
// 而没有那个数，界面就说不出「118 个项目，这是第 1–20 个」，
// 使用者也就无从知道自己有没有看全 —— 实测本机 118 个项目里
// 旧实现只显示 6 个，而界面上没有任何迹象。
type TimelineResult struct {
	Groups []ProjectGroup `json:"groups"`
	// ProjectTotal 是当前过滤条件下的项目总数，与本页返回了几个无关。
	ProjectTotal int `json:"project_total"`
	Offset       int `json:"offset"`
	Limit        int `json:"limit"`
}

// maxPerProject 是每个项目默认展开的会话数，其余折叠（需求 9.5）。
const maxPerProject = 20

// Timeline 返回按项目聚合、按最近活动倒序的会话列表。
//
// **分页单位是「项目」，不是「会话」。** 旧实现用一个全局 `LIMIT 200` 取最新
// 200 条会话再分组，代价是实测 118 个项目里只看得到 6 个 —— 另外 112 个
// （95%）整个消失，而界面上没有任何迹象。按会话分页对一个「按项目聚合」的
// 视图天然是错的：页边界会把某个项目腰斩，而每组的「总数」也只能数到页内。
//
// 过滤条件与检索共用同一个 Query（需求 9.4）：dashboard 上换视图不用重设条件。
func (e *Engine) Timeline(q Query) (TimelineResult, error) {
	where, args := q.sessionFilters()
	targetExpr := "0"
	var targetArgs []any
	if q.From > 0 || q.To > 0 {
		targetWhere, a := q.blockTimeFilter("target")
		targetArgs = a
		targetExpr = `COALESCE((SELECT target.seq FROM blocks target
                 WHERE target.session_id = s.id AND target.seq >= 0` + targetWhere + `
                 ORDER BY target.seq LIMIT 1), 0)`
	}
	limit, offset := q.limit()

	// project_total 用 COUNT(*) OVER () 在同一趟里算出来，随每行下发。
	//
	// 之前是单独一条 COUNT 查询，代价是**那个昂贵的过滤条件要跑两遍**：
	// 实测 kind=summary 时 API 613 ms 而同一条 SQL 只要 287 ms，差的正好是一倍。
	//
	// elig 只取分组与排序需要的三列：辨识文字与日期落点都是按会话逐条算的，
	// 放进 elig 会对全部入选会话（实测 2241 条）跑一遍，而真正要返回的最多
	// limit×maxPerProject 条。重的东西留到最后一步 JOIN 回 sessions 再算。
	rows, err := e.db.Query(`
WITH elig AS (
  SELECT s.id, s.project_path, s.started_at, s.ended_at
  FROM sessions s WHERE s.alive = 1`+where+`
),
grouped AS (
  SELECT project_path AS p, COUNT(*) AS total, MAX(ended_at) AS last_at,
         COUNT(*) OVER () AS project_total
  FROM elig GROUP BY project_path
),
picked AS (
  SELECT * FROM grouped ORDER BY last_at DESC LIMIT ? OFFSET ?
),
ranked AS (
  SELECT e.id, e.project_path, picked.total, picked.last_at,
         picked.project_total,
         ROW_NUMBER() OVER (PARTITION BY e.project_path
                            ORDER BY e.started_at DESC) AS rn
  FROM elig e JOIN picked ON picked.p = e.project_path
)
SELECT s.id, s.source, s.agent_label, s.parent_uid != '' AS is_sub, s.file_path,
       r.project_path, s.started_at, s.ended_at, s.msg_count,
       COALESCE(NULLIF(s.summary, ''), '') AS summary,
       s.title,
       COALESCE((SELECT b.body FROM blocks b
                 WHERE b.session_id = s.id AND b.kind = 'user' AND b.seq >= 0
                 ORDER BY b.seq LIMIT 1), '') AS first_user,
       `+targetExpr+` AS target_seq,
       r.total, r.last_at, r.project_total
FROM ranked r JOIN sessions s ON s.id = r.id
WHERE r.rn <= ?
ORDER BY r.last_at DESC, r.project_path, s.started_at DESC`,
		appendArgs(args, limit, offset, targetArgs, maxPerProject)...)
	if err != nil {
		return TimelineResult{}, err
	}
	defer rows.Close()

	var order []string
	var projectTotal int
	byProject := map[string]*ProjectGroup{}
	for rows.Next() {
		var t TimelineSession
		var project, summary, firstUser, title string
		var total, rowProjectTotal int
		var lastAt int64
		if err := rows.Scan(&t.ID, &t.Source, &t.AgentLabel, &t.IsSub, &t.FilePath, &project,
			&t.StartedAt, &t.EndedAt, &t.MsgCount, &summary, &title, &firstUser,
			&t.TargetSeq, &total, &lastAt, &rowProjectTotal); err != nil {
			return TimelineResult{}, err
		}
		// 用户 /rename 的名字优先——人写的比机器摘要可信
		t.Title = title
		switch {
		case title != "":
			t.Label = title
		case summary != "":
			t.HasSummary = true
			t.Label = summary
		default:
			t.Label = clipRunes(Strip(firstUser), timelineLabelChars)
		}
		projectTotal = rowProjectTotal
		if project == "" {
			project = "（未知项目）"
		}
		g, ok := byProject[project]
		if !ok {
			// total 与 last_at 由 SQL 在分页前算好，Go 这边只是抄过来。
			// 用 `g.Total++` 数就会退回旧实现那个 bug：数的是页内条数。
			g = &ProjectGroup{ProjectPath: project, Total: total, LastAt: lastAt}
			byProject[project] = g
			order = append(order, project)
		}
		g.Sessions = append(g.Sessions, t)
	}
	if err := rows.Err(); err != nil {
		return TimelineResult{}, err
	}

	// 翻过头那一页一行都没有，project_total 也就无从随行下发 —— 而那正是
	// 最需要它的时候：「翻过头了」与「筛完就是没有」在 groups 上完全同形，
	// project_total 是唯一能把两者分开的信号。所以只在这一种情况下补一条计数。
	// 常见路径（有结果）仍然只跑一趟过滤。
	if len(order) == 0 {
		if err := e.db.QueryRow(`
SELECT COUNT(*) FROM (
  SELECT 1 FROM sessions s WHERE s.alive = 1`+where+` GROUP BY s.project_path
)`, args...).Scan(&projectTotal); err != nil {
			return TimelineResult{}, err
		}
	}

	// SQL 已按 last_at DESC 排好，order 记录的就是首次出现的顺序
	out := make([]ProjectGroup, 0, len(order))
	for _, p := range order {
		out = append(out, *byProject[p])
	}
	return TimelineResult{Groups: out, ProjectTotal: projectTotal, Offset: offset, Limit: limit}, nil
}

// appendArgs 按 SQL 里占位符的**文本顺序**拼参数。
//
// 单独抽出来是因为顺序反直觉：`targetExpr` 写在最外层 SELECT 里，
// 位置却排在两个 CTE 的参数之后。拼错不会报错，只会静默绑错值。
func appendArgs(where []any, limit, offset int, target []any, perProject int) []any {
	out := make([]any, 0, len(where)+len(target)+3)
	out = append(out, where...)      // 1. elig 的 WHERE
	out = append(out, limit, offset) // 2. picked 的 LIMIT/OFFSET
	out = append(out, target...)     // 3. 最外层 SELECT 里的 target_seq
	out = append(out, perProject)    // 4. WHERE r.rn <= ?
	return out
}

func clipRunes(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
