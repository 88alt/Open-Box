package system

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// 局域网接口的判定(对齐上游 v0.1.462 面板服务端 `$t` / `SP` / `AP`):
//
// OpenWrt 上接口属于哪个逻辑网络由 netifd 管,`ubus call network.interface dump` 给出
// 「逻辑网络名(lan / wan / wan6 …)→ 设备名(br-lan / eth0.2 …)」的对应。名字以
// lan 开头就是家里局域网,以 wan / wwan 开头是外网;其余(guest、iot 之类)不归这两类。
// 没有 ubus(Debian、容器、开发机)时上层退回按私有地址判断,与上游 systemd 分支一致。

// ubusDump 是 `ubus call network.interface dump` 里用得到的部分。
type ubusDump struct {
	Interface []struct {
		Interface string `json:"interface"`
		Device    string `json:"device"`
		L3Device  string `json:"l3_device"`
	} `json:"interface"`
}

// ParseUbusNetworkDump 把 ubus 的接口清单解析成「设备名 → 逻辑网络名」。
//
// 与上游 `SP` 一致:优先 l3_device(拨号后 IP 在 pppoe-wan 这类设备上),设备名相同的
// 条目以不带 `6` 结尾的逻辑名(wan 胜过 wan6)为准;读不出来当空清单,不报错。
func ParseUbusNetworkDump(raw []byte) map[string]string {
	out := map[string]string{}
	var d ubusDump
	if err := json.Unmarshal(raw, &d); err != nil {
		return out
	}
	for _, it := range d.Interface {
		dev := it.L3Device
		if dev == "" {
			dev = it.Device
		}
		if dev == "" || it.Interface == "" {
			continue
		}
		old, seen := out[dev]
		if !seen || (strings.HasSuffix(old, "6") && !strings.HasSuffix(it.Interface, "6")) {
			out[dev] = it.Interface
		}
	}
	return out
}

var (
	lanLogicalRe = regexp.MustCompile(`(?i)^lan`)
	wanLogicalRe = regexp.MustCompile(`(?i)^w(w)?an`)
)

// LogicalIfaceKind 判断逻辑网络名属于哪一类:lan(家里局域网)/ wan(外网)/ ""(都不是)。
// 与上游 `AP` 一致。
func LogicalIfaceKind(logical string) string {
	switch {
	case lanLogicalRe.MatchString(logical):
		return "lan"
	case wanLogicalRe.MatchString(logical):
		return "wan"
	default:
		return ""
	}
}

// ubusRunner 读 ubus 清单:输出比默认的 4 KiB 上限长(接口多的路由器),单独放宽。
var ubusRunner Runner = ExecRunnerCap(1 << 20)

// UbusIfaceKinds 问 netifd 要接口清单,返回「设备名 → lan / wan」,只留这两类。
// 没有 ubus、超时、输出看不懂都返回空(调用方退回按地址判断),不报错。
func UbusIfaceKinds(run Runner) map[string]string {
	out := map[string]string{}
	if run == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := run(ctx, "ubus", "call", "network.interface", "dump")
	if err != nil || res.Code != 0 {
		return out
	}
	for dev, logical := range ParseUbusNetworkDump([]byte(res.Stdout)) {
		if k := LogicalIfaceKind(logical); k != "" {
			out[dev] = k
		}
	}
	return out
}
