package index

import "time"

// 全快照路径并集索引的读写。
//
// backup 包不认识 SQL（与「解析器认识 JSONL、索引层不认识」是同一条分层纪律），
// 所以这些方法由上层注入给扫描器。

// ScannedSnapshots 返回已经扫过的快照 id 集合。
func (s *Store) ScannedSnapshots() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT snapshot_id FROM backup_scanned`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// RecordSnapshotFiles 把一个快照里的文件路径并入索引，并把该快照记为已扫。
//
// **两件事在同一个事务里**：分开做的话，进程在中间挂掉就会留下一个
// 「记为已扫但文件没入库」的快照——而它永远不会被重扫，那些路径就此消失，
// 于是「永久丢失」又多出一批假的。
func (s *Store) RecordSnapshotFiles(snapshotID string, paths []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ins, err := tx.Prepare(`INSERT OR IGNORE INTO backup_seen(path) VALUES(?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, p := range paths {
		if _, err := ins.Exec(p); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO backup_scanned(snapshot_id, scanned_at) VALUES(?, ?)`,
		snapshotID, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// SeenInAnySnapshot 判断这些路径里哪些曾出现在任一快照。
//
// 一次查完而不是逐条问：调用方手上是全部已消失的会话（实测 1973 条），
// 逐条往返一次就是 1973 次查询。
func (s *Store) SeenInAnySnapshot(paths []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(paths) == 0 {
		return out, nil
	}
	// 分批：SQLite 的参数上限默认 999，超了会直接报错而不是截断
	const batch = 900
	for i := 0; i < len(paths); i += batch {
		end := min(i+batch, len(paths))
		chunk := paths[i:end]
		q := `SELECT path FROM backup_seen WHERE path IN (?` +
			repeatComma(len(chunk)-1) + `)`
		args := make([]any, len(chunk))
		for j, p := range chunk {
			args[j] = p
		}
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			out[p] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

func repeatComma(n int) string {
	b := make([]byte, 0, n*2)
	for range n {
		b = append(b, ',', '?')
	}
	return string(b)
}
