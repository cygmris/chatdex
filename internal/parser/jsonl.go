package parser

import (
	"bufio"
	"io"
	"strings"
	"time"
)

// scanLines 逐行回调，只在读到**完整的一行**后才推进 offset。
//
// 会话文件常常正被 agent 追加写入，尾部可能是半行；把半行算进水位，
// 下一轮就会从行中间续读，之后整个文件的解析都是错的。
func scanLines(r io.Reader, offset int64, fn func(line []byte) error) (int64, error) {
	return scanLinesAt(r, offset, func(line []byte, _ int64) error { return fn(line) })
}

// scanLinesAt 与 scanLines 相同，另把每行**起始**的偏移量交给回调。
//
// 为什么需要它：Grok 的 updates.jsonl 是流式日志，一条消息拆成连续多行，
// 解析器要能把水位停在「当前这条消息是从哪一行开始的」——否则扫描落在
// 一轮对话中间时，那条消息会被切成两块，而且下一轮从新水位开始、接不回去，
// **永久切开**。其余两个来源一行一条消息，用不上这个偏移量。
func scanLinesAt(r io.Reader, offset int64, fn func(line []byte, at int64) error) (int64, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF {
			// 末尾的半行不计入水位，留给下一轮
			return offset, nil
		}
		if err != nil {
			return offset, err
		}
		at := offset
		offset += int64(len(line))
		if s := strings.TrimSpace(line); s != "" {
			if err := fn([]byte(s), at); err != nil {
				return offset, err
			}
		}
	}
}

// parseTime 把 ISO8601 时间戳转成 unix 秒；解析不了就返回 0。
func parseTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}
