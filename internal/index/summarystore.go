package index

import (
	"database/sql"
	"time"

	"github.com/cygmris/chatdex/internal/search"
)

// summarySeq 是摘要块在会话内的序号。
// 取 -1 是因为摘要不属于对话本身：回读视图从 seq>=0 开始翻页，摘要不会混进去，
// 但它照常进 FTS5，于是关键词能命中摘要里出现、原文没出现的概念词（需求 11.2）。
const summarySeq = -1

// 会话在摘要生成后继续增长到什么程度就该重做（需求 11.8）。
const (
	regrowRatio = 1.2 // 消息数涨了 20%
	regrowDelta = 50  // 或者多了 50 条
)

// Progress 是摘要队列的进度。
type Progress struct {
	Done    int `json:"done"`
	Pending int `json:"pending"`
	Running int `json:"running"`
	Failed  int `json:"failed"`
}

// EnqueueSummary 把一个会话放进摘要队列。
//
// session_id 是主键，重复入队必须显式覆盖：R11.8 要求会话增长后能重做摘要，
// 若默认忽略冲突，重新入队会静默失败、摘要永远停在旧版本。
func (s *Store) EnqueueSummary(sessionID int64, priority int) error {
	_, err := s.db.Exec(`
INSERT INTO summary_queue (session_id, priority, state, attempts, err, updated_at)
VALUES (?, ?, 'pending', 0, '', ?)
ON CONFLICT(session_id) DO UPDATE SET
    priority   = MIN(summary_queue.priority, excluded.priority),
    state      = 'pending',
    attempts   = 0,
    err        = '',
    updated_at = excluded.updated_at`,
		sessionID, priority, time.Now().Unix())
	return err
}

// recentWindow 内结束的会话算「新会话」，优先出队（需求 11.4）。
const recentWindow = 24 * time.Hour

