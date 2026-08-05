package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// 从备份里把已经消失的会话取回来。
//
// 这件事的价值不是「让它能显示」——索引里已经有消息体，`alive=0` 的会话
// 现在照样能读。真正的价值是**逐字节的原件**（需求 4.3）：索引对工具结果
// 是**故意有损**的（超 `tool_result_cap` 截断、非文本清空），实测 706362 个
// 块里 43637 个被截断，涉及 2567 个会话。那部分内容只在原始文件里，
// 而原始文件一旦被日常清理删掉，就只剩备份里这一份。
//
// **只读铁律对恢复同样成立**：取回只用于展示，不落盘、不写回源目录。
// 真要把文件恢复到磁盘由用户显式操作（界面给可复制的 restic restore 命令）。

// ErrNotInBackup 表示备份里也没有这个文件。
//
// 单独一个错误类型是为了让上层能把它与「restic 挂了」区分开——
// 需求 4.4 要求这种情况明确告知，而不是显示空白或假装还在。
var ErrNotInBackup = fmt.Errorf("备份里也没有这个文件")

// findMatch 是 restic find --json 的一条结果。
//
// 注意它返回的是**数组**（每个含该文件的快照一条），
// 与 ls --json 的逐行对象、snapshots --json 的数组形态都不同。
type findMatch struct {
	Matches []struct {
		Path string `json:"path"`
	} `json:"matches"`
	Snapshot string `json:"snapshot"`
}

// locate 找出**时间最新**的那个含该文件的快照。
//
// 为什么必须是最新的：一个会话在被删掉之前会被备份很多次，每次内容都不同
// （会话在增长）。取错快照 = 拿回一个更早的、不完整的版本，而且**看起来
// 完全正常**——不比对内容根本发现不了。
//
// 为什么按时间挑而不是取数组的某一端：实测 `snapshots --json` 是时间**正序**、
// `find --json` 是**倒序**，两个命令方向相反。原来按「与 snapshots 同向」
// 取末位，结果稳定地拿回最老的版本。既然两边的顺序都不可靠且没有文档保证，
// 就干脆不依赖顺序——拿 ID 去 Snapshots() 查真实时间，取最大。
func (r *Runner) locate(ctx context.Context, path string) (string, error) {
	out, err := r.run(ctx, "find", "--json", path)
	if err != nil {
		return "", err
	}
	var hits []findMatch
	if err := json.Unmarshal(out, &hits); err != nil {
		return "", fmt.Errorf("解析 restic find 输出失败：%w", err)
	}
	candidates := map[string]bool{}
	for _, h := range hits {
		for _, m := range h.Matches {
			// find 会把路径当模式匹配，可能命中同名的别的文件
			if m.Path == path {
				candidates[h.Snapshot] = true
			}
		}
	}
	if len(candidates) == 0 {
		return "", ErrNotInBackup
	}
	snaps, err := r.Snapshots(ctx)
	if err != nil {
		return "", err
	}
	var best Snapshot
	for _, s := range snaps {
		if candidates[s.ID] && s.Time.After(best.Time) {
			best = s
		}
	}
	if best.ID == "" {
		return "", ErrNotInBackup
	}
	return best.ID, nil
}

// Fetch 从备份里流式取出一个文件。
//
// 调用方负责 Close。返回的流**不落盘**：restic dump 直接写 stdout，
// 我们把管道原样交出去，中途没有临时文件（需求 4.5）。
func (r *Runner) Fetch(ctx context.Context, path string) (io.ReadCloser, error) {
	if st := r.Available(ctx); !st.Available {
		return nil, fmt.Errorf("%s", st.Reason)
	}
	snap, err := r.locate(ctx, path)
	if err != nil {
		return nil, err
	}
	c := r.cfg()
	// 仓库路径走 env()（RESTIC_REPOSITORY），与其余所有命令一致——
	// 这里不能用 run()：它把 stdout 整个读进内存，而 dump 要流式。
	cmd := exec.CommandContext(ctx, c.bin(), "dump", snap, path)
	cmd.Env = c.env()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &dumpStream{rc: stdout, cmd: cmd, errb: &errb}, nil
}

// dumpStream 把 restic dump 的退出码并进 Close。
//
// 不这样做的话，dump 中途失败（快照损坏、路径对不上）只会表现为
// **流提前结束**——上层拿到一个截断的文件却以为读完了，然后把半个会话
// 当成完整的展示出去。要让失败以错误的形式出现，就得等进程退出。
// Close 幂等：调用方通常既 defer 一次又显式收一次错误，
// 而 cmd.Wait() 不能重入（第二次固定报 "Wait was already called"）。
type dumpStream struct {
	rc     io.ReadCloser
	cmd    *exec.Cmd
	errb   *strings.Builder
	done   bool
	closed error
}

func (d *dumpStream) Read(p []byte) (int, error) { return d.rc.Read(p) }

func (d *dumpStream) Close() error {
	if d.done {
		return d.closed
	}
	d.done = true
	d.rc.Close()
	if err := d.cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(d.errb.String()); msg != "" {
			d.closed = fmt.Errorf("%s", firstLine(msg))
		} else {
			d.closed = err
		}
	}
	return d.closed
}
