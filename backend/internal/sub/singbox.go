package sub

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 核心 emit 认识的节点类型(wireguard 是 endpoint,字段不同,这里按订阅里常见写法照收)。
var singBoxNodeTypes = map[string]bool{
	"shadowsocks": true, "vmess": true, "vless": true, "trojan": true, "hysteria2": true,
	"tuic": true, "anytls": true, "socks": true, "http": true,
}

// 不是节点的内置出站类型,直接忽略(不算「没能导入」)。
var singBoxNonNode = map[string]bool{
	"direct": true, "block": true, "dns": true, "selector": true, "urltest": true,
}

// parseSingBox 读 sing-box 配置里的 outbounds(也接受直接给一个出站数组)。
func parseSingBox(data []byte) (*Result, error) {
	var outs []any
	var top any
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("不是合法的 JSON:%v", err)
	}
	switch v := top.(type) {
	case map[string]any:
		if arr, ok := v["outbounds"].([]any); ok {
			outs = arr
		} else {
			return nil, ErrUnrecognized
		}
	case []any:
		outs = v
	}
	res := &Result{Format: FormatSingBox, Nodes: []Node{}, Skipped: []Skipped{}}
	for _, o := range outs {
		m, ok := o.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		tag, _ := m["tag"].(string)
		if singBoxNonNode[typ] {
			continue
		}
		if !singBoxNodeTypes[typ] {
			res.Skipped = append(res.Skipped, Skipped{Name: tag, Type: typ, Reason: ReasonUnsupportedType})
			continue
		}
		server, _ := m["server"].(string)
		pf, _ := m["server_port"].(float64)
		port := int(pf)
		if strings.TrimSpace(server) == "" || port < 1 || port > 65535 {
			res.Skipped = append(res.Skipped, Skipped{Name: tag, Type: typ, Reason: ReasonInvalid, Detail: "缺少服务器地址或端口"})
			continue
		}
		if t, ok := m["transport"].(map[string]any); ok {
			if _, err := transportFrom(fmt.Sprint(t["type"]), "", "", ""); err != nil {
				res.Skipped = append(res.Skipped, Skipped{Name: tag, Type: typ, Reason: ReasonUnsupportedTransport, Detail: fmt.Sprint(t["type"])})
				continue
			}
		}
		fields := map[string]any{}
		for k, v := range m {
			switch k {
			case "tag", "type", "server", "server_port":
			default:
				fields[k] = v
			}
		}
		n := newNode(typ, tag, server, port, fields)
		n.OriginalTag = n.Tag
		res.Nodes = append(res.Nodes, *n)
		if len(res.Nodes) >= MaxNodes {
			break
		}
	}
	return res, nil
}