// EnqueueMissing 把「缺摘要」与「摘要后已显著增长」的会话补进队列。
// 新会话优先级 0，历史批量补齐优先级 1——首次索引不该阻塞在全量摘要上。
func (s *Store) EnqueueMissing() (int, error) {
	now := time.Now()
	res, err := s.db.Exec(`
INSERT INTO summary_queue (session_id, priority, state, attempts, err, updated_at)
SELECT s.id,
       CASE WHEN s.ended_at >= ? THEN 0 ELSE 1 END,
       'pending', 0, '', ?
FROM sessions s
WHERE s.alive = 1
  AND s.msg_count > 0
  AND (s.summary IS NULL
       OR s.msg_count > MAX(s.summary_msg_count * ?, s.summary_msg_count + ?))
  -- failed 也必须排除。少了它，下面的 ON CONFLICT 会把失败任务重置回 pending
  -- 且 attempts 清零 —— FailSummary 那句「重试 3 次后置 failed，不再占着队列」
  -- 被完全抵消，于是一条生成不了的会话每 2 分钟复活一次、永远排在最前面，
  -- 其余任务全部饿死。**实测这样卡了两天，而 failed 计数因为活不过一轮恒为 0，
  -- 界面上完全看不出来。** 失败要重来只能显式触发（RetrySummary/RetryAllFailed）。
  AND NOT EXISTS (SELECT 1 FROM summary_queue q
                  WHERE q.session_id = s.id AND q.state IN ('pending','running','failed'))
ON CONFLICT(session_id) DO UPDATE SET
    priority = excluded.priority,
    state = 'pending', attempts = 0, err = '', updated_at = excluded.updated_at`,
		now.Add(-recentWindow).Unix(), now.Unix(), regrowRatio, regrowDelta)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// NextSummary 取一个待处理会话并置为 running。新会话优先。
func (s *Store) NextSummary() (int64, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(`SELECT session_id FROM summary_queue
WHERE state = 'pending' ORDER BY priority ASC, session_id ASC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(`UPDATE summary_queue SET state='running', updated_at=? WHERE session_id=?`,
		time.Now().Unix(), id); err != nil {
		return 0, false, err
	}
	return id, true, tx.Commit()
}

// SetSummary 写入摘要：会话字段 + FTS 里的摘要块 + 队列置 done，同一事务。
func (s *Store) SetSummary(sessionID int64, text, model string, msgCount int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE sessions
SET summary=?, summary_model=?, summary_at=?, summary_msg_count=? WHERE id=?`,
		text, model, now, msgCount, sessionID); err != nil {
		return err
	}
	// 旧摘要块先删再插，避免同一会话堆积多条摘要
	if _, err := tx.Exec(`DELETE FROM blocks WHERE session_id=? AND kind='summary'`, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO blocks (session_id, seq, ts, kind, tool_name, tool_use_id, truncated, raw_bytes, body)
VALUES (?, ?, ?, 'summary', '', '', 0, ?, ?)`,
		sessionID, summarySeq, now, len(text), search.NormalizeIndex(text)); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE summary_queue SET state='done', err='', updated_at=? WHERE session_id=?`,
		now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// SkipSummary 把「没有可摘要内容」的会话终结掉。
//
// 这类会话（只有元数据、没有消息）重试多少次都一样，算成失败会让进度条
// 一直挂着一个永远修不好的「失败 N」。存空摘要即可：既不再入队，也不虚报失败。
func (s *Store) SkipSummary(sessionID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE sessions SET summary='', summary_at=?, summary_msg_count=msg_count WHERE id=?`,
		now, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE summary_queue SET state='done', err='', updated_at=? WHERE session_id=?`,
		now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailSummary 记一次失败。重试 3 次后置 failed，不再占着队列。
func (s *Store) FailSummary(sessionID int64, msg string) error {
	_, err := s.db.Exec(`UPDATE summary_queue
SET attempts = attempts + 1,
    state = CASE WHEN attempts + 1 >= 3 THEN 'failed' ELSE 'pending' END,
    err = ?, updated_at = ?
WHERE session_id = ?`, msg, time.Now().Unix(), sessionID)
	return err
}

// RequeueRunning 把上次退出时卡在 running 的任务放回 pending。
// 进程可能在任何时刻被 kill，「可中断可续跑」要求重启后不留僵尸任务。
func (s *Store) RequeueRunning() error {
	_, err := s.db.Exec(`UPDATE summary_queue SET state='pending' WHERE state='running'`)
	return err
}

func (s *Store) SummaryProgress() (Progress, error) {
	var p Progress
	err := s.db.QueryRow(`SELECT
    (SELECT COUNT(*) FROM summary_queue WHERE state='done'),
    (SELECT COUNT(*) FROM summary_queue WHERE state='pending'),
    (SELECT COUNT(*) FROM summary_queue WHERE state='running'),
    (SELECT COUNT(*) FROM summary_queue WHERE state='failed')`).
		Scan(&p.Done, &p.Pending, &p.Running, &p.Failed)
	return p, err
}

// Failure 是一条失败任务的展示信息。
type Failure struct {
	SessionID int64  `json:"session_id"`
	Title     string `json:"title,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Err       string `json:"err"`
	Attempts  int    `json:"attempts"`
	UpdatedAt int64  `json:"updated_at"`
}

// Failures 返回失败清单与**总数**。
//
// 总数单独查，不用 len(items)：limit 会截断，而界面要显示「共 N 条失败」——
// 拿返回条数当总数会永远显示 limit（R8 的 Children 栽过同一处）。
func (s *Store) Failures(limit int) ([]Failure, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM summary_queue WHERE state='failed'`).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	rows, err := s.db.Query(`
SELECT q.session_id, s.title, COALESCE(s.summary,''), q.err, q.attempts, q.updated_at
FROM summary_queue q
JOIN sessions s ON s.id = q.session_id
WHERE q.state = 'failed'
ORDER BY q.updated_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Failure
	for rows.Next() {
		var f Failure
		if err := rows.Scan(&f.SessionID, &f.Title, &f.Summary,
			&f.Err, &f.Attempts, &f.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// RetrySummary 把一条失败任务放回队列。返回是否真的有这条失败任务。
func (s *Store) RetrySummary(sessionID int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE summary_queue
SET state='pending', attempts=0, err='', updated_at=?
WHERE session_id=? AND state='failed'`, time.Now().Unix(), sessionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RetryAllFailed 把全部失败任务放回队列，返回条数。
func (s *Store) RetryAllFailed() (int, error) {
	res, err := s.db.Exec(`UPDATE summary_queue
SET state='pending', attempts=0, err='', updated_at=?
WHERE state='failed'`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Throughput 返回最近 1 小时 / 24 小时完成的摘要数。
// 取 sessions.summary_at 这个既有字段，不为此新增采样表。
func (s *Store) Throughput() (lastHour, last24h int, err error) {
	now := time.Now().Unix()
	err = s.db.QueryRow(`SELECT
    (SELECT COUNT(*) FROM sessions WHERE summary_at > ?),
    (SELECT COUNT(*) FROM sessions WHERE summary_at > ?)`,
		now-3600, now-86400).Scan(&lastHour, &last24h)
	return
}
