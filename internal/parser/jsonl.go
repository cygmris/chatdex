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
		offset += int64(len(line))
		if s := strings.TrimSpace(line); s != "" {
			if err := fn([]byte(s)); err != nil {
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
