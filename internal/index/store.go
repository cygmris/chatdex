// Package index 负责索引库的建立、写入与增量水位维护。
//
// 安全约束（需求非功能 Security，不可放宽）：索引库含工具结果里 cat/env/curl 的
// 明文密钥，等于一份集中的凭证副本 —— 目录 0700、库文件 0600，且只放用户私有目录。
package index

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"

	_ "modernc.org/sqlite"
)

const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// Store 是索引层唯一的数据出入口。
type Store struct {
	db   *sql.DB
	path string
}

// Watermark 是一个会话文件的增量索引水位。
type Watermark struct {
	SessionID int64
	Size      int64
	MTime     int64
	Offset    int64
}

// Open 打开（必要时建立）索引库。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("建索引目录: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("建表: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, fmt.Errorf("迁移 %q: %w", m, err)
		}
	}
	for _, r := range repairs {
		if _, err := db.Exec(r); err != nil {
			db.Close()
			return nil, fmt.Errorf("自愈 %q: %w", r, err)
		}
	}
	s := &Store{db: db, path: path}
	// WAL 的 -wal / -shm 边车文件同样含数据，一并收权限。
	// 建表已经写过一次，此刻它们已存在。
	if err := s.tightenPerms(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) tightenPerms() error {
	for _, p := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			continue // 边车文件可能尚未生成
		}
		if err := os.Chmod(p, filePerm); err != nil {
			return fmt.Errorf("收紧 %s 权限: %w", p, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB 供检索层只读查询使用。写入一律走本包的方法。
func (s *Store) DB() *sql.DB { return s.db }

// UpsertSession 按 file_path 建立或更新会话元数据，返回会话 id。
// 不动水位字段——那是 AppendBlocks / ResetSession 的职责。
// meta 是一张 k/v 小表，记「跑过一次就不必再跑」这类水位。
// 与 migrations（结构变更）和 repairs（数据自愈）都不同：那两个每次启动都执行，
// 这个是用来**避免**重复执行的。
func (s *Store) metaGet(k string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) metaSet(k, v string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

// SetTitle 记下会话名。
//
// 与 UpsertSession 分开是因为时序：Upsert 在 Parse **之前**跑（它只有 Meta，
// 而 Meta 只读文件头 50 行拿不到标题），标题是 Parse 逐行走完才知道的。
//
// ⚠️ 空标题一律不写。增量续读时若这一段里没有标题记录，cur.Title 就是空——
// 这时候写下去会把之前存的名字抹掉。
func (s *Store) SetTitle(id int64, title string) error {
	if strings.TrimSpace(title) == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, title, id)
	return err
}

func (s *Store) UpsertSession(m model.SessionMeta) (int64, error) {
	_, err := s.db.Exec(`
INSERT INTO sessions (source, session_uid, parent_uid, agent_label, file_path, project_path, started_at, ended_at, msg_count, alive)
VALUES (?,?,?,?,?,?,?,?,?,1)
ON CONFLICT(file_path) DO UPDATE SET
    source=excluded.source, session_uid=excluded.session_uid,
    parent_uid=excluded.parent_uid, agent_label=excluded.agent_label,
    project_path=CASE WHEN excluded.project_path <> '' THEN excluded.project_path ELSE sessions.project_path END,
    started_at=CASE WHEN sessions.started_at = 0 THEN excluded.started_at ELSE sessions.started_at END,
    ended_at=MAX(sessions.ended_at, excluded.ended_at),
    alive=1`,
		string(m.Source), m.SessionUID, m.ParentUID, m.AgentLabel, m.FilePath,
		m.ProjectPath, m.StartedAt, m.EndedAt, m.MsgCount)
	if err != nil {
		return 0, fmt.Errorf("upsert 会话 %s: %w", m.FilePath, err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM sessions WHERE file_path = ?`, m.FilePath).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// AppendBlocks 在**同一事务**内写入内容块并推进水位。
//
// 同事务是可靠性要求：崩溃/断电后重启必须从一致状态恢复，不得出现
// 「块写进去了但水位没推进」（重复索引）或反过来（内容丢失）。
func (s *Store) AppendBlocks(sessionID int64, blocks []model.Block, wm Watermark) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(blocks) > 0 {
		stmt, err := tx.Prepare(`INSERT INTO blocks (session_id, seq, ts, kind, tool_name, tool_use_id, truncated, raw_bytes, body)
                                 VALUES (?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, b := range blocks {
			truncated := 0
			if b.Truncated {
				truncated = 1
			}
			if _, err := stmt.Exec(sessionID, b.Seq, b.TS, string(b.Kind), b.ToolName, b.ToolUseID,
				truncated, b.RawBytes, search.NormalizeIndex(b.Body)); err != nil {
				return fmt.Errorf("写入块 seq=%d: %w", b.Seq, err)
			}
		}
	}

	if _, err := tx.Exec(`UPDATE sessions SET size=?, mtime=?, offset=?,
                          msg_count=(SELECT COUNT(*) FROM blocks WHERE session_id=? AND seq>=0),
                          ended_at=MAX(ended_at, ?)
                          WHERE id=?`,
		wm.Size, wm.MTime, wm.Offset, sessionID, maxTS(blocks), sessionID); err != nil {
		return fmt.Errorf("推进水位: %w", err)
	}
	return tx.Commit()
}

func maxTS(blocks []model.Block) int64 {
	var m int64
	for _, b := range blocks {
		if b.TS > m {
			m = b.TS
		}
	}
	return m
}

// Watermark 取某个原始文件的索引水位。ok=false 表示该文件从未被索引过。
func (s *Store) Watermark(filePath string) (Watermark, bool, error) {
	var w Watermark
	err := s.db.QueryRow(`SELECT id, size, mtime, offset FROM sessions WHERE file_path = ?`, filePath).
		Scan(&w.SessionID, &w.Size, &w.MTime, &w.Offset)
	if err == sql.ErrNoRows {
		return Watermark{}, false, nil
	}
	// err 照常返回给调用方——这里的 `err == nil` 只是把「取到了吗」独立成一个
	// 返回值，不是吞掉错误。（R9 复核过，保留。）
	return w, err == nil, err
}

// ResetSession 清空一个会话已索引的全部内容并把水位归零。
// 用于原始文件被截断或原地改写的情形——那时增量续读没有意义。
func (s *Store) ResetSession(sessionID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM blocks WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	// 内容整个重建了，旧摘要不再成立——清掉，由队列重新生成
	if _, err := tx.Exec(`UPDATE sessions SET size=0, mtime=0, offset=0, msg_count=0,
        summary=NULL, summary_at=0, summary_msg_count=0 WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkDead 把原始文件已消失的会话标记为失效：检索结果不再指向不存在的文件。
// 只标记不删除——历史内容仍在库里，文件回来时可直接复活。
func (s *Store) MarkDead(filePath string) error {
	_, err := s.db.Exec(`UPDATE sessions SET alive = 0 WHERE file_path = ?`, filePath)
	return err
}

// AlivePaths 返回当前标记为存活的全部原始文件路径，供扫描器检出已删除的文件。
func (s *Store) AlivePaths() ([]string, error) {
	rows, err := s.db.Query(`SELECT file_path FROM sessions WHERE alive = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Stats 是索引占用统计（需求 7.7：索引体量因纳入工具结果而放大，须可观测）。
type Stats struct {
	DBBytes         int64          `json:"db_bytes"`
	Sessions        int            `json:"sessions"`
	DeadSessions    int            `json:"dead_sessions"`
	Blocks          int            `json:"blocks"`
	BlocksByKind    map[string]int `json:"blocks_by_kind"`
	TruncatedBlocks int            `json:"truncated_blocks"`
	Summarized      int            `json:"summarized"`
}

func (s *Store) Stats() (Stats, error) {
	st := Stats{BlocksByKind: map[string]int{}}
	for _, p := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		// 这里的 err 就是「文件不存在」本身，正是要判断的东西，不是被吞掉的错误。
		// （R9 复核过，保留。）
		if fi, err := os.Stat(p); err == nil {
			st.DBBytes += fi.Size()
		}
	}
	row := s.db.QueryRow(`SELECT
        (SELECT COUNT(*) FROM sessions WHERE alive=1),
        (SELECT COUNT(*) FROM sessions WHERE alive=0),
        (SELECT COUNT(*) FROM blocks),
        (SELECT COUNT(*) FROM blocks WHERE truncated=1),
        (SELECT COUNT(*) FROM sessions WHERE summary IS NOT NULL)`)
	if err := row.Scan(&st.Sessions, &st.DeadSessions, &st.Blocks, &st.TruncatedBlocks, &st.Summarized); err != nil {
		return st, err
	}
	rows, err := s.db.Query(`SELECT kind, COUNT(*) FROM blocks GROUP BY kind`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return st, err
		}
		st.BlocksByKind[k] = n
	}
	return st, rows.Err()
}
