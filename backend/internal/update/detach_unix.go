//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// startDetached 把脚本脱离当前进程拉起:新会话(setsid)、不占着父进程的 stdout/stderr。
// 后端重启后它还要继续跑,所以不能挂在当前进程的进程组上。
func startDetached(script string) error {
	cmd := exec.Command("sh", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// 输出没人看:看门狗只通过日志文件与 rollback.flag 表达结果
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不等它:它是脱离的,重启后继续跑
	return cmd.Process.Release()
}
