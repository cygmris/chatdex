package backup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 异地副本（S3/R2 等）的状态。
//
// 这件事此前完全在 chatdex 之外：靠一个手写脚本跑 rclone，chatdex 既不触发、
// 也不知道它跑没跑。代价不是「少个功能」——2026-08-29 实测本地 2422 个文件、
// 异地只有 1204 个，**落后 1218 个文件 / 约 1 GB**，而备份页全程显示正常
// （它报的是本地仓，而本地仓确实是好的）。这个差额没有任何自动信号。
//
// restic 不知道有第二份仓，rclone 不知道什么是快照 —— 「你落后多少」这个问题
// 只有 chatdex 答得出，那正是它该做的那部分（决策 28 划的分工）。

// MirrorStatus 是异地副本的状态。
type MirrorStatus struct {
	// Configured 表示配了异地仓。
	//
	// 🔴 **必须与 Behind==0 分开**：「没有异地副本」与「有且是最新的」在数字上
	// 都是 0，而含义相反 —— 前者是「机器没了就全没了」，后者是「你受保护」。
	// 不区分就是决策 37 那一族（不交代范围的结论）。
	Configured bool `json:"configured"`
	// Snapshots 是异地仓里的快照数。
	Snapshots int `json:"snapshots"`
	// Behind 是只在本机、异地没有的快照数。
	//
	// 只比快照 id 集合，不逐文件比对 —— 后者是 rclone check 的活
	// （4.3 GB / 2883 个对象），放在状态接口里会让备份页等好几秒。
	// 逐文件校验只在同步之后做一次。
	Behind int `json:"behind"`
	// LastSync 是**异地仓里最新快照的时间**（unix 秒），0 = 异地仓还是空的。
	//
	// ⚠️ 它不是「上次同步完成时间」——名字和这条注释一度都这么写，而代码取的是
	// remote 快照列表的最后一个。差别在失败路径上会露出来：一次校验没过的同步
	// 若已经把新快照传上去了，这个数照样前进，界面看起来像「刚同步过」。
	LastSync int64 `json:"last_sync,omitempty"`
	// Err 是取状态时的错误。
	//
	// 连不上异地仓时**必须走这里**，不得静默显示成「落后 0」——
	// 那会把「查不到」说成「查过了，是齐的」。
	Err string `json:"err,omitempty"`
}

// MirrorConfig 是异地副本的配置，由上层从 config 转过来。
type MirrorConfig struct {
	Repo       string
	EnvFile    string
	RclonePath string
}

// Mirror 负责异地副本的状态与同步。
type Mirror struct {
	// Local 是本地仓的 Runner，用来取本地快照。
	Local *Runner
	// Cfg 返回当前配置（与 Runner.Cfg 同一模式：配置可热改，不能缓存）。
	Cfg func() MirrorConfig
}

// Status 报告异地副本落后多少。
func (m *Mirror) Status(ctx context.Context) MirrorStatus {
	cfg := m.Cfg()
	if cfg.Repo == "" {
		// 没配。**不是**「落后 0」。
		return MirrorStatus{}
	}
	st := MirrorStatus{Configured: true}

	local, err := m.Local.Snapshots(ctx)
	if err != nil {
		st.Err = "读本地快照失败：" + err.Error()
		return st
	}
	remote, err := m.remoteRunner(cfg).Snapshots(ctx)
	if err != nil {
		st.Err = "读异地快照失败：" + err.Error()
		return st
	}

	have := make(map[string]bool, len(remote))
	for _, s := range remote {
		have[s.ID] = true
	}
	for _, s := range local {
		if !have[s.ID] {
			st.Behind++
		}
	}
	st.Snapshots = len(remote)
	if n := len(remote); n > 0 {
		st.LastSync = remote[n-1].Time.Unix()
	}
	return st
}

// remoteRunner 造一个指向异地仓的 Runner。
//
// 复用 Runner 而不是另写一套：restic 原生支持 S3 后端，「远端仓」与「本地仓」
// 对它是同一件事，差别只在 RESTIC_REPOSITORY 的值。另写一套必然漂移。
func (m *Mirror) remoteRunner(cfg MirrorConfig) *Runner {
	base := m.Local.Cfg()
	return &Runner{
		Cfg: func() Config {
			c := base
			c.Repo = cfg.Repo
			c.Env = loadEnvFile(cfg.EnvFile)
			return c
		},
	}
}

// loadEnvFile 读 KEY=VALUE 文件，供 S3 凭据用。
//
// 🔴 凭据只走这里，**不进 config.json**：那个文件会被 /api/config 读出来给界面，
// 也会进备份。读不到就返回 nil —— 让 restic 自己去报「没有凭据」，
// 比我们伪造一个空值让它得到一个看起来像权限不足的 403 强
// （cloudflare-ops 的坑 3 记过：空值让人以为是权限问题而不是没配）。
func loadEnvFile(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path) // 只读
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if k, _, ok := strings.Cut(line, "="); ok && k != "" {
			out = append(out, line)
		}
	}
	return out
}

// ---------------- 同步 ----------------

// mirrorOrder 是同步的目录顺序。**snapshots 必须最后。**
//
// 保证异地在任何一刻能看到的快照，它引用的 pack 都已经在了。
// 反过来先传 snapshots，中途断网就留下一个「指向不存在数据」的仓——
// 那种仓 restic snapshots 列得出来，restore 却会失败，比没有更糟。
//
// locks/ 不在列表里：本机的锁对异地没有意义，传过去反而会挡住
// 将来直接对异地用 restic。
var mirrorOrder = []string{"keys", "data", "index", "snapshots"}

