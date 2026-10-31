package index

import (
	"context"
	"fmt"

	"modernc.org/sqlite"
)

// SnapshotTo 把索引库的一致副本写到 dst（SQLite 在线备份 API，页对页复制）。
//
// 不用 VACUUM INTO：它会把整个库重新排版，页面搬家后 restic 的内容分块对不上——
// 同一段真实写入（62 个新块、3 分钟）实测 VACUUM INTO 的副本让 restic 再写 1881 MB，
// 在线备份的副本只写 35 MB（R27 对照实验）。代价是副本不压实（含空闲页），
// 对备份来说无所谓：空闲页每天都一样，去重掉了。
//
// Step(-1) 一次拷完而不是分步：分步拷贝期间源库若被写，SQLite 会从头重来；
// 一次到底只在源库上持一个读事务，扫描与检索照常。
func (s *Store) SnapshotTo(ctx context.Context, dst string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		src, ok := dc.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("底层连接不支持在线备份（%T）", dc)
		}
		b, err := src.NewBackup(dst)
		if err != nil {
			return err
		}
		if _, err := b.Step(-1); err != nil {
			_ = b.Finish()
			return err
		}
		return b.Finish()
	})
}
