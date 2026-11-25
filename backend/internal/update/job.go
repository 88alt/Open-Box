package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/release"
)

// 阶段名。前端按这些字符串显示进度(M6 第七节);committing / restarting_core 要等 M6-6。
const (
	StageStarting       = "starting"
	StageProbing        = "probing"
	StageDownloading    = "downloading"
	StageVerifying      = "verifying"
	StageExtracting     = "extracting"
	StageCommitting     = "committing"
	StageRestartingCore = "restarting_core"
	StageDone           = "done"
	StageFailed         = "failed"
	StageCancelled      = "cancelled"
)

// ErrBusy 表示已经有一次升级在进行(接口回 409)。
var ErrBusy = errors.New("已经有一次升级在进行")

// errCommitNotReady 是 M6-5 的终点:下载、校验、冒烟都过了,但「提交」还没实现。
// 说清楚「本次没有改动系统」,免得用户以为装了一半。
var errCommitNotReady = errors.New("下载、校验、冒烟检查都通过了,但提交(替换文件并重启)还没有实现(M6-6);本次没有改动系统")

// logLines 是日志尾部保留的行数。
const logLines = 200

// job 是一次升级的进度与日志。所有字段由 mu 保护:Status / Cancel 与工作 goroutine 并发访问。
type job struct {
	mu      sync.Mutex
	stage   string
	message string
	bytes   int64
	total   int64
	started bool // 已经开始下载,bytes / total 才有意义
	running bool
	log     []string
	cancel  context.CancelFunc
}

func newJob(cancel context.CancelFunc) *job {
	return &job{stage: StageStarting, running: true, cancel: cancel}
}

// setStage 换阶段并记一行日志。
func (j *job) setStage(stage, message string) {
	j.mu.Lock()
	j.stage, j.message = stage, message
	j.mu.Unlock()
	if message != "" {
		j.logf("%s:%s", stage, message)
	}
}

// setBytes 更新下载进度。
func (j *job) setBytes(done, total int64) {
	j.mu.Lock()
	j.bytes, j.total, j.started = done, total, true
	j.mu.Unlock()
}

// logf 往日志尾部追加一行(带时间,最多留 logLines 行)。
func (j *job) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	j.mu.Lock()
	j.log = append(j.log, line)
	if len(j.log) > logLines {
		j.log = append([]string(nil), j.log[len(j.log)-logLines:]...)
	}
	j.mu.Unlock()
}

// finish 收尾:写最终阶段与说明,标成不在跑。
func (j *job) finish(stage, message string) {
	j.mu.Lock()
	j.stage, j.message, j.running = stage, message, false
	j.mu.Unlock()
	if message != "" {
		j.logf("%s:%s", stage, message)
	}
}

func (j *job) isRunning() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

// snapshot 取一份进度与日志尾部。
func (j *job) snapshot() (Progress, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := Progress{Stage: j.stage, Message: j.message, Running: j.running}
	if j.started {
		done, total := j.bytes, j.total
		p.Bytes, p.Total = &done, &total
	}
	return p, strings.Join(j.log, "\n")
}

// Run 开始一次升级。channel 取 auto / direct / mirror。立即返回,进度靠 Status 轮询(M6 第七节)。
func (s *Service) Run(channel string) error {
	if s.Checker == nil {
		return errors.New("没有配置发布来源,不能升级")
	}
	if _, err := s.Checker.routesFor(channel); err != nil {
		return err
	}
	s.mu.Lock()
	if s.j != nil && s.j.isRunning() {
		s.mu.Unlock()
		return ErrBusy
	}
	// 升级要跑几分钟,不能跟着这次请求的 ctx 一起结束
	ctx, cancel := context.WithCancel(context.Background())
	j := newJob(cancel)
	s.j = j
	s.mu.Unlock()
	go s.work(ctx, j, channel)
	return nil
}

// Cancel 请求取消。只改进度与状态,真正停下由工作 goroutine 看到 ctx 结束后来做。
// 返回 "cancelled" 表示已经发出取消,"not-running" 表示没有在跑的升级。
func (s *Service) Cancel() string {
	s.mu.Lock()
	j := s.j
	s.mu.Unlock()
	if j == nil || !j.isRunning() {
		return "not-running"
	}
	j.cancel()
	return "cancelled"
}

