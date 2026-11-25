package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/release"
)

// 日志状态(M6 第七节「提交」)。committing 之后不能取消;restarting 等新进程自检;
// healthy 是自检通过;rolled-back 是回滚完成;failed 是回滚也失败了(要人来看)。
const (
	JournalCommitting = "committing"
	JournalRestarting = "restarting"
	JournalHealthy    = "healthy"
	JournalRolledBack = "rolled-back"
	JournalFailed     = "failed"

	// JournalName 是升级日志的文件名,放在 <data-dir>/update/ 下。
	JournalName = "journal.json"
)

// replaceList 是升级要换位的东西,相对安装根。etc / data 永远不碰(配置与用户数据)。
// 每一项都是目录:整个目录换掉比逐个文件换简单,也不会留下新旧混着的半套文件。
var replaceList = []string{"bin", "panel/dist", "geodata", "openwrt", "debian"}

// JournalEntry 是一条换位记录。
type JournalEntry struct {
	// Path 是相对安装根的目标,例如 bin、panel/dist。
	Path string `json:"path"`
	// Done 表示这一条已经换位完成。回滚不看它(看磁盘上有没有 .old / .new),
	// 它是给人看日志、判断卡在哪一步用的。
	Done bool `json:"done"`
	// Fresh 表示安装根里原来没有这一项(新加的目录)。回滚时要把它整个删掉,
	// 否则会留下一个旧版本不认识的新目录。
	Fresh bool `json:"fresh,omitempty"`
}

