package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 看门狗与自检(M6 第七节「重启与看门狗」)。
//
// 后端不能指望自己重启自己之后还能判断新版本好不好:新二进制可能根本起不来。
// 所以提交完成后由**旧进程**先拉起一个脱离自己的 sh 脚本(不依赖新文件、不依赖 Go 运行时),
// 再请服务管理器重启后端:
//   - 新进程起来后自检通过,把日志写成 healthy,脚本看到就退出;
//   - 等到超时还不是 healthy,脚本按日志把文件换回去、恢复状态快照、重启服务,
//     并留一个 rollback.flag,让重启后的进程知道「这是回滚,不是新版本」。
const (
	// RollbackFlag 是看门狗回滚后留的标记文件,下次启动的进程据此把日志改成 rolled-back。
	RollbackFlag = "rollback.flag"
	// watchdogWait 是看门狗最多等多久(秒)。procd 的 respawn 与 systemd 的 StartLimit
	// 都不会无限重试,等太久没意义;具体值以真机为准。
	watchdogWait = 90
)

// WatchdogRequest 是拉起看门狗要的东西。
type WatchdogRequest struct {
	Dir      string   // 脚本放哪(<data-dir>/update)
	Journal  string   // journal.json 的完整路径
	Root     string   // 安装根
	StateDir string   // 状态目录
	Backup   string   // 状态快照
	Entries  []string // 换位清单(按提交顺序,脚本里会反过来回滚)
	Restart  string   // 重启服务的命令,例如 "/etc/init.d/openbox-backend restart"
	WaitSec  int      // 等多久,0 = 默认 90
}

// shQuote 把一段文字包成单引号字符串(sh 里单引号里除了单引号都是字面量)。
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// WatchdogScript 生成看门狗脚本。它只用 sh + busybox 都有的东西(sleep / grep / rm / mv / tar)。
func WatchdogScript(r WatchdogRequest) string {
	wait := r.WaitSec
	if wait <= 0 {
		wait = watchdogWait
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# 由 openbox-backend 在提交完成后生成并脱离父进程运行,别手改。\n")
	b.WriteString("# 新版本在 " + strconv.Itoa(wait) + " 秒内没把日志写成 healthy 就回滚到旧版本。\n")
	b.WriteString("set -u\n")
	fmt.Fprintf(&b, "J=%s\nR=%s\nS=%s\nB=%s\n", shQuote(r.Journal), shQuote(r.Root), shQuote(r.StateDir), shQuote(r.Backup))
	b.WriteString("WAIT=" + strconv.Itoa(wait) + "\n")
	b.WriteString("i=0\n")
	b.WriteString("while [ \"$i\" -lt \"$WAIT\" ]; do\n")
	b.WriteString("  if grep -q '\"status\": \"healthy\"' \"$J\" 2>/dev/null; then exit 0; fi\n")
	b.WriteString("  sleep 2\n")
	b.WriteString("  i=$((i+2))\n")
	b.WriteString("done\n")
	b.WriteString("# 超时:按提交顺序反过来回滚(磁盘上有没有 .old / .new 决定做什么,和 Go 那边同一套判据)\n")
	// 逆序写进脚本:提交时先换的目录可能依赖后换的,反过来最稳
	for i := len(r.Entries) - 1; i >= 0; i-- {
		p := r.Entries[i]
		fmt.Fprintf(&b, "p=%s\n", shQuote(p))
		b.WriteString("if [ -e \"$R/$p.old\" ]; then rm -rf \"$R/$p\"; mv \"$R/$p.old\" \"$R/$p\"; fi\n")
		b.WriteString("rm -rf \"$R/$p.new\"\n")
	}
	b.WriteString("if [ -n \"$B\" ] && [ -f \"$B\" ] && [ -d \"$S\" ]; then tar -xzf \"$B\" -C \"$S\"; fi\n")
	fmt.Fprintf(&b, "echo '看门狗:新版本没在 %s 秒内自检通过,已回滚到旧版本' > \"$R/data/update/%s\" 2>/dev/null\n", strconv.Itoa(wait), RollbackFlag)
	if r.Restart != "" {
		b.WriteString("# 重启服务:让旧版本重新跑起来\n")
		b.WriteString(r.Restart + "\n")
	}
	return b.String()
}

// StartWatchdog 把脚本写到磁盘并脱离当前进程拉起它。返回脚本路径。
func StartWatchdog(r WatchdogRequest) (string, error) {
	if r.Dir == "" || r.Journal == "" || r.Root == "" {
		return "", errors.New("看门狗需要目录、日志与安装根")
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Join(r.Dir, "watchdog.sh")
	if err := os.WriteFile(name, []byte(WatchdogScript(r)), 0o755); err != nil {
		return "", err
	}
	if err := startDetached(name); err != nil {
		return name, fmt.Errorf("拉起看门狗失败:%w", err)
	}
	return name, nil
}
