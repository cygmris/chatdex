package index

import (
	"path/filepath"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

// 迁移把 Grok 会话的身份从 chat_history.jsonl 换到 updates.jsonl，
// 并清空块、把水位归零，让下一轮扫描整个重建。
//
// 🔴 这条钉住的是 R25 的核心风险：file_path 是 UNIQUE 键且水位挂在它上面。
// 不迁移就会出现同一个会话两份（旧路径一份、新路径一份）。
func TestGrokRepairMigratesFilePath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	oldPath := "/home/u/.grok/sessions/%2Ftmp%2Fp/uuid-1/chat_history.jsonl"
	newPath := "/home/u/.grok/sessions/%2Ftmp%2Fp/uuid-1/updates.jsonl"

	// 造一个「改造前」的库：grok 会话指着 chat_history.jsonl，有块、有水位、有摘要
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceGrok, SessionUID: "uuid-1", FilePath: oldPath,
		ProjectPath: "/tmp/p", StartedAt: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "压缩前的提问"},
		{Seq: 1, TS: 1001, Kind: model.KindAssistant, Body: "压缩前的回答"},
	}
	if err := st.AppendBlocks(id, blocks, Watermark{Size: 4096, MTime: 55, Offset: 4096}); err != nil {
		t.Fatal(err)
	}
	// 种一份摘要——不种的话 summary_* 三个字段本来就是零值，
	// 「有没有清零」的断言就没有鉴别力（变异扫描当场抓到过这一点）。
	if _, err := st.DB().Exec(
		`UPDATE sessions SET summary = ?, summary_at = ?, summary_msg_count = ? WHERE id = ?`,
		"压缩后那 15% 内容生成的旧摘要", 12345, 2, id); err != nil {
		t.Fatal(err)
	}

	// 对照：一个 claude 会话，路径里同样有 chat_history.jsonl 字样，**不该被动**
	claudePath := "/home/u/.claude/projects/p/chat_history.jsonl"
	cid, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "c1", FilePath: claudePath, StartedAt: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendBlocks(cid, []model.Block{{Seq: 0, TS: 1, Kind: model.KindUser, Body: "别动我"}},
		Watermark{Size: 10, MTime: 1, Offset: 10}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// 重开 = 跑一遍 repairs
	st2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	// ① 身份已改写
	var gotPath string
	if err := st2.DB().QueryRow(`SELECT file_path FROM sessions WHERE id = ?`, id).Scan(&gotPath); err != nil {
		t.Fatal(err)
	}
	if gotPath != newPath {
		t.Errorf("file_path = %q，想要 %q", gotPath, newPath)
	}
	if _, _, err := st2.Watermark(oldPath); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st2.Watermark(oldPath); ok {
		t.Error("旧路径还查得到水位 —— 迁移没生效，会造成同一会话两份")
	}

	// ② 水位归零、摘要作废
	wm, ok, err := st2.Watermark(newPath)
	if err != nil || !ok {
		t.Fatalf("新路径查不到水位：ok=%v err=%v", ok, err)
	}
	if wm.Size != 0 || wm.MTime != 0 || wm.Offset != 0 {
		t.Errorf("水位 = %+v，想要全零（否则扫描器会从 chat_history 的偏移量续读 updates.jsonl）", wm)
	}

	// ②b 摘要作废：旧摘要是按压缩后那 15% 内容生成的，重建后不再成立
	var sumAt, sumMsgN int64
	var sum *string
	if err := st2.DB().QueryRow(
		`SELECT summary, summary_at, summary_msg_count FROM sessions WHERE id = ?`, id).
		Scan(&sum, &sumAt, &sumMsgN); err != nil {
		t.Fatal(err)
	}
	if sum != nil || sumAt != 0 || sumMsgN != 0 {
		t.Errorf("摘要没作废：summary=%v at=%d msg_count=%d —— "+
			"summary_msg_count 不清零，队列会以为摘要还是新的", sum, sumAt, sumMsgN)
	}

	// ③ 块已清空 —— 那些是从被压缩后的文件解析出来的，要整个重建
	var n int
	if err := st2.DB().QueryRow(`SELECT COUNT(*) FROM blocks WHERE session_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("旧块还剩 %d 条 —— 应清空由下一轮扫描重建", n)
	}
	// FTS 也要跟着清（靠 blocks 的触发器）
	var ftsN int
	if err := st2.DB().QueryRow(
		`SELECT COUNT(*) FROM blocks_fts WHERE blocks_fts MATCH '压缩前的提问'`).Scan(&ftsN); err != nil {
		t.Fatal(err)
	}
	if ftsN != 0 {
		t.Errorf("FTS 里还有 %d 条旧内容 —— 触发器没同步", ftsN)
	}

	// ④ 对照：claude 会话一点没动
	var cPath string
	var cn int
	if err := st2.DB().QueryRow(`SELECT file_path FROM sessions WHERE id = ?`, cid).Scan(&cPath); err != nil {
		t.Fatal(err)
	}
	if err := st2.DB().QueryRow(`SELECT COUNT(*) FROM blocks WHERE session_id = ?`, cid).Scan(&cn); err != nil {
		t.Fatal(err)
	}
	if cPath != claudePath || cn != 1 {
		t.Errorf("claude 会话被误伤：path=%q blocks=%d —— 迁移必须按 source='grok' 限定", cPath, cn)
	}
}

// 迁移必须幂等：repairs 每次启动都跑。
//
// 第二次运行时 LIKE 恒不命中；而②那条 DELETE 的谓词
// （offset=0 AND msg_count=0）此时会选中已迁移的会话——但它已经没有块了，
// 删 0 行。真正危险的是它误删**新扫描进来的**会话，所以这里连带钉住那一点。
func TestGrokRepairIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	newPath := "/home/u/.grok/sessions/%2Ftmp%2Fp/uuid-2/updates.jsonl"

	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceGrok, SessionUID: "uuid-2", FilePath: newPath, StartedAt: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 一个已经索引好的 grok 会话：有块、水位非零
	if err := st.AppendBlocks(id, []model.Block{{Seq: 0, TS: 1, Kind: model.KindUser, Body: "已索引"}},
		Watermark{Size: 99, MTime: 9, Offset: 99}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	for i := 0; i < 3; i++ {
		s, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		var path string
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM blocks WHERE session_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if err := s.DB().QueryRow(`SELECT file_path FROM sessions WHERE id = ?`, id).Scan(&path); err != nil {
			t.Fatal(err)
		}
		s.Close()
		if n != 1 {
			t.Fatalf("第 %d 次启动后块数 = %d，想要 1 —— 已索引好的 grok 会话被误删了", i+1, n)
		}
		if path != newPath {
			t.Fatalf("第 %d 次启动后 file_path = %q —— 已迁移的路径又被改了", i+1, path)
		}
	}
}

// 🔴 崩溃恢复：身份已改、块还没清 —— ②必须清得掉。
//
// 这个场景是本迁移唯一不靠事务保证的地方，而它有一个非显而易见的陷阱：
// repairs 里的 msg_count 自愈语句**跑在本迁移之前**，会把中间态会话的
// msg_count 从 0 重算回真实块数。所以②的判据只能是 "offset" = 0；
// 一旦加上 msg_count = 0，这个场景恰好不命中，残块永远留在库里
// —— 而搜索会同时返回旧块和重建后的新块，同一段话出现两遍。
func TestGrokRepairCleansCrashMidState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	newPath := "/home/u/.grok/sessions/%2Ftmp%2Fp/uuid-3/updates.jsonl"

	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceGrok, SessionUID: "uuid-3", FilePath: newPath, StartedAt: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendBlocks(id, []model.Block{{Seq: 0, TS: 1, Kind: model.KindUser, Body: "残块"}},
		Watermark{Size: 50, MTime: 5, Offset: 50}); err != nil {
		t.Fatal(err)
	}
	// 手工造出中间态：路径已是新的、水位已归零，块还在
	if _, err := st.DB().Exec(
		`UPDATE sessions SET size=0, mtime=0, "offset"=0, msg_count=0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	var n int
	if err := st2.DB().QueryRow(`SELECT COUNT(*) FROM blocks WHERE session_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("中间态的残块还剩 %d 条 —— 重建后会与新块并存，同一段话搜出两遍", n)
	}
}
