package search

import (
	"database/sql"
	"errors"
)

// 父子关系查询。关系本身 R1 就存在库里（sessions.parent_uid），这里只是把它用起来。

// childListLimit 是子代理列表一次返回的条数上限。
//
// 实测有主会话挂着 **350** 个子代理，所以必须限量：那不是一个能读完的列表，
// 而且全铺出来会把回读页顶开。要逐个看应该用「仅子代理」过滤加检索。
const childListLimit = 50

// Children 返回某会话的子代理（按开始时间正序）与**总数**。
//
// 总数单独查一次而不是用 len(items)：limit 会截断，而界面上要显示「共 350 个」——
// 拿返回条数当总数会永远显示 50。
func (e *Engine) Children(id int64, limit int) ([]SessionHit, int, error) {
	if limit <= 0 || limit > childListLimit {
		limit = childListLimit
	}

	var total int
	err := e.db.QueryRow(`
SELECT COUNT(*) FROM sessions c
JOIN sessions p ON p.session_uid = c.parent_uid
WHERE p.id = ? AND c.alive = 1`, id).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}

	rows, err := e.db.Query(`
SELECT c.id, c.source, c.session_uid, c.file_path, c.project_path,
       c.started_at, c.ended_at, c.msg_count,
       COALESCE(c.summary, ''), c.summary_model, c.summary_at, c.title
FROM sessions c
JOIN sessions p ON p.session_uid = c.parent_uid
WHERE p.id = ? AND c.alive = 1
ORDER BY c.started_at ASC
LIMIT ?`, id, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []SessionHit
	for rows.Next() {
		var h SessionHit
		if err := rows.Scan(&h.ID, &h.Source, &h.SessionUID, &h.FilePath, &h.ProjectPath,
			&h.StartedAt, &h.EndedAt, &h.MsgCount,
			&h.Summary, &h.SummaryModel, &h.SummaryAt, &h.Title); err != nil {
			return nil, 0, err
		}
		h.IsSub = true // 能出现在这里就一定是子代理，不必再查一列
		out = append(out, h)
	}
	return out, total, rows.Err()
}

// Parent 返回某子代理的主会话。
//
// 三种情形都返回 (nil, nil)：不是子代理、主会话不在索引里、主会话已下线。
// 实测 1554 个子代理的 parent_uid 100% 能对上，但索引可以被部分删除，
// 所以缺失是正常状态而不是错误——页面据此只标身份、不给死链。
func (e *Engine) Parent(id int64) (*SessionHit, error) {
	var h SessionHit
	err := e.db.QueryRow(`
SELECT p.id, p.source, p.session_uid, p.file_path, p.project_path,
       p.started_at, p.ended_at, p.msg_count,
       COALESCE(p.summary, ''), p.summary_model, p.summary_at, p.title
FROM sessions c
JOIN sessions p ON p.session_uid = c.parent_uid
WHERE c.id = ? AND c.parent_uid != '' AND p.alive = 1`, id).
		Scan(&h.ID, &h.Source, &h.SessionUID, &h.FilePath, &h.ProjectPath,
			&h.StartedAt, &h.EndedAt, &h.MsgCount,
			&h.Summary, &h.SummaryModel, &h.SummaryAt, &h.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // 三种情形同义：不是子代理 / 主会话不在索引里 / 主会话已下线
	}
	if err != nil {
		return nil, err // 真出错要报出来，别和"没有父会话"混为一谈
	}
	return &h, nil
}