// work 是升级的工作 goroutine。
func (s *Service) work(ctx context.Context, j *job, channel string) {
	err := s.pipeline(ctx, j, channel)
	switch {
	case err == nil:
		// pipeline 自己已经把阶段写成 done
	case errors.Is(err, context.Canceled), ctx.Err() != nil:
		// 取消时具体报什么错取决于当时卡在哪一步(读响应、解压、冒烟),统一按取消收场
		j.finish(StageCancelled, "已取消")
	default:
		j.finish(StageFailed, err.Error())
	}
}

// pipeline 走完 M6-5 的四个阶段:探测 → 下载(边解压)→ 校验 → 冒烟。系统一个字节没动。
func (s *Service) pipeline(ctx context.Context, j *job, channel string) error {
	if s.DataDir == "" {
		return errors.New("没有数据目录,不知道该往哪解压")
	}
	base := filepath.Join(s.DataDir, "update")

	j.setStage(StageStarting, "准备升级")
	// 上一次升级没收尾(日志停在 committing / restarting)时不许再开:
	// 那说明有文件换了一半或者新版本还没自检,再叠一次只会更难收拾
	if prev, err := ReadJournal(filepath.Join(base, JournalName)); err != nil {
		return err
	} else if prev != nil && (prev.Status == JournalCommitting || prev.Status == JournalRestarting) {
		return fmt.Errorf("上一次升级还没收尾(状态 %s),先等它自检完或人工处理", prev.Status)
	}
	// 上一次失败或取消可能留下暂存目录,先清干净,免得把旧文件当成新包
	if err := cleanUpdateDir(base); err != nil {
		return fmt.Errorf("清理旧的暂存目录失败:%w", err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return fmt.Errorf("建暂存目录失败:%w", err)
	}

	j.setStage(StageProbing, "正在探测最新版")
	plan, err := s.Checker.Plan(ctx, channel, "")
	if err != nil {
		return fmt.Errorf("探测最新版失败:%w", err)
	}
	j.logf("目标版本 %s(架构 %s,通道 %s)", plan.Tag, plan.Arch, viaName(plan.Via))
	// 版本号要拿来当暂存目录名,先卡写法:v0.1.484 或 v0.1.484-main.2,别的形态(带斜杠之类)直接拒绝
	if !ValidTag(plan.Tag) {
		return fmt.Errorf("发布版本号写法不对:%q", plan.Tag)
	}
	if cur, ok := ParseVersion(s.Version); ok {
		if lv, ok := ParseVersion(plan.Tag); ok && lv.Compare(cur) <= 0 {
			j.finish(StageDone, "已经是最新版("+s.Version+")")
			return nil
		}
	}

	// 校验和清单先取:没有它就不知道该核对什么,也不用白下 100 MB
	sums, err := fetchSums(ctx, s.Client, plan.Sums, plan.Via)
	if err != nil {
		return err
	}
	table, err := release.ParseSums(bytes.NewReader(sums))
	if err != nil {
		return fmt.Errorf("SHA256SUMS 有问题:%w", err)
	}
	want, ok := table[plan.Archive.Name]
	if !ok {
		return fmt.Errorf("SHA256SUMS 里没有 %s", plan.Archive.Name)
	}

	j.setStage(StageDownloading, "正在下载 "+plan.Archive.Name)
	dir := filepath.Join(base, plan.Tag)
	var ex *Extracted
	err = open(ctx, s.Client, assetURL(plan.Archive, plan.Via), func(r io.Reader, total int64) error {
		e, err := Extract(ctx, r, total, dir, ExtractHooks{
			OnProgress: j.setBytes,
			OnManifest: func(m release.Manifest) error {
				// manifest 排在包的最前面,所以空间与身份检查在写文件之前就能做完
				if m.Version != plan.Tag {
					return fmt.Errorf("包里的版本是 %s,要装的是 %s", m.Version, plan.Tag)
				}
				if m.Arch != plan.Arch {
					return fmt.Errorf("包里的架构是 %s,本机要 %s", m.Arch, plan.Arch)
				}
				return checkSpace(s.FreeSpace, dir, m.TotalSize())
			},
			FreeSpace: s.FreeSpace,
		})
		if err != nil {
			return err
		}
		ex = e
		return nil
	})
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	j.logf("下载完成:%d 字节,压缩包 sha256 %s", ex.Size, short(ex.Sum))

	j.setStage(StageVerifying, "正在校验下载内容")
	if !strings.EqualFold(ex.Sum, want) {
		os.RemoveAll(dir)
		return fmt.Errorf("安装包的校验和对不上(下载到 %s,清单写 %s),已丢弃", short(ex.Sum), short(want))
	}
	if err := ex.Manifest.Verify(ex.Root); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("包里的文件和清单对不上:%w", err)
	}
	j.logf("校验通过:%d 个文件", len(ex.Manifest.Files))

	// 阶段名沿用 M6 第七节的 extracting:解压在下载阶段就顺手做完了,这一步是冒烟
	j.setStage(StageExtracting, "正在冒烟检查新二进制")
	smoke := s.Smoke
	if smoke == nil {
		smoke = func(ctx context.Context, root string) error {
			return SmokeTest(ctx, root, s.ConfigFile)
		}
	}
	if err := smoke(ctx, ex.Root); err != nil {
		os.RemoveAll(dir)
		return err
	}
	j.logf("冒烟检查通过")

	// 提交(M6-6)。没接安装根 / 没接服务管理器时**不做**:换了文件却不重启,
	// 会留下「文件是新的、进程是旧的」的半截状态,比不升级更糟。
	if s.InstallRoot == "" || s.Restart == nil {
		os.RemoveAll(dir)
		return errCommitNotReady
	}

	j.setStage(StageCommitting, "正在替换文件")
	journalPath := filepath.Join(base, JournalName)
	req := CommitRequest{
		Root: s.InstallRoot, Staging: ex.Root, Journal: journalPath,
		StateDir: s.StateDir, From: s.Version, To: plan.Tag, Now: time.Now(),
	}
	// 从这里起不能取消(文件已经在换),ctx 也就不再传给提交
	commitCtx := context.Background()
	cj, err := Commit(commitCtx, req, Ops{})
	if err != nil {
		if cj != nil {
			if rbErr := Rollback(commitCtx, journalPath, cj, Ops{}); rbErr != nil {
				return fmt.Errorf("提交失败(%v),回滚也失败(%v):需要人工处理", err, rbErr)
			}
			return fmt.Errorf("提交失败,已回滚:%w", err)
		}
		os.RemoveAll(dir)
		return fmt.Errorf("提交失败:%w", err)
	}

	// 先脱离一个看门狗,再请服务管理器重启:新版本起不来时由它把文件换回去
	entries := make([]string, 0, len(cj.Entries))
	for _, e := range cj.Entries {
		entries = append(entries, e.Path)
	}
	startWD := s.StartWatchdog
	if startWD == nil {
		startWD = StartWatchdog
	}
	if _, err := startWD(WatchdogRequest{
		Dir: base, Journal: journalPath, Root: s.InstallRoot, StateDir: s.StateDir,
		Backup: cj.Backup, Entries: entries, Restart: s.RestartCommand,
	}); err != nil {
		if rbErr := Rollback(commitCtx, journalPath, cj, Ops{}); rbErr != nil {
			return fmt.Errorf("拉不起看门狗(%v),回滚也失败(%v):需要人工处理", err, rbErr)
		}
		return fmt.Errorf("拉不起看门狗,已回滚:%w", err)
	}

	j.setStage(StageRestartingCore, "正在重启服务")
	if err := s.Restart(commitCtx); err != nil {
		// 服务没重启:文件已经是新的,进程还是旧的;看门狗会等到超时后回滚
		return fmt.Errorf("请服务管理器重启失败:%w", err)
	}
	// 走到这里说明重启是异步的,本进程随时会退出;新进程自检后把日志写成 healthy
	j.finish(StageDone, "已升级到 "+plan.Tag+",新进程会做自检")
	return nil
}

// viaName 把通道标记变成人能读的写法。
func viaName(via string) string {
	if via == "" || via == "api" {
		return "直连"
	}
	return "镜像 " + via
}

// short 取 sha256 的前 12 位,给日志与报错用。
func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// cleanUpdateDir 清掉 <data-dir>/update 下的暂存目录,但**保留**升级日志、
// 回滚标记与看门狗脚本:那三样是恢复用的,删了就没法判断上一次升级到哪了。
func cleanUpdateDir(base string) error {
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		switch e.Name() {
		case JournalName, JournalName + ".tmp", RollbackFlag, "watchdog.sh":
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
