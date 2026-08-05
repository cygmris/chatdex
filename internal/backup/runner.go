// Package backup 是 chatdex 与 restic 之间**唯一**的交互层。
//
// 分工：restic 管「存得住、存得安全」（去重、压缩、加密、完整性校验），
// chatdex 管 restic 做不到的那部分——知道该存什么、存了没有、怎么读回来。
//
// 为什么对接而不自研（实测支撑，勿再重新推导）：
//   - 7 天 8989 轮扫描累计触发 1811 次「重建」信号（size == offset 且 mtime 变了）。
//     索引层可以粗判（重解析一遍，浪费 CPU 但结果对），归档层不行——判错一次
//     就留一份几百 MB 的重复副本。restic 的内容寻址分块天然免疫这一整类问题。
//   - 备份是「错了就完蛋」的功能；restic 有生产验证与 `restic check`。
//   - 源数据含明文凭证（这也是索引库 0600 的原因）；restic 默认加密，
//     自研等于再造一份长期留存的未加密副本。
//
// 两条纪律，破了就会碎：
//  1. **只解析 --json**。人类可读输出跨版本必碎。
//  2. **命令行拼装只有一处**（buildArgs）。同一件事两处实现必然漂移。
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// probeTimeout 是探测类命令的超时。探测要快——它挡在界面渲染前面。
const probeTimeout = 5 * time.Second

// Source 是一个备份源。
//
// 做成列表而不是单个路径：restic 一条命令能备多个路径，
// 而「这次只备其中一个」是常见需求（用户明确要求 ~/.claude/projects、
// ~/.codex/sessions、/agentdata 这些可以任选其一或全部）。
type Source struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// Config 是 Runner 需要的配置，由上层从 config.Backup 转过来。
//
// **没有 Password 字段，只有 PasswordFile。** 密码不进 chatdex 的配置文件、
// 不进日志、不进界面——丢了备份就不可恢复，但那是用户自己的密码管理，
// 不该由我们代管。
type Config struct {
	Repo         string
	PasswordFile string
	ResticPath   string // 空 = 从 PATH 找
	Sources      []Source
}

// Runner 执行 restic 命令。
type Runner struct {
	Cfg func() Config // 热取，与 summary.Worker.Live 同一手法

	// 自动备份的结果记在内存里（不建表），由扫描循环写、API 读，所以要锁。
	mu       sync.Mutex
	lastAuto *AutoResult
}

func (r *Runner) cfg() Config {
	if r.Cfg == nil {
		return Config{}
	}
	return r.Cfg()
}

// bin 返回 restic 可执行文件路径。
//
// 可配是必须的，不是灵活性：restic 常装在 ~/.local/bin（本机实测就是），
// 而 systemd --user 起的服务其 PATH 未必包含它。写死「从 PATH 找」
// 会让「命令行里能跑、服务里跑不了」——一个查起来很费劲的现象。
func (c Config) bin() string {
	if c.ResticPath != "" {
		return c.ResticPath
	}
	return "restic"
}

// EnabledSources 返回勾选启用的源路径。
func (c Config) EnabledSources() []string {
	var out []string
	for _, s := range c.Sources {
		if s.Enabled && strings.TrimSpace(s.Path) != "" {
			out = append(out, s.Path)
		}
	}
	return out
}

// env 组装 restic 需要的环境变量。
//
// 用 RESTIC_PASSWORD_FILE 而不是 RESTIC_PASSWORD：后者会让密码出现在
// 进程环境里（/proc/<pid>/environ 同用户可读），前者只暴露路径。
func (c Config) env() []string {
	env := append(os.Environ(),
		"RESTIC_REPOSITORY="+c.Repo,
		// 关掉进度输出：我们只读 --json，人类可读的进度条只会污染 stdout
		"RESTIC_PROGRESS_FPS=0",
	)
	if c.PasswordFile != "" {
		env = append(env, "RESTIC_PASSWORD_FILE="+c.PasswordFile)
	}
	return env
}

