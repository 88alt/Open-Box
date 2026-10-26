package sub

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrClashInvalid 表示内容像 Clash 配置,但 proxies 部分的 YAML 读不懂(语法错误、用了不支持的写法)。
var ErrClashInvalid = errors.New("Clash 订阅无法解析")

var clashMarker = regexp.MustCompile(`(?m)^proxies\s*:`)

func looksLikeClash(text string) bool { return clashMarker.MatchString(text) }

// Clash 里不是真节点的条目,直接忽略(不算「没能导入」)。
var clashNonNode = map[string]bool{
	"direct": true, "reject": true, "reject-drop": true, "pass": true, "dns": true, "compatible": true,
}

// extractProxiesSection 从整份 Clash 配置里切出顶层 proxies 段。
// 只解析这一段:配置里其它部分(规则、DNS、多行文本)用了什么 YAML 写法都不影响订阅导入。
func extractProxiesSection(text string) string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if clashMarker.MatchString(strings.TrimRight(l, "\r")) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], "\r")
		if l == "" {
			continue
		}
		switch l[0] {
		case ' ', '\t', '#', '-', ']', '}':
			continue
		}
		end = i
		break
	}
	return strings.Join(lines[start:end], "\n")
}

// parseClash 解析 Clash / Mihomo 配置里的 proxies。proxy-providers 不处理(那是别的订阅地址)。
func parseClash(text string) (*Result, error) {
	v, err := parseYAML(extractProxiesSection(text))
	if err != nil {
		return nil, fmt.Errorf("%w:%v", ErrClashInvalid, err)
	}
	top, _ := v.(map[string]any)
	list, _ := top["proxies"].([]any)
	res := &Result{Format: FormatClash, Nodes: []Node{}, Skipped: []Skipped{}}
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		n, skip := clashNode(m)
		if skip != nil {
			res.Skipped = append(res.Skipped, *skip)
			continue
		}
		if n == nil {
			continue
		}
		res.Nodes = append(res.Nodes, *n)
		if len(res.Nodes) >= MaxNodes {
			break
		}
	}
	return res, nil
}

// ---- 取值小工具 ----

// cstr 取第一个非空的标量,统一成字符串(YAML 解析器里数字都是字符串,布尔是 bool)。
func cstr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return ""
}

