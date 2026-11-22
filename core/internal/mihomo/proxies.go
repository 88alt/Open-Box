// Package mihomo:sing-box 档案 → Mihomo(Clash.Meta)配置的生成,按上游 v0.1.477 的 Node 调用形状对齐。
// 本文件是第一块:mihomoProxies(节点 → Mihomo proxies)。
//
// 做法:先用 emit 包得到已经规整过的 sing-box 出站 / endpoint(字段取舍、传输层和 TLS 的规则与
// 生成 sing-box 配置时完全一致),再把它逐字段翻成 Mihomo 的写法。Mihomo 表达不了的写法(socks4、
// quic 传输、未知的 ss 插件)直接报错,不静默改成别的写法。
package mihomo

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/core/internal/emit"
	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// Proxies 对应 core 方法 mihomoProxies(nodes):节点数组 → Mihomo proxies 数组(name 取节点 tag)。
// 任何一个节点翻不了就整个报错,错误文字带上节点 tag。
func Proxies(nodes any) ([]any, error) {
	list, ok := nodes.([]any)
	if !ok {
		return nil, fmt.Errorf("mihomoProxies: nodes must be an array")
	}
	out := make([]any, 0, len(list))
	for _, n := range list {
		p, err := proxyOf(n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", jsx.ToString(jsx.Get(n, "tag")), err)
		}
		out = append(out, p)
	}
	return out, nil
}

func proxyOf(node any) (map[string]any, error) {
	if jsx.Get(node, "type") == "wireguard" {
		ep, err := emit.EmitEndpoint(node)
		if err != nil {
			return nil, err
		}
		return wireguard(ep, node), nil
	}
	o, err := emit.EmitOutbound(node)
	if err != nil {
		return nil, err
	}
	p := map[string]any{"name": o["tag"], "server": o["server"], "port": o["server_port"]}
	if d, ok := o["detour"].(string); ok && d != "" {
		p["dialer-proxy"] = d
	}
	switch o["type"] {
	case "shadowsocks":
		p["type"] = "ss"
		p["cipher"] = o["method"]
		p["password"] = o["password"]
		p["udp"] = true
		if err := ssPlugin(p, o); err != nil {
			return nil, err
		}
	case "vmess":
		p["type"] = "vmess"
		p["uuid"] = o["uuid"]
		p["alterId"] = o["alter_id"]
		p["cipher"] = o["security"]
		p["udp"] = true
		if err := streamOpts(p, o); err != nil {
			return nil, err
		}
	case "vless":
		p["type"] = "vless"
		p["uuid"] = o["uuid"]
		if v, ok := o["flow"]; ok {
			p["flow"] = v
		}
		p["udp"] = true
		if err := streamOpts(p, o); err != nil {
			return nil, err
		}
	case "trojan":
		p["type"] = "trojan"
		p["password"] = o["password"]
		p["udp"] = true
		if err := streamOpts(p, o); err != nil {
			return nil, err
		}
	case "anytls":
		p["type"] = "anytls"
		p["password"] = o["password"]
		p["udp"] = true
		tlsOpts(p, o, "sni")
	case "hysteria2":
		hysteria2(p, o)
	case "tuic":
		p["type"] = "tuic"
		p["uuid"] = o["uuid"]
		p["password"] = o["password"]
		p["udp-relay-mode"] = "native"
		if v, ok := o["congestion_control"]; ok {
			p["congestion-controller"] = v
		}
		tlsOpts(p, o, "sni")
	case "socks":
		if v, ok := o["version"]; ok && v != "5" {
			return nil, fmt.Errorf("socks version %s is not supported by Mihomo", jsx.ToString(v))
		}
		p["type"] = "socks5"
		p["udp"] = true
		creds(p, o)
	case "http":
		p["type"] = "http"
		creds(p, o)
		tlsOpts(p, o, "sni")
	default:
		return nil, fmt.Errorf("no Mihomo mapping for type: %s", jsx.ToString(o["type"]))
	}
	return p, nil
}

