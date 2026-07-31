package index

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

// 回填会话名。
//
// 为什么需要单独一条路径：scanner 的 decide() 里，未变化的文件走 actSkip，
// **根本不会被重新解析**。所以光改解析器，历史会话永远拿不到标题。
//
// 为什么不改 decide() 让它重新解析：那是增量索引最核心、最容易出错的逻辑
// （R1 为它写了五种情形的判定表和回归测试）。为一个 2% 覆盖率的字段去动它，
// 风险与收益完全不成比例。
//
// 这条路径只做三件事：只读打开文件、找两个特征串、UPDATE sessions.title。
// 碰不到 blocks、碰不到水位、碰不到 FTS。
//
// 实测：扫 3067 个文件（3.1 GB）17 秒，命中 82 个。对比全量重建 13 分钟。

// 特征串。先做字节包含判断再 json 解析——绝大多数行两个都不含，
// 省掉一次 json.Unmarshal 是 17 秒和几分钟的差别。
var (
	markCustomTitle = []byte(`"custom-title"`)
	markAgentName   = []byte(`"agent-name"`)
)

type titleRec struct {
	Type        string `json:"type"`
	CustomTitle string `json:"customTitle"`
	AgentName   string `json:"agentName"`
}

// titleFromFile 只读扫一遍文件取会话名。取最后一次出现的值，
// custom-title 优先——与解析器里的规则一致。
func titleFromFile(path string) (string, error) {
	f, err := os.Open(path) // 只读，维持 R1 的只读铁律
	if err != nil {
		return "", err
	}
	defer f.Close()

	var custom, agent string
	sc := bufio.NewScanner(f)
	// 单行可能很长（一条 Write 的 content 就能几百 KB），给足缓冲
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, markCustomTitle) && !bytes.Contains(line, markAgentName) {
			continue
		}
		var r titleRec
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Type {
		case "custom-title":
			if r.CustomTitle != "" {
				custom = r.CustomTitle
			}
		case "agent-name":
			if r.AgentName != "" {
				agent = r.AgentName
			}
		}
	}
	// 扫描出错（超长行等）不算失败：已经读到的部分仍然有效
	if err := sc.Err(); err != nil {
		slog.Warn("回填：文件扫描中断，用已读到的结果", "path", path, "err", err)
	}
	if custom != "" {
		return custom, nil
	}
	return agent, nil
}

// BackfillTitles 给存量会话补上标题，只跑一次。
//
// 幂等：重复调用结果一致；跑过之后靠 meta 里的水位直接跳过，
// 不重复付出扫描代价（需求 2.3）。
func (s *Store) BackfillTitles() (int, error) {
	done, err := s.metaGet("titles_backfilled_at")
	if err != nil {
		return 0, err
	}
	if done != "" {
		return 0, nil // 跑过了
	}

	rows, err := s.db.Query(
		`SELECT id, file_path FROM sessions WHERE title = '' AND alive = 1`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id   int64
		path string
	}
	var todo []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.path); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	t0 := time.Now()
	var n int
	for _, it := range todo {
		title, err := titleFromFile(it.path)
		if err != nil {
			// 源文件已删除等：跳过继续，不中断整轮（需求 2.5）
			continue
		}
		if title == "" {
			continue
		}
		if err := s.SetTitle(it.id, title); err != nil {
			slog.Warn("回填：写标题失败", "id", it.id, "err", err)
			continue
		}
		n++
	}

	if err := s.metaSet("titles_backfilled_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		// 水位没写上不影响本轮结果（标题已经写进去了），
		// 只是下次会再扫一遍——而重扫是幂等的
		slog.Warn("回填：记录水位失败，下次会重扫", "err", err)
	}
	slog.Info("会话名回填完成", "扫描", len(todo), "命中", n, "耗时", time.Since(t0).Round(time.Millisecond))
	return n, nil
}