func cbool(m map[string]any, key string) (val, set bool) {
	switch v := m[key].(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func cmap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

// clist 取字符串列表;写成逗号分隔的单个字符串也认。
func clist(m map[string]any, key string) []string {
	var out []string
	switch v := m[key].(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, e := range strings.Split(v, ",") {
			if e = strings.TrimSpace(e); e != "" {
				out = append(out, e)
			}
		}
	}
	return out
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ---- 节点 ----

func clashNode(m map[string]any) (*Node, *Skipped) {
	name := cstr(m, "name")
	typ := strings.ToLower(cstr(m, "type"))
	skip := func(reason, detail string) *Skipped {
		return &Skipped{Name: name, Type: typ, Reason: reason, Detail: detail}
	}
	if clashNonNode[typ] {
		return nil, nil
	}
	server := cstr(m, "server")
	if server == "" {
		return nil, skip(ReasonInvalid, "缺少服务器地址")
	}
	portText := cstr(m, "port")
	if portText == "" && (typ == "hysteria2" || typ == "hy2") {
		// 只写了 ports 范围:取第一个端口当主端口
		first := strings.SplitN(strings.SplitN(cstr(m, "ports"), ",", 2)[0], "-", 2)[0]
		portText = strings.TrimSpace(first)
	}
	port, err := parsePort(portText)
	if err != nil {
		return nil, skip(ReasonInvalid, err.Error())
	}

	nodeType := typ
	var fields map[string]any
	switch typ {
	case "ss":
		nodeType = "shadowsocks"
		fields, err = clashSS(m)
	case "vmess":
		fields, err = clashVMess(m)
	case "vless":
		fields, err = clashVLESS(m)
	case "trojan":
		fields, err = clashTrojan(m)
	case "hysteria2", "hy2":
		nodeType = "hysteria2"
		fields, err = clashHysteria2(m)
	case "tuic":
		fields, err = clashTUIC(m)
	case "anytls":
		fields, err = clashAnyTLS(m)
	case "socks5", "socks":
		nodeType = "socks"
		fields = clashAuth(m)
	case "http":
		fields = clashAuth(m)
		if tls := clashTLS(m, false, false, false); tls != nil {
			fields["tls"] = tls
		}
	default:
		return nil, skip(ReasonUnsupportedType, "")
	}
	if err != nil {
		var ue *unsupportedError
		if asUnsupported(err, &ue) {
			return nil, skip(ue.reason, ue.detail)
		}
		return nil, skip(ReasonInvalid, err.Error())
	}
	n := newNode(nodeType, name, server, port, fields)
	n.OriginalTag = n.Tag
	return n, nil
}

func clashAuth(m map[string]any) map[string]any {
	f := map[string]any{}
	if u := cstr(m, "username"); u != "" {
		f["username"] = u
	}
	if p := cstr(m, "password"); p != "" {
		f["password"] = p
	}
	return f
}

// clashTLS 生成 tls 字段。fixed 为真表示这类协议必走 TLS(trojan / hysteria2 / tuic / anytls),
// 配置里的 tls: false 不起作用;否则以配置里的 tls 为准(没写就是关)。有 reality-opts 一律开。
func clashTLS(m map[string]any, on, fixed, quic bool) map[string]any {
	reality := cmap(m, "reality-opts")
	if v, ok := cbool(m, "tls"); ok && !fixed {
		on = v
	}
	if fixed {
		on = true
	}
	if reality != nil {
		on = true
	}
	if !on {
		return nil
	}
	tls := map[string]any{"enabled": true}
	if sni := cstr(m, "servername", "sni"); sni != "" {
		tls["server_name"] = sni
	}
	if a := clist(m, "alpn"); len(a) > 0 {
		tls["alpn"] = toAny(a)
	}
	if fp := cstr(m, "client-fingerprint"); fp != "" && fp != "none" && !quic {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	if reality != nil {
		r := map[string]any{"enabled": true}
		if pk := cstr(reality, "public-key"); pk != "" {
			r["public_key"] = pk
		}
		if sid := cstr(reality, "short-id"); sid != "" {
			r["short_id"] = sid
		}
		tls["reality"] = r
	}
	return tls
}

// clashHeaders 把 headers 映射整理成 {名: 值};值是列表时取第一个。
func clashHeaders(h map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range h {
		switch t := v.(type) {
		case string:
			if t != "" {
				out[k] = t
			}
		case []any:
			if len(t) > 0 {
				if s, ok := t[0].(string); ok && s != "" {
					out[k] = s
				}
			}
		}
	}
	return out
}

func headerFold(h map[string]any, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// clashTransport 按 network / *-opts 生成 transport;tcp 返回 nil。
func clashTransport(m map[string]any) (map[string]any, error) {
	network := strings.ToLower(cstr(m, "network"))
	switch network {
	case "", "tcp":
		return nil, nil
	case "ws", "websocket":
		o := cmap(m, "ws-opts")
		headers := clashHeaders(cmap(o, "headers"))
		if up, _ := cbool(o, "v2ray-http-upgrade"); up {
			t := map[string]any{"type": "httpupgrade"}
			if h := headerFold(headers, "Host"); h != "" {
				t["host"] = h
			}
			if p := cstr(o, "path"); p != "" {
				t["path"] = p
			}
			return t, nil
		}
		t := map[string]any{"type": "ws"}
		if p := cstr(o, "path"); p != "" {
			t["path"] = p
		}
		if len(headers) > 0 {
			t["headers"] = headers
		}
		if n, err := strconv.Atoi(cstr(o, "max-early-data")); err == nil && n > 0 {
			t["max_early_data"] = n
			hn := cstr(o, "early-data-header-name")
			if hn == "" {
				hn = "Sec-WebSocket-Protocol"
			}
			t["early_data_header_name"] = hn
		}
		return t, nil
	case "grpc":
		o := cmap(m, "grpc-opts")
		t := map[string]any{"type": "grpc"}
		if s := cstr(o, "grpc-service-name"); s != "" {
			t["service_name"] = s
		}
		return t, nil
	case "h2":
		o := cmap(m, "h2-opts")
		t := map[string]any{"type": "http"}
		if h := clist(o, "host"); len(h) > 0 {
			t["host"] = toAny(h)
		}
		if p := cstr(o, "path"); p != "" {
			t["path"] = p
		}
		return t, nil
	case "http":
		o := cmap(m, "http-opts")
		t := map[string]any{"type": "http"}
		headers := clashHeaders(cmap(o, "headers"))
		if h := clist(cmap(o, "headers"), "Host"); len(h) > 0 {
			t["host"] = toAny(h)
		} else if h := headerFold(headers, "Host"); h != "" {
			t["host"] = []any{h}
		}
		if p := clist(o, "path"); len(p) > 0 {
			t["path"] = p[0]
		}
		if me := cstr(o, "method"); me != "" {
			t["method"] = me
		}
		return t, nil
	}
	return nil, &unsupportedError{reason: ReasonUnsupportedTransport, detail: network}
}

func clashSS(m map[string]any) (map[string]any, error) {
	method, pw := cstr(m, "cipher"), cstr(m, "password")
	if method == "" {
		return nil, fmt.Errorf("缺少加密方式")
	}
	if pw == "" && method != "none" {
		return nil, fmt.Errorf("缺少密码")
	}
	f := map[string]any{"method": method, "password": pw}
	opts := cmap(m, "plugin-opts")
	switch plugin := strings.ToLower(cstr(m, "plugin")); plugin {
	case "":
	case "obfs", "obfs-local", "simple-obfs":
		f["plugin"] = "obfs-local"
		if mode := cstr(opts, "mode"); mode != "" {
			po := "obfs=" + mode
			if h := cstr(opts, "host"); h != "" {
				po += ";obfs-host=" + h
			}
			f["plugin_opts"] = po
		}
	case "v2ray-plugin":
		f["plugin"] = "v2ray-plugin"
		mode := cstr(opts, "mode")
		if mode == "" {
			mode = "websocket"
		}
		po := "mode=" + mode
		if tls, _ := cbool(opts, "tls"); tls {
			po += ";tls"
		}
		if h := cstr(opts, "host"); h != "" {
			po += ";host=" + h
		}
		if p := cstr(opts, "path"); p != "" {
			po += ";path=" + p
		}
		f["plugin_opts"] = po
	default:
		return nil, &unsupportedError{reason: ReasonUnsupportedPlugin, detail: plugin}
	}
	return f, nil
}

func clashVMess(m map[string]any) (map[string]any, error) {
	id := cstr(m, "uuid")
	if id == "" {
		return nil, fmt.Errorf("缺少 uuid")
	}
	alter, _ := strconv.Atoi(cstr(m, "alterId", "alter-id", "alter_id"))
	f := map[string]any{"uuid": id, "alter_id": alter}
	if sc := cstr(m, "cipher"); sc != "" {
		f["security"] = sc
	}
	if err := clashStream(m, f, false); err != nil {
		return nil, err
	}
	return f, nil
}

func clashVLESS(m map[string]any) (map[string]any, error) {
	id := cstr(m, "uuid")
	if id == "" {
		return nil, fmt.Errorf("缺少 uuid")
	}
	if enc := cstr(m, "encryption"); enc != "" && enc != "none" {
		return nil, &unsupportedError{reason: ReasonUnsupportedType, detail: "vless encryption"}
	}
	f := map[string]any{"uuid": id}
	if flow := cstr(m, "flow"); flow != "" {
		f["flow"] = flow
	}
	if err := clashStream(m, f, false); err != nil {
		return nil, err
	}
	return f, nil
}

func clashTrojan(m map[string]any) (map[string]any, error) {
	pw := cstr(m, "password")
	if pw == "" {
		return nil, fmt.Errorf("缺少密码")
	}
	f := map[string]any{"password": pw}
	if err := clashStream(m, f, true); err != nil {
		return nil, err
	}
	return f, nil
}

// clashStream 写入 tls 与 transport。
func clashStream(m, f map[string]any, tlsFixed bool) error {
	tr, err := clashTransport(m)
	if err != nil {
		return err
	}
	if tr != nil {
		f["transport"] = tr
	}
	if tls := clashTLS(m, false, tlsFixed, false); tls != nil {
		f["tls"] = tls
	}
	return nil
}

func clashHysteria2(m map[string]any) (map[string]any, error) {
	pw := cstr(m, "password")
	if pw == "" {
		return nil, fmt.Errorf("缺少密码")
	}
	f := map[string]any{"password": pw}
	f["tls"] = clashTLS(m, true, true, true)
	if o := cstr(m, "obfs"); o != "" && !strings.EqualFold(o, "none") {
		obfs := map[string]any{"type": o}
		if op := cstr(m, "obfs-password"); op != "" {
			obfs["password"] = op
		}
		f["obfs"] = obfs
	}
	if ports := cstr(m, "ports", "mport"); ports != "" {
		f["server_ports"] = ports
		if iv := cstr(m, "hop-interval"); iv != "" {
			f["hop_interval"] = iv
		}
	}
	return f, nil
}

func clashTUIC(m map[string]any) (map[string]any, error) {
	id := cstr(m, "uuid")
	if id == "" {
		if cstr(m, "token") != "" {
			return nil, &unsupportedError{reason: ReasonUnsupportedType, detail: "tuic v4"}
		}
		return nil, fmt.Errorf("缺少 uuid")
	}
	f := map[string]any{"uuid": id, "password": cstr(m, "password")}
	if cc := cstr(m, "congestion-controller", "congestion-control", "congestion_control"); cc != "" {
		f["congestion_control"] = cc
	}
	f["tls"] = clashTLS(m, true, true, true)
	return f, nil
}

func clashAnyTLS(m map[string]any) (map[string]any, error) {
	pw := cstr(m, "password")
	if pw == "" {
		return nil, fmt.Errorf("缺少密码")
	}
	f := map[string]any{"password": pw}
	f["tls"] = clashTLS(m, true, true, false)
	return f, nil
}
