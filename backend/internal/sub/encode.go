package sub

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrNoShareLink 表示这个节点生成不出分享链接(类型不支持,或缺少必要字段)。
type ErrNoShareLink struct{ Reason string }

func (e *ErrNoShareLink) Error() string { return e.Reason }

func noLink(format string, a ...any) error {
	return &ErrNoShareLink{Reason: fmt.Sprintf(format, a...)}
}

// EncodeLink 把节点编码成单条分享链接,是 parseLink 的反向:ss(SIP002)/ vmess(v2rayN)/ vless / trojan /
// hysteria2 / tuic / anytls / socks5 / http(s)。链接里的节点名用 n.Tag。
// 只写 parseLink 读得回来的字段(tls / transport / reality / obfs / 跳跃端口 / ss 插件),所以
// parseLink(EncodeLink(n)) 与 n 在这些字段上一致;其余字段(如 multiplex)分享链接没有通行写法,不输出。
func EncodeLink(n Node) (string, error) {
	if strings.TrimSpace(n.Server) == "" || n.ServerPort < 1 || n.ServerPort > 65535 {
		return "", noLink("节点缺少服务器地址或端口")
	}
	f := n.Fields
	if f == nil {
		f = map[string]any{}
	}
	switch n.Type {
	case "shadowsocks", "ss":
		return encodeSS(n, f)
	case "vmess":
		return encodeVMess(n, f)
	case "vless":
		return encodeVLESS(n, f)
	case "trojan":
		return encodeTrojan(n, f)
	case "anytls":
		return encodeAnyTLS(n, f)
	case "hysteria2", "hy2":
		return encodeHysteria2(n, f)
	case "tuic":
		return encodeTUIC(n, f)
	case "socks", "socks5":
		return encodeSocks(n, f)
	case "http", "https":
		return encodeHTTP(n, f)
	}
	return "", noLink("暂不支持为 %s 类型的节点生成分享链接", n.Type)
}

// ---- 小工具 ----

