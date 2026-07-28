package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sub", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedSession(t *testing.T, st *Store, path string) int64 {
	t.Helper()
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "uuid-1", FilePath: path,
		ProjectPath: "/home/user/projects/chatdex", StartedAt: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// 索引库含明文凭证副本，权限是不可放宽的约束（需求非功能 Security）。
func TestPermissionsAre0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	st, err := Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != dirPerm {
		t.Errorf("目录权限 = %o, want %o", got, dirPerm)
	}
	for _, p := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			continue // 边车文件未生成则跳过
		}
		if got := fi.Mode().Perm(); got != filePerm {
			t.Errorf("%s 权限 = %o, want %o", p, got, filePerm)
		}
	}
}

func TestAppendBlocksIsSearchableViaFTS(t *testing.T) {
	st := openTemp(t)
	id := seedSession(t, st, "/tmp/a.jsonl")

	blocks := []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "做一个类似 timemachine 的管理工具"},
		{Seq: 1, TS: 1001, Kind: model.KindToolUse, ToolName: "Bash", ToolUseID: "t1", Body: `{"command":"rsync -av /src /dst"}`},
		{Seq: 2, TS: 1002, Kind: model.KindToolResult, ToolName: "Bash", ToolUseID: "t1",
			Truncated: true, RawBytes: 99999, Body: "sent 12 bytes"},
	}
	if err := st.AppendBlocks(id, blocks, Watermark{Size: 500, MTime: 42, Offset: 500}); err != nil {
		t.Fatal(err)
	}

	// 中文经归一化后必须可检索
	var n int
	q := `"` + search.NormalizeQuery("管理工具") + `"`
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM blocks_fts WHERE blocks_fts MATCH ?`, q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("中文命中 %d 条，want 1", n)
	}
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM blocks_fts WHERE blocks_fts MATCH 'rsync'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("工具入参命中 %d 条，want 1", n)
	}

	// 水位与截断元数据落库
	wm, ok, err := st.Watermark("/tmp/a.jsonl")
	if err != nil || !ok {
		t.Fatalf("取水位失败 ok=%v err=%v", ok, err)
	}
	if wm.Size != 500 || wm.MTime != 42 || wm.Offset != 500 {
		t.Errorf("水位 = %+v", wm)
	}
	var truncated, raw int
	if err := st.DB().QueryRow(`SELECT truncated, raw_bytes FROM blocks WHERE seq=2`).Scan(&truncated, &raw); err != nil {
		t.Fatal(err)
	}
	if truncated != 1 || raw != 99999 {
		t.Errorf("截断元数据 truncated=%d raw_bytes=%d", truncated, raw)
	}
}

// blocks 与 blocks_fts 必须在同一事务内，回滚后两边都不得有残留。
func TestFailedTransactionLeavesNoResidue(t *testing.T) {
	st := openTemp(t)
	seedSession(t, st, "/tmp/b.jsonl")

	// 写向一个不存在的 session_id：第一条块能插入，第二条触发外键约束失败，
	// 整个事务必须回滚——blocks 与 blocks_fts 两边都不得留下第一条。
	blocks := []model.Block{
		{Seq: 0, Kind: model.KindUser, Body: "第一条"},
		{Seq: 1, Kind: model.KindUser, Body: "第二条"},
	}
	if err := st.AppendBlocks(999999, blocks, Watermark{}); err == nil {
		t.Fatal("期望外键约束报错，实际成功了")
	}

	var nBlocks, nFTS int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM blocks`).Scan(&nBlocks); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM blocks_fts`).Scan(&nFTS); err != nil {
		t.Fatal(err)
	}
	if nBlocks != 0 || nFTS != 0 {
		t.Errorf("回滚后仍有残留 blocks=%d blocks_fts=%d", nBlocks, nFTS)
	}
}

// 删除块时 FTS 索引必须同步收缩，否则检索会指向已不存在的内容。
func TestResetSessionClearsFTS(t *testing.T) {
	st := openTemp(t)
	id := seedSession(t, st, "/tmp/c.jsonl")
	if err := st.AppendBlocks(id, []model.Block{
		{Seq: 0, Kind: model.KindUser, Body: "restic 增量备份"},
	}, Watermark{Size: 10, MTime: 1, Offset: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetSession(id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM blocks_fts WHERE blocks_fts MATCH 'restic'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("重置后 FTS 仍命中 %d 条", n)
	}
	wm, _, err := st.Watermark("/tmp/c.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if wm.Offset != 0 || wm.Size != 0 {
		t.Errorf("重置后水位未归零: %+v", wm)
	}
}

func TestMarkDeadAndAlivePaths(t *testing.T) {
	st := openTemp(t)
	seedSession(t, st, "/tmp/d1.jsonl")
	if _, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceCodex, SessionUID: "u2", FilePath: "/tmp/d2.jsonl",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDead("/tmp/d2.jsonl"); err != nil {
		t.Fatal(err)
	}
	paths, err := st.AlivePaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/tmp/d1.jsonl" {
		t.Errorf("AlivePaths = %v", paths)
	}
}

func TestStatsReflectsContent(t *testing.T) {
	st := openTemp(t)
	id := seedSession(t, st, "/tmp/e.jsonl")
	if err := st.AppendBlocks(id, []model.Block{
		{Seq: 0, Kind: model.KindUser, Body: "hello"},
		{Seq: 1, Kind: model.KindToolResult, ToolName: "Bash", Truncated: true, RawBytes: 5000, Body: "out"},
	}, Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	s, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if s.Sessions != 1 || s.Blocks != 2 || s.TruncatedBlocks != 1 {
		t.Errorf("Stats = %+v", s)
	}
	if s.BlocksByKind["user"] != 1 || s.BlocksByKind["tool_result"] != 1 {
		t.Errorf("BlocksByKind = %v", s.BlocksByKind)
	}
	fi, err := os.Stat(st.path)
	if err != nil {
		t.Fatal(err)
	}
	if s.DBBytes < fi.Size() {
		t.Errorf("DBBytes=%d 小于主库文件大小 %d", s.DBBytes, fi.Size())
	}
}

// 幂等：重复 upsert 同一文件不得产生第二个会话行。
func TestUpsertSessionIsIdempotent(t *testing.T) {
	st := openTemp(t)
	id1 := seedSession(t, st, "/tmp/f.jsonl")
	id2 := seedSession(t, st, "/tmp/f.jsonl")
	if id1 != id2 {
		t.Errorf("重复 upsert 产生了不同 id: %d vs %d", id1, id2)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("会话行数 = %d, want 1", n)
	}
}

// 已有的库要能升级：CREATE TABLE IF NOT EXISTS 对存量库加不了列，
// 忘了迁移的话新字段在开发机（新建库）一切正常，一升级到线上索引就炸。
func TestMigrationAddsColumnToExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	// 造一个「旧版」库：没有 summary_msg_count 列
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE sessions DROP COLUMN summary_msg_count`); err != nil {
		t.Skipf("本机 SQLite 不支持 DROP COLUMN，跳过：%v", err)
	}
	st.Close()

	// 重新打开应自动补列
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("打开旧版库失败（迁移没跑）: %v", err)
	}
	defer st2.Close()
	if _, err := st2.DB().Exec(`SELECT summary_msg_count FROM sessions LIMIT 1`); err != nil {
		t.Errorf("迁移未补上列: %v", err)
	}

	// 再开一次：迁移必须可重复执行
	st3, err := Open(path)
	if err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	st3.Close()
}