// run 执行一条 restic 命令并返回 stdout。
//
// stderr 单独收：restic 把错误写在那里，而我们需要它来给出**可读的原因**
// （需求 6.2：仓库未初始化要给明确提示，不能抛一个 restic 原始错误）。
func (r *Runner) run(ctx context.Context, args ...string) ([]byte, error) {
	c := r.cfg()
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = c.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), fmt.Errorf("%s", firstLine(msg))
	}
	return out.Bytes(), nil
}

// firstLine 从 restic 的 stderr 里提炼出**一句人能看懂的原因**。
//
// 两层处理，缺一不可：
//  1. restic 0.19 在 --json 模式下把错误也吐成 JSON
//     （`{"message_type":"exit_error","code":10,"message":"..."}`）。
//     直接展示这坨东西，等于把「仓库还没初始化」写成一行日志噪声甩给用户。
//  2. 提炼出的 message 常带一大段「Is there a repository at ...」的追问，
//     整段塞进界面会把真正的原因淹掉，所以只取第一行。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	// stderr 可能有多行，逐行找那条 exit_error
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m struct {
			Type    string `json:"message_type"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &m) == nil && m.Message != "" {
			s = m.Message
			break
		}
	}
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(strings.TrimPrefix(first, "Fatal: "))
}

// Status 是备份功能的可用性，与 /api/chat/status 同构。
type Status struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Version   string `json:"version,omitempty"`
	Repo      string `json:"repo,omitempty"`
	// RepoReady 区分「restic 有但仓库还没初始化」——这一种是可以引导用户
	// 一键解决的，与「restic 压根没装」是两回事，不该混成同一个 reason。
	RepoReady bool `json:"repo_ready"`
}

// Available 探测备份功能是否可用。
//
// 三种不可用各有各的 reason，因为对使用者意味着不同的下一步：
// 没配仓库 → 去设置页；restic 没装 → 去装；仓库没初始化 → 点一下初始化。
// 混成一个「备份不可用」等于让人自己猜。
func (r *Runner) Available(ctx context.Context) Status {
	c := r.cfg()
	if strings.TrimSpace(c.Repo) == "" {
		return Status{Reason: "还没配置备份仓库路径"}
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	ver, err := r.run(ctx, "version", "--json")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return Status{Reason: "没找到 restic（装好后可在设置页指定它的路径）", Repo: c.Repo}
		}
		return Status{Reason: "restic 不可用：" + err.Error(), Repo: c.Repo}
	}
	var v struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(ver, &v) // 版本取不到不影响可用性判断，只是显示不出版本号

	if _, err := r.run(ctx, "snapshots", "--json", "--latest", "1"); err != nil {
		return Status{
			Reason:  "仓库还不能用：" + err.Error(),
			Version: v.Version, Repo: c.Repo,
		}
	}
	return Status{Available: true, Version: v.Version, Repo: c.Repo, RepoReady: true}
}

// Result 是一次备份的结果。
type Result struct {
	SnapshotID   string  `json:"snapshot_id"`
	FilesNew     int     `json:"files_new"`
	FilesChanged int     `json:"files_changed"`
	FilesTotal   int     `json:"files_total"`
	BytesAdded   int64   `json:"bytes_added"`
	Seconds      float64 `json:"seconds"`
	// Partial 表示有些文件读不了（restic 退出码 3），但其余都备成功了。
	// 这与「失败」是两回事——一个权限不足的文件不该让整次备份显示为失败。
	Partial bool   `json:"partial"`
	Warning string `json:"warning,omitempty"`
}

// resticPartial 是 restic 的「部分源数据读不了」退出码。
const resticPartial = 3

// Backup 跑一次备份。
//
// 只解析 --json 的 summary 行。restic 会先吐一串 status 进度行，
// 那些是给人看的，不解析。
func (r *Runner) Backup(ctx context.Context) (Result, error) {
	c := r.cfg()
	srcs := c.EnabledSources()
	if len(srcs) == 0 {
		return Result{}, fmt.Errorf("没有勾选任何备份源")
	}
	if bad := repoInsideSources(c.Repo, srcs); bad != "" {
		// 把仓库自己备进去会递归膨胀——实测误备一次就多了 5 GB。
		// 这种错误发生时用户完全看不出来（备份"成功"了，只是越来越大）。
		return Result{}, fmt.Errorf("备份源 %s 包含了仓库自身（%s），会无限膨胀", bad, c.Repo)
	}

	args := append([]string{"backup", "--json"}, srcs...)
	out, err := r.run(ctx, args...)

	res, ok := parseSummary(out)
	if !ok {
		if err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("restic 没有给出备份摘要")
	}
	if err != nil {
		// 有 summary 说明备份实际跑完了。退出码 3 = 部分文件读不了，
		// 其余都成功——降级为警告而不是把整次备份判为失败。
		if isExitCode(err, resticPartial) || strings.Contains(err.Error(), "could not be read") {
			res.Partial = true
			res.Warning = err.Error()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

// repoInsideSources 返回第一个包含了仓库路径的源；没有则返回空。
func repoInsideSources(repo string, srcs []string) string {
	if repo == "" {
		return ""
	}
	rp := filepath.Clean(repo)
	for _, s := range srcs {
		sp := filepath.Clean(s)
		if rp == sp || strings.HasPrefix(rp, sp+string(filepath.Separator)) {
			return s
		}
	}
	return ""
}

func parseSummary(out []byte) (Result, bool) {
	var res Result
	var found bool
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m struct {
			Type      string  `json:"message_type"`
			Snapshot  string  `json:"snapshot_id"`
			New       int     `json:"files_new"`
			Changed   int     `json:"files_changed"`
			Processed int     `json:"total_files_processed"`
			Added     int64   `json:"data_added"`
			Duration  float64 `json:"total_duration"`
		}
		if json.Unmarshal(line, &m) != nil || m.Type != "summary" {
			continue
		}
		res = Result{
			SnapshotID: m.Snapshot, FilesNew: m.New, FilesChanged: m.Changed,
			FilesTotal: m.Processed, BytesAdded: m.Added, Seconds: m.Duration,
		}
		found = true
	}
	return res, found
}

func isExitCode(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}

// Snapshot 是仓库里的一个快照。
type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
}

// Snapshots 列出仓库里的快照。restic 自己就是权威记录，
// chatdex **不另存一份**——存两份必然漂移。
func (r *Runner) Snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := r.run(ctx, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID    string    `json:"id"`
		Time  time.Time `json:"time"`
		Paths []string  `json:"paths"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("解析快照列表: %w", err)
	}
	snaps := make([]Snapshot, 0, len(raw))
	for _, s := range raw {
		snaps = append(snaps, Snapshot{ID: s.ID, Time: s.Time, Paths: s.Paths})
	}
	return snaps, nil
}