func fstr(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

func fmap(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

func fbool(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

// flist 把字符串或字符串数组读成列表。
func flist(m map[string]any, k string) []string {
	switch v := m[k].(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	case []string:
		return v
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

// esc 做 URL 组件转义,空格写 %20(parseLink 用 PathUnescape / ParseQuery 读,两者都认)。
func esc(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// hostPort 拼 host:port,IPv6 加方括号。
func hostPort(server string, port int) string {
	if strings.Contains(server, ":") && !strings.HasPrefix(server, "[") {
		server = "[" + server + "]"
	}
	return server + ":" + strconv.Itoa(port)
}

// query 按写入顺序拼查询串,空值跳过。
type query struct{ parts []string }

func (q *query) add(k, v string) {
	if v == "" {
		return
	}
	q.parts = append(q.parts, esc(k)+"="+esc(v))
}

func (q *query) String() string {
	if len(q.parts) == 0 {
		return ""
	}
	return "?" + strings.Join(q.parts, "&")
}

func fragment(tag string) string {
	if strings.TrimSpace(tag) == "" {
		return ""
	}
	return "#" + esc(tag)
}

// addTLS 把 tls 字段写成 security / sni / alpn / fp / pbk / sid / allowInsecure。
// defaultOn 对应 parseLink 里该协议的默认(trojan 默认开 TLS):默认开而节点没开时要明写 security=none,否则读回来会变成开。
func addTLS(q *query, tls map[string]any, defaultOn bool) {
	enabled := tls != nil && fbool(tls, "enabled")
	reality := fmap(tls, "reality")
	switch {
	case reality != nil && fbool(reality, "enabled"):
		q.add("security", "reality")
	case enabled:
		q.add("security", "tls")
	case defaultOn:
		q.add("security", "none")
		return
	default:
		return
	}
	q.add("sni", fstr(tls, "server_name"))
	q.add("alpn", strings.Join(flist(tls, "alpn"), ","))
	if u := fmap(tls, "utls"); u != nil && fbool(u, "enabled") {
		q.add("fp", fstr(u, "fingerprint"))
	}
	if reality != nil && fbool(reality, "enabled") {
		q.add("pbk", fstr(reality, "public_key"))
		q.add("sid", fstr(reality, "short_id"))
	}
	if fbool(tls, "insecure") {
		q.add("allowInsecure", "1")
	}
}

// transportParts 把 transport 字段还原成 (type, host, path, serviceName);tcp(没有 transport)返回空 type。
func transportParts(tr map[string]any) (typ, host, path, service string, err error) {
	if tr == nil {
		return "", "", "", "", nil
	}
	switch t := fstr(tr, "type"); t {
	case "", "tcp":
		return "", "", "", "", nil
	case "ws":
		if h := fmap(tr, "headers"); h != nil {
			host = fstr(h, "Host")
			if host == "" {
				host = fstr(h, "host")
			}
		}
		return "ws", host, fstr(tr, "path"), "", nil
	case "grpc":
		return "grpc", "", "", fstr(tr, "service_name"), nil
	case "http":
		return "h2", strings.Join(flist(tr, "host"), ","), fstr(tr, "path"), "", nil
	case "httpupgrade":
		return "httpupgrade", fstr(tr, "host"), fstr(tr, "path"), "", nil
	default:
		return "", "", "", "", noLink("暂不支持为 %s 传输方式生成分享链接", t)
	}
}

// ---- 各协议 ----

func encodeVLESS(n Node, f map[string]any) (string, error) {
	uuid := fstr(f, "uuid")
	if uuid == "" {
		return "", noLink("节点缺少 uuid")
	}
	typ, host, path, service, err := transportParts(fmap(f, "transport"))
	if err != nil {
		return "", err
	}
	q := &query{}
	q.add("encryption", "none")
	q.add("flow", fstr(f, "flow"))
	addTLS(q, fmap(f, "tls"), false)
	q.add("type", typ)
	q.add("host", host)
	q.add("path", path)
	q.add("serviceName", service)
	return "vless://" + esc(uuid) + "@" + hostPort(n.Server, n.ServerPort) + q.String() + fragment(n.Tag), nil
}

func encodeTrojan(n Node, f map[string]any) (string, error) {
	pw := fstr(f, "password")
	if pw == "" {
		return "", noLink("节点缺少密码")
	}
	typ, host, path, service, err := transportParts(fmap(f, "transport"))
	if err != nil {
		return "", err
	}
	q := &query{}
	addTLS(q, fmap(f, "tls"), true)
	q.add("type", typ)
	q.add("host", host)
	q.add("path", path)
	q.add("serviceName", service)
	return "trojan://" + esc(pw) + "@" + hostPort(n.Server, n.ServerPort) + q.String() + fragment(n.Tag), nil
}

func encodeAnyTLS(n Node, f map[string]any) (string, error) {
	pw := fstr(f, "password")
	if pw == "" {
		return "", noLink("节点缺少密码")
	}
	q := &query{}
	if tls := fmap(f, "tls"); tls != nil {
		q.add("sni", fstr(tls, "server_name"))
		q.add("alpn", strings.Join(flist(tls, "alpn"), ","))
		if u := fmap(tls, "utls"); u != nil && fbool(u, "enabled") {
			q.add("fp", fstr(u, "fingerprint"))
		}
		if fbool(tls, "insecure") {
			q.add("insecure", "1")
		}
	}
	return "anytls://" + esc(pw) + "@" + hostPort(n.Server, n.ServerPort) + q.String() + fragment(n.Tag), nil
}

func encodeHysteria2(n Node, f map[string]any) (string, error) {
	pw := fstr(f, "password")
	if pw == "" {
		return "", noLink("节点缺少密码")
	}
	q := &query{}
	if tls := fmap(f, "tls"); tls != nil {
		q.add("sni", fstr(tls, "server_name"))
		q.add("alpn", strings.Join(flist(tls, "alpn"), ","))
		if fbool(tls, "insecure") {
			q.add("insecure", "1")
		}
	}
	if o := fmap(f, "obfs"); o != nil {
		q.add("obfs", fstr(o, "type"))
		q.add("obfs-password", fstr(o, "password"))
	}
	if sp := fstr(f, "server_ports"); sp != "" {
		q.add("mport", sp)
		q.add("hop-interval", fstr(f, "hop_interval"))
	}
	return "hysteria2://" + esc(pw) + "@" + hostPort(n.Server, n.ServerPort) + "/" + q.String() + fragment(n.Tag), nil
}

func encodeTUIC(n Node, f map[string]any) (string, error) {
	uuid := fstr(f, "uuid")
	if uuid == "" {
		return "", noLink("节点缺少 uuid")
	}
	q := &query{}
	q.add("congestion_control", fstr(f, "congestion_control"))
	if tls := fmap(f, "tls"); tls != nil {
		q.add("sni", fstr(tls, "server_name"))
		q.add("alpn", strings.Join(flist(tls, "alpn"), ","))
		if fbool(tls, "insecure") {
			q.add("allow_insecure", "1")
		}
	}
	return "tuic://" + esc(uuid) + ":" + esc(fstr(f, "password")) + "@" + hostPort(n.Server, n.ServerPort) + q.String() + fragment(n.Tag), nil
}

func userinfo(f map[string]any) string {
	u, p := fstr(f, "username"), fstr(f, "password")
	switch {
	case u == "" && p == "":
		return ""
	case p == "":
		return esc(u) + "@"
	}
	return esc(u) + ":" + esc(p) + "@"
}

func encodeSocks(n Node, f map[string]any) (string, error) {
	return "socks5://" + userinfo(f) + hostPort(n.Server, n.ServerPort) + fragment(n.Tag), nil
}

func encodeHTTP(n Node, f map[string]any) (string, error) {
	scheme := "http"
	q := &query{}
	if tls := fmap(f, "tls"); tls != nil && fbool(tls, "enabled") {
		scheme = "https"
		q.add("sni", fstr(tls, "server_name"))
	}
	return scheme + "://" + userinfo(f) + hostPort(n.Server, n.ServerPort) + q.String() + fragment(n.Tag), nil
}

func encodeSS(n Node, f map[string]any) (string, error) {
	method, pw := fstr(f, "method"), fstr(f, "password")
	if method == "" {
		return "", noLink("节点缺少加密方式")
	}
	ui := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + pw))
	out := "ss://" + ui + "@" + hostPort(n.Server, n.ServerPort)
	if plugin := fstr(f, "plugin"); plugin != "" {
		v := plugin
		if opts := fstr(f, "plugin_opts"); opts != "" {
			v += ";" + opts
		}
		out += "/?plugin=" + esc(v)
	}
	return out + fragment(n.Tag), nil
}

func encodeVMess(n Node, f map[string]any) (string, error) {
	uuid := fstr(f, "uuid")
	if uuid == "" {
		return "", noLink("节点缺少 uuid")
	}
	typ, host, path, service, err := transportParts(fmap(f, "transport"))
	if err != nil {
		return "", err
	}
	if typ == "" {
		typ = "tcp"
	}
	if typ == "grpc" {
		path = service // v2rayN 的 gRPC 把 serviceName 放在 path
	}
	aid := "0"
	switch v := f["alter_id"].(type) {
	case int:
		aid = strconv.Itoa(v)
	case float64:
		aid = strconv.Itoa(int(v))
	}
	m := map[string]any{
		"v": "2", "ps": n.Tag, "add": n.Server, "port": strconv.Itoa(n.ServerPort),
		"id": uuid, "aid": aid, "scy": firstNonEmpty(fstr(f, "security"), "auto"),
		"net": typ, "type": "none", "host": host, "path": path,
	}
	if tls := fmap(f, "tls"); tls != nil && fbool(tls, "enabled") {
		m["tls"] = "tls"
		if v := fstr(tls, "server_name"); v != "" {
			m["sni"] = v
		}
		if v := strings.Join(flist(tls, "alpn"), ","); v != "" {
			m["alpn"] = v
		}
		if u := fmap(tls, "utls"); u != nil && fbool(u, "enabled") {
			if v := fstr(u, "fingerprint"); v != "" {
				m["fp"] = v
			}
		}
	} else {
		m["tls"] = ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(b), nil
}