func creds(p, o map[string]any) {
	if v, ok := o["username"]; ok {
		p["username"] = v
	}
	if v, ok := o["password"]; ok {
		p["password"] = v
	}
}

// tlsOpts 翻 sing-box 的 tls 块。sniKey:vmess / vless 叫 servername,其余叫 sni。
func tlsOpts(p, o map[string]any, sniKey string) {
	t, ok := o["tls"].(map[string]any)
	if !ok {
		return
	}
	p["tls"] = true
	if v, ok := t["server_name"]; ok {
		p[sniKey] = v
	}
	if v, ok := t["alpn"]; ok {
		p["alpn"] = v
	}
	if t["insecure"] == true {
		p["skip-cert-verify"] = true
	}
	if u, ok := t["utls"].(map[string]any); ok {
		if fp, ok := u["fingerprint"]; ok {
			p["client-fingerprint"] = fp
		}
	}
	if r, ok := t["reality"].(map[string]any); ok {
		ro := map[string]any{}
		if v, ok := r["public_key"]; ok {
			ro["public-key"] = v
		}
		if v, ok := r["short_id"]; ok {
			ro["short-id"] = v
		}
		p["reality-opts"] = ro
	}
}

// streamOpts:vmess / vless / trojan 的 TLS 与传输层。
func streamOpts(p, o map[string]any) error {
	sni := "servername"
	if o["type"] == "trojan" {
		sni = "sni"
	}
	tlsOpts(p, o, sni)
	t, ok := o["transport"].(map[string]any)
	if !ok {
		return nil
	}
	switch t["type"] {
	case "ws", "httpupgrade":
		p["network"] = "ws"
		opts := map[string]any{}
		if v, ok := t["path"]; ok {
			opts["path"] = v
		}
		headers := map[string]any{}
		if h, ok := t["headers"].(map[string]any); ok {
			for k, v := range h {
				headers[k] = v
			}
		}
		if t["type"] == "httpupgrade" {
			opts["v2ray-http-upgrade"] = true
			if h, ok := t["host"]; ok {
				headers["Host"] = h
			}
		}
		if len(headers) > 0 {
			opts["headers"] = headers
		}
		if v, ok := t["max_early_data"]; ok {
			opts["max-early-data"] = v
		}
		if v, ok := t["early_data_header_name"]; ok {
			opts["early-data-header-name"] = v
		}
		p["ws-opts"] = opts
	case "http":
		p["network"] = "http"
		opts := map[string]any{}
		if v, ok := t["method"]; ok {
			opts["method"] = v
		}
		if v, ok := t["path"]; ok {
			opts["path"] = []any{v}
		}
		headers := map[string]any{}
		if h, ok := t["headers"].(map[string]any); ok {
			for k, v := range h {
				if _, isArr := v.([]any); isArr {
					headers[k] = v
				} else {
					headers[k] = []any{v}
				}
			}
		}
		if h, ok := t["host"]; ok {
			headers["Host"] = h
		}
		if len(headers) > 0 {
			opts["headers"] = headers
		}
		p["http-opts"] = opts
	case "grpc":
		p["network"] = "grpc"
		opts := map[string]any{}
		if v, ok := t["service_name"]; ok {
			opts["grpc-service-name"] = v
		}
		p["grpc-opts"] = opts
	default:
		return fmt.Errorf("transport %s is not supported by Mihomo", jsx.ToString(t["type"]))
	}
	return nil
}

var hopDurRe = regexp.MustCompile(`^(\d+)(ms|s|m|h)$`)