// SyncResult 是一次同步的结果。
type SyncResult struct {
	Checked  int     `json:"checked"` // rclone check 比对的文件数
	Seconds  float64 `json:"seconds"`
	Verified bool    `json:"verified"` // 逐文件校验通过
}

// Sync 把本地仓镜像到异地。
//
// 按决策 28「对接工具、不自研」：shell 出去调 rclone，不自己写 S3 客户端。
//
// 🔴 **完成的判据是 rclone check 通过，不是 copy 的退出码。**
// 「copy 退 0」只说明命令没报错，不说明文件都对——这条是 sw 知识库里
// 那篇异地备份文档的第四节，本期把它写进代码。
func (m *Mirror) Sync(ctx context.Context) (SyncResult, error) {
	cfg := m.Cfg()
	if cfg.Repo == "" {
		return SyncResult{}, errors.New("没有配置异地副本")
	}
	bin := cfg.RclonePath
	if bin == "" {
		bin = "rclone"
	}
	if _, err := exec.LookPath(bin); err != nil {
		// rclone 缺失是降级不是错误：报告层照常工作（与 restic 可选同一模式）
		return SyncResult{}, fmt.Errorf("找不到 rclone：%w", err)
	}
	dst, env, err := rcloneTarget(cfg)
	if err != nil {
		return SyncResult{}, err
	}
	src := m.Local.Cfg().Repo
	if strings.Contains(src, ":") {
		return SyncResult{}, errors.New("本地仓不是目录，无法镜像")
	}

	start := time.Now()
	opts := []string{"--s3-no-check-bucket", "--transfers", "4", "--checkers", "16",
		"--s3-chunk-size", "32M", "--retries", "5", "--low-level-retries", "20"}

	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, bin, append(args, opts...)...)
		cmd.Env = append(os.Environ(), env...)
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s：%s", strings.Join(args[:2], " "), strings.TrimSpace(errb.String()))
		}
		return nil
	}

	// 跑两遍：源仓在同步期间可能被写入（自动备份每 30 分钟一轮）。
	// 第二遍很快，且把第一遍期间新增的补齐。
	for range 2 {
		// config 是文件不是目录 —— 用 copy 会传成 config/config，
		// 而 restic 打开时只会说「不是一个仓库」，不会提示路径多了一层。
		if err := run("copyto", src+"/config", dst+"/config"); err != nil {
			return SyncResult{}, err
		}
		for _, d := range mirrorOrder {
			if err := run("copy", src+"/"+d, dst+"/"+d); err != nil {
				return SyncResult{}, err
			}
		}
	}

	// 逐文件校验。**copy 退 0 不等于文件都对。**
	cmd := exec.CommandContext(ctx, bin,
		append([]string{"check", "--exclude", "/locks/**", src, dst}, opts...)...)
	cmd.Env = append(os.Environ(), env...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	checkErr := cmd.Run()
	out := errb.String()
	res := SyncResult{Seconds: time.Since(start).Seconds()}
	if mm := reCheckMatched.FindStringSubmatch(out); mm != nil {
		res.Checked, _ = strconv.Atoi(mm[1])
	}
	if checkErr != nil {
		return res, fmt.Errorf("校验未通过：%s", strings.TrimSpace(out))
	}
	res.Verified = true
	return res, nil
}

var reCheckMatched = regexp.MustCompile(`(\d+) matching files`)

// rcloneTarget 把 restic 的 S3 仓地址转成 rclone 的目标与凭据环境变量。
//
//	s3:https://<host>/<bucket>/<prefix>  →  mirror:<bucket>/<prefix>
//
// 凭据走 RCLONE_CONFIG_MIRROR_* 环境变量而**不是命令行**：
// 命令行参数在 /proc/<pid>/cmdline 里，同机器上任何进程都看得到。
// 与「密码用文件不用环境变量」是同一条纪律的另一面——
// 这里环境变量是更安全的那一侧，因为替代品是命令行。
func rcloneTarget(cfg MirrorConfig) (string, []string, error) {
	rest, ok := strings.CutPrefix(cfg.Repo, "s3:")
	if !ok {
		return "", nil, errors.New("异地仓不是 s3: 开头，暂时只支持 S3 兼容后端")
	}
	u, err := url.Parse(rest)
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("异地仓地址解析不了：%s", cfg.Repo)
	}
	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return "", nil, errors.New("异地仓地址里没有桶名")
	}
	env := []string{
		"RCLONE_CONFIG_MIRROR_TYPE=s3",
		"RCLONE_CONFIG_MIRROR_PROVIDER=Cloudflare",
		"RCLONE_CONFIG_MIRROR_ENDPOINT=" + u.Scheme + "://" + u.Host,
	}
	for _, kv := range loadEnvFile(cfg.EnvFile) {
		switch {
		case strings.HasPrefix(kv, "AWS_ACCESS_KEY_ID="):
			env = append(env, "RCLONE_CONFIG_MIRROR_ACCESS_KEY_ID="+strings.TrimPrefix(kv, "AWS_ACCESS_KEY_ID="))
		case strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY="):
			env = append(env, "RCLONE_CONFIG_MIRROR_SECRET_ACCESS_KEY="+strings.TrimPrefix(kv, "AWS_SECRET_ACCESS_KEY="))
		}
	}
	return "mirror:" + path, env, nil
}
