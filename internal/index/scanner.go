package index

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/parser"
)

// Scanner 扫描各解析器的根目录并把新增内容写进索引。
//
// 只读：会话原始文件一律以 O_RDONLY 打开，绝不写入、修改或重命名。
type Scanner struct {
	Store *Store
	Reg   *parser.Registry
	Cfg   Config

	// OnFile 在每个文件处理完后回调，供 CLI 打进度。可为 nil。
	OnFile func(path string, indexed int)
}

// Report 是一轮扫描的结果。
type Report struct {
	FilesSeen    int
	FilesIndexed int // 实际读取并写入了内容的文件数
	BlocksAdded  int
	LinesSkipped int // 坏行
	MarkedDead   int
	Rebuilt      int  // 因截断/改写而从头重建的会话数
	SizeCapped   bool // 索引库触顶，已停止新增
	// SizeUnknown 表示本轮**没能读到**库体积，因而体积上限未被校验。
	// 它与 SizeCapped 是两件事：一个是「已封顶」的结论，一个是「不知道有没有封顶」。
	// 把后者当成前者的反面（即"没超限"）正是这次要修的那类静默失效。
	SizeUnknown bool
	DBBytes     int64
}

// classifySize 判定本轮的体积检查结论。**三态，不可压成两态。**
//
// 抽成函数有两个理由：一是它在 Scanner 里没法测（Store 是具体类型，
// 注入不了一个会失败的 Stats()）；二是这条判定正是本项目栽过的那类错误——
// 原写法 `err == nil && bytes >= max` 在 Stats() 出错时整个条件为假，
// 与「没超限」完全同义，于是体积上限守卫静默关闭、索引继续无限增长。
//
// 「不知道有没有超」和「确认没超」是两个结论，不能共用一个 false。
func classifySize(bytes, max int64, err error) (capped, unknown bool) {
	if err != nil {
		return false, true
	}
	return bytes >= max, false
}

// 水位判定的五种情形，与 design.md「增量变更检测」表一一对应。
type action int

const (
	actSkip    action = iota // size == offset 且 mtime 未变
	actAppend                // size > offset：追加
	actRebuild               // size < offset 或 mtime 变了：截断/原地改写
)

func decide(wm Watermark, known bool, size, mtime int64) action {
	switch {
	case !known:
		return actAppend
	case size < wm.Offset:
		return actRebuild
	case size == wm.Offset && mtime != wm.MTime:
		return actRebuild
	case size == wm.Offset:
		return actSkip
	default:
		return actAppend
	}
}

// ScanOnce 跑一轮完整扫描。
func (s *Scanner) ScanOnce() (Report, error) {
	var rep Report
	seen := map[string]bool{}

	for _, root := range s.Reg.Roots() {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// 目录不存在（没装某个工具）不算错误
				if errors.Is(err, fs.ErrNotExist) {
					return fs.SkipDir
				}
				slog.Warn("扫描时跳过", "path", path, "err", err)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			p := s.Reg.For(path)
			if p == nil {
				return nil
			}
			rep.FilesSeen++
			seen[path] = true

			if rep.SizeCapped {
				return nil
			}
			if err := s.indexFile(p, path, &rep); err != nil {
				slog.Warn("索引文件失败，跳过", "path", path, "err", err)
			}
			if s.OnFile != nil {
				s.OnFile(path, rep.FilesIndexed)
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return rep, err
		}
	}

	// 原始文件消失的会话标记失效：检索结果不得指向已不存在的文件
	alive, err := s.Store.AlivePaths()
	if err != nil {
		return rep, err
	}
	for _, p := range alive {
		if !seen[p] {
			if err := s.Store.MarkDead(p); err != nil {
				return rep, err
			}
			rep.MarkedDead++
		}
	}

	st, err := s.Store.Stats()
	if err != nil {
		// 量不到体积不该让整轮索引失败（与「LLM 不可用不得整体不可用」同一条纪律），
		// 但必须留下痕迹——否则报告里的 DBBytes=0 会被当成真值。
		slog.Warn("读取索引库体积失败，本轮报告的体积不可信", "err", err)
		rep.SizeUnknown = true
	} else {
		rep.DBBytes = st.DBBytes
	}
	return rep, nil
}

