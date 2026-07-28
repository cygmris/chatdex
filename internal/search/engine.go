package search

import (
	"database/sql"
	"fmt"
	"strings"
)

// snippetTokensStr 是 snippet() 的 token 预算（直接拼进 SQL，故为字符串）。
// CJK 被切成单字后每个字都是一个 token，给小了会把高亮短语从中间截断。
const snippetTokensStr = "64"

// HitOpen / HitClose 是片段里标记命中位置的两个控制字符。
//
// 刻意不用 <mark>：片段正文是会话原文，里面完全可能有 <script>。
// 前端必须先把整段文本按 HTML 转义，再把这两个哨兵换成 <mark></mark>，
// 否则等于把历史会话里的任意 HTML 直接注进页面。
const (
	HitOpen  = "\x02"
	HitClose = "\x03"
)

// ftsScoreCTE 是所有检索查询的公共前置：先在**只有 FTS 表**的上下文里算出
// 每个命中块的 bm25 分数与高亮片段，再由外层去 join 过滤。
//
// 这个形状不是随意选的——bm25()/snippet() 只能用在直接查询 FTS 表的语境里，
// 一旦与 blocks/sessions 同处一个 SELECT（哪怕写成子查询被优化器展平），
// SQLite 就会报 "unable to use function bm25 in the requested context"。
// `AS MATERIALIZED` 是关键：它阻止优化器把 CTE 展平回外层。
// ⚠️ 这里**只算 bm25，绝不算 snippet**。snippet() 要把正文从外部内容表取回来
// 重新分词，代价与「全库命中数」成正比：实测查「的」（单个 CJK 字即一个 token，
// 命中 92864 块）时，带 snippet 的一趟要 2.8 秒，不带只要 99 毫秒。
// 片段只对**最终要展示的那几行**按 rowid 单独取（见 snippetFor），代价与命中总数无关。
const ftsScoreCTE = `SELECT rowid AS bid, bm25(blocks_fts) AS score
FROM blocks_fts WHERE blocks_fts MATCH ?`

// Engine 是全项目**唯一**的检索实现：dashboard、MCP 端点与聊天 agent
// 三端共用它，不得各自再写一套（需求 4.4 / 10.4）。
type Engine struct{ db *sql.DB }

func NewEngine(db *sql.DB) *Engine { return &Engine{db: db} }

// Query 是一次检索的条件。多个条件以 AND 组合（需求 6.4）。
type Query struct {
	Text     string   `json:"text"`
	Kinds    []string `json:"kinds"`     // user|assistant|tool_use|tool_result|summary
	ToolName string   `json:"tool_name"` // 需求 7.3：「哪次用 rsync 部署的」
	Source   string   `json:"source"`    // claude|codex
	Project  string   `json:"project"`
	From     int64    `json:"from"` // unix 秒，0 = 不限
	To       int64    `json:"to"`
	Limit    int      `json:"limit"`
	Offset   int      `json:"offset"`
}

// SessionHit 是会话粒度的命中。
type SessionHit struct {
	ID          int64   `json:"id"`
	Source      string  `json:"source"`
	SessionUID  string  `json:"session_uid"`
	AgentLabel  string  `json:"agent_label,omitempty"`
	FilePath    string  `json:"file_path"` // 原始文件绝对路径（需求 3.4）
	ProjectPath string  `json:"project_path"`
	StartedAt   int64   `json:"started_at"`
	EndedAt     int64   `json:"ended_at"`
	MsgCount    int     `json:"msg_count"`
	Summary     string  `json:"summary,omitempty"`
	Score       float64 `json:"score"` // 会话内最佳块的 bm25，越小越相关
	Hits        int     `json:"hits"`  // 命中数：只展示，**不参与排序**
	Snippet     string  `json:"snippet"`
	BestSeq     int     `json:"best_seq"` // 最佳命中块的会话内序号，供跳转
	BestKind    string  `json:"best_kind"`
	BestTool    string  `json:"best_tool,omitempty"`
}

