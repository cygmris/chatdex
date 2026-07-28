// Package parser 把各家 agent 工具的 JSONL 会话记录解析成统一的内部模型。
//
// 需求 2 明文要求解析器可插拔：将来出现第三种 agent 工具（或上游改格式），
// 只新增一个 Parser 实现并注册，索引与检索层一行不动。
//
// 所有实现都必须只读打开文件——绝不写入、修改或删除任何会话原始文件。
package parser

import (
	"io"

	"github.com/cygmris/chatdex/internal/model"
)

// Cursor 是一次解析的进度：读到哪个字节、下一个块的序号是多少。
//
// Offset 只在**完整的一行**读完后推进：会话文件正被 agent 追加写入时，
// 尾部可能是半行，把它算进水位下次就会从行中间续读。
type Cursor struct {
	Offset  int64
	Seq     int
	Skipped int // 累计跳过的坏行数，供排查
}

// Parser 是一种 agent 工具的会话格式解析器。
type Parser interface {
	// Name 是来源标识，写进 sessions.source（claude / codex）。
	Name() string

	// Roots 是该工具存放会话记录的根目录（绝对路径）。
	Roots() []string

	// Match 判断一个文件是否归本解析器处理。
	Match(path string) bool

	// Meta 从文件路径（必要时读文件头几行）解出会话元数据。
	Meta(path string) (model.SessionMeta, error)

	// Parse 从 start 位置续读并逐块回调 emit，返回新的进度。
	// 调用方负责把 r 定位到 start.Offset。
	// 单行解析失败只跳过该行并累计到 Cursor.Skipped，不得中断整个文件。
	Parse(r io.Reader, start Cursor, emit func(model.Block) error) (Cursor, error)
}

// Registry 是解析器集合。
type Registry struct {
	parsers []Parser
}

func NewRegistry(ps ...Parser) *Registry { return &Registry{parsers: ps} }

// For 返回负责该文件的解析器，没有则返回 nil。
func (r *Registry) For(path string) Parser {
	for _, p := range r.parsers {
		if p.Match(path) {
			return p
		}
	}
	return nil
}

func (r *Registry) All() []Parser { return r.parsers }

// Roots 汇总全部解析器的扫描根目录。
func (r *Registry) Roots() []string {
	var out []string
	for _, p := range r.parsers {
		out = append(out, p.Roots()...)
	}
	return out
}
