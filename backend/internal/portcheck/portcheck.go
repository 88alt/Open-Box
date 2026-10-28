// Package portcheck 实现「共享网络」保存服务器前的端口检测(GET /api/openbox/servers/port-check)。
//
// 自己的设计,依据前端 checkServerPort / OpenboxPortCheck 的类型、ServerEditDialog 的用法和 core 的入站常量,
// 没有参照上游。只读:不绑定端口、不连接、不改任何东西。
package portcheck

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 前端 OpenboxPortCheck.reason 的取值。
const (
	ReasonInvalid   = "invalid"   // 不是 1–65535 的整数
	ReasonReserved  = "reserved"  // 面板 / 内核自用的端口
	ReasonServer    = "server"    // 已被另一台共享服务器占用(name 是它的名字)
	ReasonListening = "listening" // 已被路由器上其它服务监听
)

// Result 就是前端的 OpenboxPortCheck。
type Result struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Name   string `json:"name,omitempty"`
}

// Server 是档案里的一台共享服务器(只取检测要用的字段)。
type Server struct {
	ID   string
	Name string
	Port int
}

// KernelOwnPorts 是内核配置里固定占用的端口:面板专用回环入站 7891、内核 DNS 入站 7853、
// 「只让这些终端进内核」的直连 DNS 入站 7855、控制接口 9095。取自 core/internal/config 的常量
// (PanelInboundPort / DNSInboundPort / DNSDirectInboundPort / 控制接口地址);core 改了这些,这里要跟着改。
// 另外会读已部署的配置补上它实际用的端口,所以这个列表只是读不到配置时的兜底。
var KernelOwnPorts = []int{7853, 7855, 7891, 9095}

// Checker 做一次端口检测。
type Checker struct {
	Servers    func(ctx context.Context) ([]Server, error) // 档案里的共享服务器
	Reserved   []int                                       // 面板自己的监听端口等,不含 KernelOwnPorts(那个总会算上)
	ConfigPath string                                      // 已部署的 sing-box 配置,用来补读内核实际占用的端口;为空 / 读不到就只用 KernelOwnPorts
	Listeners  func() (*system.Listeners, error)           // 此刻的端口占用;为空 = 读 /proc/net
}

// Check 判断 port 能不能给 id 这台服务器用。id 是正在编辑的那台(新建时是草稿 id),它自己占的端口不算冲突。
// 顺序与前端提示一致:无效 → 自用 → 别的服务器 → 别的服务在监听。
// 读不到端口占用情况时返回 error(不猜,见 DESIGN 第 4 节)。
//
// 「别的服务在监听」不知道这台服务器是 TCP 还是 UDP 协议(请求里只有端口和 id),所以 TCP 和 UDP 任一被占都算占用;
// 这是保守的:可能把「TCP 被占、想用 UDP 协议」也拦下,宁可多提示。
// 已保存的这台服务器如果端口没变,端口本来就是内核替它占着的,不查监听。
func (c *Checker) Check(ctx context.Context, port int, id string) (Result, error) {
	if port < 1 || port > 65535 {
		return Result{Reason: ReasonInvalid}, nil
	}
	if c.reserved()[port] {
		return Result{Reason: ReasonReserved}, nil
	}
	var servers []Server
	if c.Servers != nil {
		var err error
		if servers, err = c.Servers(ctx); err != nil {
			return Result{}, err
		}
	}
	own := false
	for _, s := range servers {
		if s.Port != port {
			continue
		}
		if s.ID == id {
			own = true
			continue
		}
		return Result{Reason: ReasonServer, Name: s.Name}, nil
	}
	if own {
		return Result{OK: true}, nil
	}
	read := c.Listeners
	if read == nil {
		read = func() (*system.Listeners, error) { return system.ReadListeners("/proc/net") }
	}
	l, err := read()
	if err != nil {
		return Result{}, err
	}
	if l.TCP[port] || l.UDP[port] {
		return Result{Reason: ReasonListening}, nil
	}
	return Result{OK: true}, nil
}

func (c *Checker) reserved() map[int]bool {
	m := map[int]bool{}
	for _, p := range KernelOwnPorts {
		m[p] = true
	}
	for _, p := range c.Reserved {
		m[p] = true
	}
	for _, p := range kernelPortsFromConfig(c.ConfigPath) {
		m[p] = true
	}
	return m
}

// kernelPortsFromConfig 读已部署的配置:控制接口的端口,和所有不是共享服务器的入站(标签不以 share- 开头)的监听端口。
// 共享服务器自己的入站要排除,否则每台已启用的服务器都会被当成「内核自用」。读不到 / 读不懂就返回空。
func kernelPortsFromConfig(path string) []int {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		Inbounds []struct {
			Tag        string  `json:"tag"`
			ListenPort float64 `json:"listen_port"`
		} `json:"inbounds"`
		Experimental struct {
			ClashAPI struct {
				Controller string `json:"external_controller"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return nil
	}
	var out []int
	for _, in := range cfg.Inbounds {
		if strings.HasPrefix(in.Tag, "share-") {
			continue
		}
		if p := int(in.ListenPort); p >= 1 && p <= 65535 {
			out = append(out, p)
		}
	}
	if ec := cfg.Experimental.ClashAPI.Controller; ec != "" {
		if i := strings.LastIndexByte(ec, ':'); i >= 0 {
			if p, err := strconv.Atoi(ec[i+1:]); err == nil && p >= 1 && p <= 65535 {
				out = append(out, p)
			}
		}
	}
	return out
}

// ServersFromProfile 从档案(profile.Get 的结果)里取出共享服务器;格式不对的条目跳过。
func ServersFromProfile(p map[string]any) []Server {
	list, _ := p["servers"].([]any)
	var out []Server
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		port := 0
		switch v := m["port"].(type) {
		case float64:
			port = int(v)
		case int:
			port = v
		case int64:
			port = int(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				port = int(n)
			}
		}
		if port < 1 {
			continue
		}
		out = append(out, Server{ID: id, Name: name, Port: port})
	}
	return out
}