// BlockHit 是块粒度的命中。
type BlockHit struct {
	SessionID int64   `json:"session_id"`
	Seq       int     `json:"seq"`
	TS        int64   `json:"ts"`
	Kind      string  `json:"kind"`
	ToolName  string  `json:"tool_name,omitempty"`
	ToolUseID string  `json:"tool_use_id,omitempty"`
	Truncated bool    `json:"truncated"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
}

// Result 是检索结果。NoMatch 明确区分「无命中」与「有命中但被过滤空了」，
// 避免用近似结果冒充命中（需求 1.6）。
type Result struct {
	Sessions []SessionHit `json:"sessions"`
	NoMatch  bool         `json:"no_match"`
	Query    Query        `json:"query"`
}

// BuildMatch 把用户输入转成 FTS5 MATCH 表达式。
//
// 每个空白分隔的词各成一个短语（引号包裹使 * - : ( 等特殊字符按字面处理），
// 词与词之间是 AND。CJK 经 NormalizeQuery 切成单字后，短语查询保证它们按序相邻，
// 于是「浏览器」能命中「增量备份」内部而不会命中散落各处的三个字。
func BuildMatch(text string) string {
	var phrases []string
	for _, term := range strings.Fields(text) {
		norm := strings.TrimSpace(NormalizeQuery(term))
		if norm == "" {
			continue
		}
		phrases = append(phrases, `"`+strings.ReplaceAll(norm, `"`, `""`)+`"`)
	}
	return strings.Join(phrases, " ")
}

