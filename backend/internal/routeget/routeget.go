// Package routeget 问系统「从这个地址来、进这个接口、去那个目的地的包,路由会从哪转出」(只读,`ip route get`)。
// 给 terminal-test 的入口取证用:连接没被改写、内核连接表里也没有时,看系统是直接从 WAN 设备转出去(旁路),
// 还是会进 tun。自己的设计,依据 iproute2 的公开输出格式,没有参照上游。
package routeget

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Info 是路由查询的结果。
type Info struct {
	Line string // 命令输出(一行,空白已规整)
	Dev  string // 转出的设备
	Via  string // 网关,直连路由没有
	Tun  bool   // 设备是 tun 设备(/sys/class/net/<dev>/tun_flags 存在)
}

// Getter 零值可用。
type Getter struct {
	Run         system.Runner // 默认 system.ExecRunner
	SysClassNet string        // 默认 /sys/class/net
}

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.:@][A-Za-z0-9_.:@-]{0,31}$`)

// Get 执行 `ip -o route get <dst> from <src> iif <iif>`。参数不合法、命令失败、输出里没有 dev 都返回 error,
// 不把「查不到」当成任何一种结论。
func (g *Getter) Get(ctx context.Context, dst, src, iif string) (Info, error) {
	if net.ParseIP(dst) == nil || net.ParseIP(src) == nil || !ifaceRe.MatchString(iif) {
		return Info{}, errors.New("路由查询的参数不合法")
	}
	run := g.Run
	if run == nil {
		run = system.ExecRunner
	}
	r, err := run(ctx, "ip", "-o", "route", "get", dst, "from", src, "iif", iif)
	if err != nil {
		return Info{}, fmt.Errorf("运行 ip route get 失败:%w", err)
	}
	if r.Code != 0 {
		msg := strings.Join(strings.Fields(r.Stderr), " ")
		if msg == "" {
			msg = fmt.Sprintf("退出码 %d", r.Code)
		}
		return Info{}, errors.New("ip route get 失败:" + msg)
	}
	var line string
	for _, l := range strings.Split(r.Stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	line = strings.Join(strings.Fields(strings.ReplaceAll(line, `\`, " ")), " ")
	info := Info{Line: line}
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "via":
			if info.Via == "" {
				info.Via = f[i+1]
			}
		case "dev":
			if info.Dev == "" {
				info.Dev = f[i+1]
			}
		}
	}
	if info.Dev == "" || !ifaceRe.MatchString(info.Dev) {
		return Info{}, errors.New("ip route get 的输出里没有出口设备")
	}
	sys := g.SysClassNet
	if sys == "" {
		sys = "/sys/class/net"
	}
	if _, err := os.Stat(filepath.Join(sys, info.Dev, "tun_flags")); err == nil {
		info.Tun = true
	}
	return info, nil
}
