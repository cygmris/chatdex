package index

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

// 副本必须是可打开、内容相等的 SQLite 库；而且是在源库**持续被写**的情况下拷出来的。
func TestSnapshotToProducesAnOpenableIdenticalCopy(t *testing.T) {
	st := openTemp(t)
	id := seedForSummary(t, st, "s1", 50)
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := st.SnapshotTo(context.Background(), dst); err != nil {
		t.Fatalf("SnapshotTo: %v", err)
	}
	// 副本之后源库继续写：副本不能跟着变（它是那一刻的一致快照）
	if err := st.AppendBlocks(id, []model.Block{{Kind: "user", Body: "之后写的", Seq: 50, TS: 1}}, Watermark{Size: 2, MTime: 2, Offset: 2}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("副本 quick_check=%q err=%v", check, err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM blocks").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Errorf("副本应有拷贝那一刻的 50 块，实际 %d", n)
	}
}