// Journal 是升级日志(<data-dir>/update/journal.json)。
// 提交前先写它,之后每一步都落一次盘;崩溃后靠「磁盘上有没有 .old / .new」反推该回滚什么。
type Journal struct {
	Status    string         `json:"status"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Root      string         `json:"root"`
	StateDir  string         `json:"stateDir,omitempty"`
	Backup    string         `json:"backup,omitempty"`
	StartedAt string         `json:"startedAt"`
	Message   string         `json:"message,omitempty"`
	Entries   []JournalEntry `json:"entries"`
}

// Ops 是提交 / 回滚用到的文件操作。零值用真实实现;测试里替换成注入故障的版本。
type Ops struct {
	Rename    func(oldpath, newpath string) error
	RemoveAll func(path string) error
	Snapshot  func(stateDir, dst string) error
	Restore   func(src, stateDir string) error
	Write     func(path string, j *Journal) error
}

func (o Ops) withDefaults() Ops {
	if o.Rename == nil {
		o.Rename = os.Rename
	}
	if o.RemoveAll == nil {
		o.RemoveAll = os.RemoveAll
	}
	if o.Snapshot == nil {
		o.Snapshot = SnapshotState
	}
	if o.Restore == nil {
		o.Restore = RestoreState
	}
	if o.Write == nil {
		o.Write = WriteJournal
	}
	return o
}

// CommitRequest 是一次提交要的东西。
type CommitRequest struct {
	Root     string    // 安装根(/opt/open-box)
	Staging  string    // 解压出来的新版本目录
	Journal  string    // journal.json 的完整路径
	StateDir string    // 状态目录(<data-dir>/state),提交前打快照
	From, To string    // 旧版本 / 新版本
	Now      time.Time // 写进日志的时间,测试里固定
}

// Commit 把暂存目录里的新文件换到位。顺序(M6 第七节「提交」):
//
//	状态快照 → 写日志 → 新文件挪成 <目标>.new → 旧的改名 <目标>.old → <目标>.new 改成 <目标>
//
// 全程只有改名:暂存目录与安装根在同一个文件系统上,改名是原子的,不占双份空间。
// 任何一步失败都返回错误,由调用方(或下次启动)按日志回滚。
func Commit(ctx context.Context, req CommitRequest, ops Ops) (*Journal, error) {
	ops = ops.withDefaults()
	if req.Root == "" || req.Staging == "" || req.Journal == "" {
		return nil, errors.New("提交需要安装根、暂存目录与日志路径")
	}
	if err := os.MkdirAll(filepath.Dir(req.Journal), 0o755); err != nil {
		return nil, fmt.Errorf("建日志目录失败:%w", err)
	}

	j := &Journal{
		Status:    JournalCommitting,
		From:      req.From,
		To:        req.To,
		Root:      req.Root,
		StateDir:  req.StateDir,
		StartedAt: req.Now.UTC().Format(time.RFC3339),
	}
	// 状态文件先拍一份:新版本可能迁移过状态格式,回滚时旧版本读不了新的
	if req.StateDir != "" {
		j.Backup = filepath.Join(filepath.Dir(req.Journal), "state-"+fileNameSafe(req.From)+".tar.gz")
		if err := ops.Snapshot(req.StateDir, j.Backup); err != nil {
			return nil, fmt.Errorf("备份状态目录失败:%w", err)
		}
	}

	entries, err := planCommit(req.Root, req.Staging)
	if err != nil {
		return nil, err
	}
	j.Entries = entries
	// 先落盘:从这里起崩溃都能靠日志 + 磁盘上的 .old / .new 回滚
	if err := ops.Write(req.Journal, j); err != nil {
		return nil, fmt.Errorf("写升级日志失败:%w", err)
	}

	// 一、把新文件挪成 .new(旧文件一个字节都还没动)
	for i := range j.Entries {
		p := j.Entries[i].Path
		src := filepath.Join(req.Staging, filepath.FromSlash(p))
		dst := filepath.Join(req.Root, filepath.FromSlash(p)) + ".new"
		if err := ctx.Err(); err != nil {
			return j, err
		}
		if err := ops.RemoveAll(dst); err != nil {
			return j, fmt.Errorf("清理上次残留的 %s.new 失败:%w", p, err)
		}
		if err := ops.Rename(src, dst); err != nil {
			return j, fmt.Errorf("准备新文件 %s 失败:%w", p, err)
		}
	}

	// 二、逐个换位:旧的改名 .old,新的改成目标
	for i := range j.Entries {
		if err := ctx.Err(); err != nil {
			return j, err
		}
		p := j.Entries[i].Path
		target := filepath.Join(req.Root, filepath.FromSlash(p))
		if _, err := os.Lstat(target); err == nil {
			if err := ops.RemoveAll(target + ".old"); err != nil {
				return j, fmt.Errorf("清理上次残留的 %s.old 失败:%w", p, err)
			}
			if err := ops.Rename(target, target+".old"); err != nil {
				return j, fmt.Errorf("挪走旧的 %s 失败:%w", p, err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return j, fmt.Errorf("看不了 %s:%w", p, err)
		}
		if err := ops.Rename(target+".new", target); err != nil {
			return j, fmt.Errorf("换成新的 %s 失败:%w", p, err)
		}
		j.Entries[i].Done = true
		// 每换一条落一次盘:中途断电也知道换到哪了
		if err := ops.Write(req.Journal, j); err != nil {
			return j, fmt.Errorf("写升级日志失败:%w", err)
		}
	}

	j.Status = JournalRestarting
	if err := ops.Write(req.Journal, j); err != nil {
		return j, fmt.Errorf("写升级日志失败:%w", err)
	}
	return j, nil
}

// Rollback 按日志把文件换回去。幂等:重复调用不会更糟(看磁盘上有没有 .old / .new 决定做什么)。
// 最后把状态快照恢复回去,状态写 rolled-back。
func Rollback(ctx context.Context, journalPath string, j *Journal, ops Ops) error {
	ops = ops.withDefaults()
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// 逆序:后换的先还原
	for i := len(j.Entries) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			keep(err)
			break
		}
		p := j.Entries[i].Path
		target := filepath.Join(j.Root, filepath.FromSlash(p))
		old := target + ".old"
		if _, err := os.Lstat(old); err == nil {
			// 旧的有备份:把现在的东西清掉再换回去(现在可能是新的,也可能是空的)
			keep(ops.RemoveAll(target))
			keep(ops.Rename(old, target))
		} else if j.Entries[i].Fresh {
			// 原来没有这一项:升级新加的目录要删掉,否则旧版本会看到一套陌生的目录
			keep(ops.RemoveAll(target))
		}
		// 没换完的 .new 直接丢掉
		keep(ops.RemoveAll(target + ".new"))
		j.Entries[i].Done = false
	}
	if j.Backup != "" && j.StateDir != "" {
		keep(ops.Restore(j.Backup, j.StateDir))
	}
	if firstErr != nil {
		j.Status = JournalFailed
		j.Message = "回滚没做完:" + firstErr.Error()
		keep(ops.Write(journalPath, j))
		return firstErr
	}
	j.Status = JournalRolledBack
	j.Message = "已回滚到 " + j.From
	keep(ops.Write(journalPath, j))
	return nil
}

// planCommit 按固定的替换清单算出这次要换哪些目录。暂存目录里没有的项跳过
// (例如包里不带规则库时,用户已经下载的规则集不能被删掉)。
func planCommit(root, staging string) ([]JournalEntry, error) {
	var out []JournalEntry
	for _, p := range replaceList {
		src := filepath.Join(staging, filepath.FromSlash(p))
		info, err := os.Stat(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("看不了暂存目录里的 %s:%w", p, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("暂存目录里的 %s 不是目录", p)
		}
		_, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		out = append(out, JournalEntry{Path: p, Fresh: errors.Is(statErr, fs.ErrNotExist)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("暂存目录 %s 里没有可替换的内容(至少要 bin/)", staging)
	}
	if out[0].Path != "bin" {
		return nil, errors.New("暂存目录里没有 bin/,不像是安装包")
	}
	return out, nil
}

// WriteJournal 原子地写一份升级日志(先写 .tmp 再改名,断电不会留半份)。
func WriteJournal(path string, j *Journal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadJournal 读一份升级日志。文件不存在时返回 (nil, nil)。
func ReadJournal(path string) (*Journal, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var j Journal
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err := dec.Decode(&j); err != nil {
		return nil, fmt.Errorf("升级日志读不了:%w", err)
	}
	return &j, nil
}

// SnapshotState 把状态目录里的 .json 打成 tar.gz(M6 第七节:cache.db 与规则集缓存不进快照)。
func SnapshotState(stateDir, dst string) error {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 全新安装还没有状态目录:写一份空快照,回滚时什么都不用恢复
			entries = nil
		} else {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		if err := release.CheckPath(name); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(stateDir, name))
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// RestoreState 把快照恢复回状态目录(覆盖同名文件)。目录里多出来的 .json 不动:
// 那是升级之后新写的,删掉反而丢数据。
func RestoreState(src, stateDir string) error {
	f, err := os.Open(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("状态快照坏了:%w", err)
	}
	defer gz.Close()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("状态快照坏了:%w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag != tar.TypeReg || release.CheckPath(name) != nil || strings.Contains(name, "/") {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 8<<20))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stateDir, name), b, 0o600); err != nil {
			return err
		}
	}
}

// fileNameSafe 把版本号变成能当文件名的一段(版本号已经过 ValidTag,这里只是兜底)。
func fileNameSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
