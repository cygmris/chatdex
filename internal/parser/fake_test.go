package parser

import (
	"io"
	"strings"

	"github.com/cygmris/chatdex/internal/model"
)

// fakeParser 只用于验证注册表的派发逻辑。
type fakeParser struct {
	name   string
	suffix string
	roots  []string
}

func (f fakeParser) Name() string           { return f.name }
func (f fakeParser) Roots() []string        { return f.roots }
func (f fakeParser) Match(path string) bool { return strings.HasSuffix(path, f.suffix) }
func (f fakeParser) Meta(string) (model.SessionMeta, error) {
	return model.SessionMeta{Source: model.Source(f.name)}, nil
}
func (f fakeParser) Parse(io.Reader, Cursor, func(model.Block) error) (Cursor, error) {
	return Cursor{}, nil
}