func hysteria2(p, o map[string]any) {
	p["type"] = "hysteria2"
	p["password"] = o["password"]
	tlsOpts(p, o, "sni")
	if ob, ok := o["obfs"].(map[string]any); ok {
		if v, ok := ob["type"]; ok {
			p["obfs"] = v
		}
		if v, ok := ob["password"]; ok {
			p["obfs-password"] = v
		}
	}
	if ports, ok := o["server_ports"].([]any); ok && len(ports) > 0 {
		parts := make([]string, 0, len(ports))
		for _, x := range ports {
			parts = append(parts, strings.ReplaceAll(jsx.ToString(x), ":", "-"))
		}
		p["ports"] = strings.Join(parts, ",")
		if iv, ok := o["hop_interval"].(string); ok {
			if sec, ok := hopSeconds(iv); ok {
				p["hop-interval"] = sec
			}
		}
	}
}

// hopSeconds:sing-box 的时长("30s" / "2m" / "500ms")→ Mihomo 的整数秒(不足 1 秒按 1 秒)。
func hopSeconds(s string) (float64, bool) {
	m := hopDurRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "ms":
		n /= 1000
	case "m":
		n *= 60
	case "h":
		n *= 3600
	}
	return math.Max(1, math.Round(n)), true
}

// ssPlugin:SIP003 插件。sing-box 的 plugin_opts 是 "k=v;k=v;flag" 文本,Mihomo 要结构化的 plugin-opts。
func ssPlugin(p, o map[string]any) error {
	name, ok := o["plugin"].(string)
	if !ok || name == "" {
		return nil
	}
	kv := map[string]string{}
	if s, ok := o["plugin_opts"].(string); ok {
		for _, part := range strings.Split(s, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			k, v, has := strings.Cut(part, "=")
			if !has {
				v = "true"
			}
			kv[k] = v
		}
	}
	opts := map[string]any{}
	switch name {
	case "obfs-local", "obfs":
		p["plugin"] = "obfs"
		if v := kv["obfs"]; v != "" {
			opts["mode"] = v
		}
		if v := kv["obfs-host"]; v != "" {
			opts["host"] = v
		}
	case "v2ray-plugin":
		p["plugin"] = "v2ray-plugin"
		opts["mode"] = "websocket"
		if v := kv["mode"]; v != "" {
			opts["mode"] = v
		}
		if v := kv["host"]; v != "" {
			opts["host"] = v
		}
		if v := kv["path"]; v != "" {
			opts["path"] = v
		}
		if _, ok := kv["tls"]; ok {
			opts["tls"] = true
		}
		if _, ok := kv["mux"]; ok {
			opts["mux"] = kv["mux"] != "0" && kv["mux"] != "false"
		}
	default:
		return fmt.Errorf("shadowsocks plugin %s is not supported by Mihomo", name)
	}
	if len(opts) > 0 {
		p["plugin-opts"] = opts
	}
	return nil
}

// wireguard:endpoint → Mihomo 的 wireguard 代理(单个对端)。
func wireguard(ep map[string]any, node any) map[string]any {
	p := map[string]any{"name": ep["tag"], "type": "wireguard", "udp": true}
	if v := jsx.Get(node, "server"); jsx.Truthy(v) {
		p["server"] = v
	}
	if v := jsx.Get(node, "server_port"); jsx.Truthy(v) {
		p["port"] = v
	}
	if v, ok := ep["private_key"]; ok {
		p["private-key"] = v
	}
	if peers, ok := ep["peers"].([]any); ok && len(peers) > 0 {
		if peer, ok := peers[0].(map[string]any); ok {
			if v, ok := peer["public_key"]; ok {
				p["public-key"] = v
			}
			if v, ok := peer["pre_shared_key"]; ok {
				p["pre-shared-key"] = v
			}
			if v, ok := peer["allowed_ips"]; ok {
				p["allowed-ips"] = v
			}
		}
	}
	if addrs, ok := ep["address"].([]any); ok {
		for _, a := range addrs {
			s := strings.SplitN(jsx.ToString(a), "/", 2)[0]
			if s == "" {
				continue
			}
			if strings.Contains(s, ":") {
				if _, has := p["ipv6"]; !has {
					p["ipv6"] = s
				}
			} else if _, has := p["ip"]; !has {
				p["ip"] = s
			}
		}
	}
	return p
}