// filters 生成公共的 WHERE 片段。
func (q Query) filters() (string, []any) {
	var sb strings.Builder
	var args []any

	if len(q.Kinds) > 0 {
		sb.WriteString(" AND b.kind IN (" + strings.TrimRight(strings.Repeat("?,", len(q.Kinds)), ",") + ")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if q.ToolName != "" {
		sb.WriteString(" AND b.tool_name = ?")
		args = append(args, q.ToolName)
	}
	if q.Source != "" {
		sb.WriteString(" AND s.source = ?")
		args = append(args, q.Source)
	}
	if q.Project != "" {
		// 子目录下产生的会话也算「该工作目录下」
		sb.WriteString(" AND (s.project_path = ? OR s.project_path LIKE ?)")
		args = append(args, q.Project, q.Project+"/%")
	}
	if q.From > 0 {
		sb.WriteString(" AND b.ts >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		sb.WriteString(" AND b.ts <= ?")
		args = append(args, q.To)
	}
	return sb.String(), args
}

func (q Query) limit() (int, int) {
	n := q.Limit
	if n <= 0 || n > 200 {
		n = 20
	}
	off := q.Offset
	if off < 0 {
		off = 0
	}
	return n, off
}

// SearchSessions 返回按相关度排序的会话列表。
//
// ⚠️ 排序用会话内的**最佳块**（MIN(bm25)），不是命中数求和。
// 这条直接对应踩出来的事故：找 restic 那次，命中最多的两个会话
// （2272 / 2154 次）都不是目标，目标只有 669 次。求和排序会原样重建那个 bug。
func (e *Engine) SearchSessions(q Query) (Result, error) {
	match := BuildMatch(q.Text)
	if match == "" {
		return e.recentSessions(q)
	}

	where, args := q.filters()
	limit, offset := q.limit()

	rows, err := e.db.Query(`
WITH f AS MATERIALIZED (`+ftsScoreCTE+`)
SELECT s.id, s.source, s.session_uid, s.agent_label, s.file_path, s.project_path,
       s.started_at, s.ended_at, s.msg_count, COALESCE(s.summary, ''),
       MIN(f.score) AS score, f.bid AS best_bid, COUNT(*) AS hits
FROM f
JOIN blocks   b ON b.id = f.bid
JOIN sessions s ON s.id = b.session_id
WHERE s.alive = 1`+where+`
GROUP BY s.id
ORDER BY score ASC
LIMIT ? OFFSET ?`, append(append([]any{match}, args...), limit, offset)...)
	if err != nil {
		return Result{}, fmt.Errorf("检索会话: %w", err)
	}
	defer rows.Close()

	res := Result{Query: q}
	var bestIDs []int64
	for rows.Next() {
		var h SessionHit
		var bid int64
		// MIN() 的裸列取自产生该最小值的那一行——SQLite 对 min()/max() 有此保证，
		// 于是 best_bid 就是该会话最相关的那个块。
		if err := rows.Scan(&h.ID, &h.Source, &h.SessionUID, &h.AgentLabel, &h.FilePath,
			&h.ProjectPath, &h.StartedAt, &h.EndedAt, &h.MsgCount, &h.Summary,
			&h.Score, &bid, &h.Hits); err != nil {
			return Result{}, err
		}
		res.Sessions = append(res.Sessions, h)
		bestIDs = append(bestIDs, bid)
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}

	for i := range res.Sessions {
		if err := e.fillBest(&res.Sessions[i], bestIDs[i], match); err != nil {
			return Result{}, err
		}
	}
	res.NoMatch = len(res.Sessions) == 0
	return res, nil
}

// fillBest 补上该会话最佳块的位置与高亮片段。
//
// 按 rowid 定位单块，代价与全库命中数无关——这正是把 snippet 挪出宽查询的意义。
func (e *Engine) fillBest(h *SessionHit, bid int64, match string) error {
	var snip string
	err := e.db.QueryRow(`
SELECT b.seq, b.kind, b.tool_name,
       (SELECT snippet(blocks_fts, 0, char(2), char(3), '…', `+snippetTokensStr+`)
        FROM blocks_fts WHERE blocks_fts MATCH ? AND rowid = b.id)
FROM blocks b WHERE b.id = ?`, match, bid).
		Scan(&h.BestSeq, &h.BestKind, &h.BestTool, &snip)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	h.Snippet = Strip(snip) // 去掉 CJK 分隔标记后才可直接展示
	return nil
}

// recentSessions 处理「只有过滤条件、没有关键词」的情形（时间线与初始视图会用到）。
// 这时不存在「命中」，Hits 保持 0——拿消息数冒充命中数会让人误以为整个会话都被匹配了。
func (e *Engine) recentSessions(q Query) (Result, error) {
	where, args := q.filters()
	limit, offset := q.limit()

	rows, err := e.db.Query(`
SELECT s.id, s.source, s.session_uid, s.agent_label, s.file_path, s.project_path,
       s.started_at, s.ended_at, s.msg_count, COALESCE(s.summary, '')
FROM sessions s
JOIN blocks b ON b.session_id = s.id
WHERE s.alive = 1`+where+`
GROUP BY s.id
ORDER BY s.started_at DESC
LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()

	res := Result{Query: q}
	for rows.Next() {
		var h SessionHit
		if err := rows.Scan(&h.ID, &h.Source, &h.SessionUID, &h.AgentLabel, &h.FilePath,
			&h.ProjectPath, &h.StartedAt, &h.EndedAt, &h.MsgCount, &h.Summary); err != nil {
			return Result{}, err
		}
		res.Sessions = append(res.Sessions, h)
	}
	return res, rows.Err()
}

// SearchBlocks 返回块粒度的命中，按相关度排序。
func (e *Engine) SearchBlocks(q Query) ([]BlockHit, error) {
	match := BuildMatch(q.Text)
	if match == "" {
		return nil, nil
	}
	where, args := q.filters()
	limit, offset := q.limit()

	rows, err := e.db.Query(`
WITH f AS MATERIALIZED (`+ftsScoreCTE+`)
SELECT b.id, b.session_id, b.seq, b.ts, b.kind, b.tool_name, b.tool_use_id, b.truncated,
       f.score
FROM f
JOIN blocks   b ON b.id = f.bid
JOIN sessions s ON s.id = b.session_id
WHERE s.alive = 1`+where+`
ORDER BY f.score ASC
LIMIT ? OFFSET ?`, append(append([]any{match}, args...), limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BlockHit
	var ids []int64
	for rows.Next() {
		var h BlockHit
		var id int64
		if err := rows.Scan(&id, &h.SessionID, &h.Seq, &h.TS, &h.Kind, &h.ToolName,
			&h.ToolUseID, &h.Truncated, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 片段只对本页这几行按 rowid 取
	for i := range out {
		snip, err := e.snippetFor(ids[i], match)
		if err != nil {
			return nil, err
		}
		out[i].Snippet = snip
	}
	return out, nil
}

// snippetFor 取单个块的高亮片段。
func (e *Engine) snippetFor(bid int64, match string) (string, error) {
	var snip string
	err := e.db.QueryRow(`SELECT snippet(blocks_fts, 0, char(2), char(3), '…', `+snippetTokensStr+`)
FROM blocks_fts WHERE blocks_fts MATCH ? AND rowid = ?`, match, bid).Scan(&snip)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return Strip(snip), err
}

// Message 是回读视图里的一条消息。
type Message struct {
	Seq       int    `json:"seq"`
	TS        int64  `json:"ts"`
	Kind      string `json:"kind"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Truncated bool   `json:"truncated"`
	RawBytes  int    `json:"raw_bytes,omitempty"`
	Body      string `json:"body"`
}

// SessionView 是一个会话的回读视图（需求 3）。
type SessionView struct {
	SessionHit
	Alive    bool      `json:"alive"`
	Total    int       `json:"total"`
	FromSeq  int       `json:"from_seq"`
	Messages []Message `json:"messages"`
}

// GetSession 按时间正序返回会话消息，支持分页以免一次性渲染上万条（需求 3.3 / 4.2）。
func (e *Engine) GetSession(id int64, fromSeq, limit int) (SessionView, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var v SessionView
	var alive int
	err := e.db.QueryRow(`
SELECT s.id, s.source, s.session_uid, s.agent_label, s.file_path, s.project_path,
       s.started_at, s.ended_at, s.msg_count, COALESCE(s.summary,''), s.alive,
       (SELECT COUNT(*) FROM blocks WHERE session_id = s.id)
FROM sessions s WHERE s.id = ?`, id).
		Scan(&v.ID, &v.Source, &v.SessionUID, &v.AgentLabel, &v.FilePath, &v.ProjectPath,
			&v.StartedAt, &v.EndedAt, &v.MsgCount, &v.Summary, &alive, &v.Total)
	if err != nil {
		return v, err
	}
	v.Alive = alive == 1
	v.FromSeq = fromSeq

	rows, err := e.db.Query(`
SELECT seq, ts, kind, tool_name, tool_use_id, truncated, raw_bytes, body
FROM blocks WHERE session_id = ? AND seq >= ?
ORDER BY seq ASC LIMIT ?`, id, fromSeq, limit)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		var truncated int
		if err := rows.Scan(&m.Seq, &m.TS, &m.Kind, &m.ToolName, &m.ToolUseID,
			&truncated, &m.RawBytes, &m.Body); err != nil {
			return v, err
		}
		m.Truncated = truncated == 1
		m.Body = Strip(m.Body)
		v.Messages = append(v.Messages, m)
	}
	return v, rows.Err()
}

// ProjectStat 是项目维度的汇总，供过滤下拉与时间线使用。
type ProjectStat struct {
	ProjectPath string `json:"project_path"`
	Sessions    int    `json:"sessions"`
	LastAt      int64  `json:"last_at"`
}

func (e *Engine) Projects() ([]ProjectStat, error) {
	rows, err := e.db.Query(`
SELECT project_path, COUNT(*), MAX(ended_at)
FROM sessions WHERE alive = 1 AND project_path <> ''
GROUP BY project_path ORDER BY MAX(ended_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectStat
	for rows.Next() {
		var p ProjectStat
		if err := rows.Scan(&p.ProjectPath, &p.Sessions, &p.LastAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
