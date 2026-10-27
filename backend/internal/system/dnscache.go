package system

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// 清系统自带的 DNS 缓存层。两件事都只是让缓存失效,不改任何配置、不重启服务,所以不需要回滚:
//   - dnsmasq:收到 SIGHUP 会清缓存并重读 /etc/hosts 等(dnsmasq 手册的说明)。后端自己在 /proc 里按进程名找 dnsmasq,
//     不依赖 killall / pidof 是否存在;
//   - systemd-resolved:resolvectl flush-caches。

// FindProcs 在 procDir(通常是 /proc)里找进程名(comm)恰好等于 comm 的进程号,跳过自己。procDir 不存在返回空。
func FindProcs(procDir, comm string) []int {
	ents, err := os.ReadDir(procDir)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		b, err := os.ReadFile(procDir + "/" + e.Name() + "/comm")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == comm {
			out = append(out, pid)
		}
	}
	return out
}

// SignalHUP 给进程发 SIGHUP。Windows 上没有这个信号,返回 error(开发机上就是「没清掉」)。
func SignalHUP(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGHUP)
}

// FlushDnsmasq 给每个 dnsmasq 进程发一次 SIGHUP;至少有一个发送成功就返回 true。
func FlushDnsmasq(procDir string, signal func(pid int) error) bool {
	ok := false
	for _, pid := range FindProcs(procDir, "dnsmasq") {
		if signal(pid) == nil {
			ok = true
		}
	}
	return ok
}

// FlushResolved 执行 resolvectl flush-caches;命令不存在、systemd-resolved 没在跑(退出码非 0)都返回 false。
func FlushResolved(ctx context.Context, run Runner) bool {
	res, err := run(ctx, "resolvectl", "flush-caches")
	return err == nil && res.Code == 0
}