// Init 初始化仓库。
//
// 单独一个入口而不是让用户去命令行敲：探测到「仓库还不能用」时，
// 界面上点一下就能解决，不该把人赶去读 restic 的文档。
func (r *Runner) Init(ctx context.Context) error {
	if strings.TrimSpace(r.cfg().Repo) == "" {
		return fmt.Errorf("还没配置备份仓库路径")
	}
	_, err := r.run(ctx, "init")
	return err
}

// AutoResult 是最后一次「扫描后顺手备一次」的结果。
//
// 需求 5.3：备份失败 SHALL 明确显示原因，**SHALL NOT 只记日志**。
// 手动那条路靠 HTTP 500 把原因带回界面；自动这条路没人看着，
// 只 slog.Warn 的话，一个每半小时失败一次的备份除了 journalctl 里没人看得见。
//
// 只留在内存里，不建表——与「不另存快照历史」同一条纪律（design「明确不做」）。
type AutoResult struct {
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
	Result
}

// RecordAuto 记下自动备份的结果，供 /api/backup/status 下发。
func (r *Runner) RecordAuto(res Result, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := &AutoResult{At: time.Now(), Result: res}
	if err != nil {
		a.Error = err.Error()
	}
	r.lastAuto = a
}

// LastAuto 返回最后一次自动备份的结果；从没跑过则为 nil。
func (r *Runner) LastAuto() *AutoResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAuto
}