func (s *Scanner) indexFile(p parser.Parser, path string, rep *Report) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	size, mtime := fi.Size(), fi.ModTime().Unix()

	wm, known, err := s.Store.Watermark(path)
	if err != nil {
		return err
	}
	act := decide(wm, known, size, mtime)
	if act == actSkip {
		return nil
	}

	meta, err := p.Meta(path)
	if err != nil {
		return err
	}
	id, err := s.Store.UpsertSession(meta)
	if err != nil {
		return err
	}

	start := parser.Cursor{Offset: wm.Offset, Seq: seqAfter(s.Store, id)}
	if act == actRebuild {
		if err := s.Store.ResetSession(id); err != nil {
			return err
		}
		start = parser.Cursor{}
		rep.Rebuilt++
	}

	f, err := os.Open(path) // 只读
	if err != nil {
		return err
	}
	defer f.Close()
	if start.Offset > 0 {
		if _, err := f.Seek(start.Offset, 0); err != nil {
			return err
		}
	}

	var blocks []model.Block
	cur, err := p.Parse(f, start, func(b model.Block) error {
		blocks = append(blocks, s.applyPolicy(b))
		return nil
	})
	if err != nil {
		return err
	}

	if len(blocks) == 0 && cur.Offset == wm.Offset {
		return nil
	}
	if err := s.Store.AppendBlocks(id, blocks, Watermark{Size: size, MTime: mtime, Offset: cur.Offset}); err != nil {
		return err
	}

	// 标题是 Parse 才知道的；空值不写（见 SetTitle 的注释）
	if err := s.Store.SetTitle(id, cur.Title); err != nil {
		slog.Warn("记录会话名失败", "path", path, "err", err)
	}

	rep.FilesIndexed++
	rep.BlocksAdded += len(blocks)
	rep.LinesSkipped += cur.Skipped

	// 每写完一个文件检查一次体积上限：超限停止新增，但不删任何数据
	if s.Cfg.MaxBytes > 0 {
		// 三态，不是两态。原来写的是 `err == nil && st.DBBytes >= MaxBytes`——
		// Stats() 一出错整个条件为假，与「没超限」完全同义，于是**体积上限守卫
		// 静默关闭、索引继续无限增长，日志里一个字都没有**。而这个守卫存在的
		// 全部意义就是防无限增长。
		st, err := s.Store.Stats()
		capped, unknown := classifySize(st.DBBytes, s.Cfg.MaxBytes, err)
		switch {
		case unknown:
			slog.Warn("读取索引库体积失败，本轮未能校验体积上限", "err", err)
			rep.SizeUnknown = true
		case capped:
			rep.SizeCapped = true
			slog.Warn("索引库已达体积上限，停止索引新增内容（检索照常，不会删除历史数据）",
				"db_bytes", st.DBBytes, "max_bytes", s.Cfg.MaxBytes)
		}
	}
	return nil
}

// seqAfter 取该会话已有的最大 seq+1，让续读的块序号接着排。
func seqAfter(st *Store, sessionID int64) int {
	var n *int
	if err := st.DB().QueryRow(`SELECT MAX(seq) FROM blocks WHERE session_id = ?`, sessionID).Scan(&n); err != nil || n == nil {
		return 0
	}
	return *n + 1
}

// applyPolicy 落实工具结果的截断与非文本跳过规则（需求 7.4 / 7.5 / 7.7）。
func (s *Scanner) applyPolicy(b model.Block) model.Block {
	if b.Kind != model.KindToolResult {
		return b
	}
	b.RawBytes = len(b.Body)

	if !s.Cfg.ToolResultBody {
		b.Body, b.Truncated = "", true
		return b
	}
	if isNonText(b.Body) {
		b.Body, b.Truncated = "", true
		return b
	}
	if limit := s.Cfg.ToolResultCap; limit > 0 && len(b.Body) > limit {
		b.Body, b.Truncated = truncateUTF8(b.Body, limit), true
	}
	return b
}

// isNonText 判定二进制 / base64 图片 / 其他明显非文本内容：只留元数据。
func isNonText(s string) bool {
	if strings.ContainsRune(s, 0) {
		return true
	}
	if !utf8.ValidString(s) {
		return true
	}
	if len(s) > 1024 {
		head := s
		if len(head) > 512 {
			head = head[:512]
		}
		n := 0
		for i := 0; i < len(head); i++ {
			if isBase64Byte(head[i]) {
				n++
			}
		}
		if n*100 >= len(head)*95 {
			return true
		}
	}
	return false
}

func isBase64Byte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
		c == '+' || c == '/' || c == '='
}

// truncateUTF8 按字节上限截断但不切断多字节字符。
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && s[limit]&0xC0 == 0x80 {
		limit--
	}
	return s[:limit]
}
