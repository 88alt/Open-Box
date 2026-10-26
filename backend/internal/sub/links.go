package sub

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// parseLink 解析一条分享链接。返回节点,或返回「没能导入」的说明(二者必有其一)。
func parseLink(line string) (*Node, *Skipped) {
	i := strings.Index(line, "://")
	scheme := strings.ToLower(line[:i])
	var (
		n   *Node
		err error
	)
	switch scheme {
	case "ss":
		n, err = parseSS(line)
	case "vmess":
		n, err = parseVMess(line)
	case "vless":
		n, err = parseVLESS(line)
	case "trojan":
		n, err = parseTrojan(line)
	case "hysteria2", "hy2":
		n, err = parseHysteria2(line)
	case "tuic":
		n, err = parseTUIC(line)
	case "anytls":
		n, err = parseAnyTLS(line)
	case "socks", "socks5", "socks5h":
		n, err = parseSocks(line)
	case "http", "https":
		n, err = parseHTTPProxy(line, scheme == "https")
	default:
		return nil, &Skipped{Name: nameOf(line), Type: scheme, Reason: ReasonUnsupportedType}
	}
	if err != nil {
		var ue *unsupportedError
		if asUnsupported(err, &ue) {
			return nil, &Skipped{Name: nameOf(line), Type: scheme, Reason: ue.reason, Detail: ue.detail}
		}
		return nil, &Skipped{Name: nameOf(line), Type: scheme, Reason: ReasonInvalid, Detail: err.Error()}
	}
	n.OriginalTag = n.Tag
	return n, nil
}

type unsupportedError struct{ reason, detail string }

func (e *unsupportedError) Error() string { return e.reason + ": " + e.detail }

func asUnsupported(err error, target **unsupportedError) bool {
	if e, ok := err.(*unsupportedError); ok {
		*target = e
		return true
	}
	return false
}

// nameOf 取链接末尾 #fragment 作为节点名,没有就退回协议名。
func nameOf(line string) string {
	if i := strings.LastIndex(line, "#"); i >= 0 && i+1 < len(line) {
		if s, err := url.PathUnescape(line[i+1:]); err == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// parsed 是拆开的 URI:Go 的 url.Parse 对「userinfo 里有 base64 / 特殊字符」「主机是 IPv6」的容忍度不够,
// 所以分享链接统一用自己的轻量拆分。
type parsed struct {
	user, pass string
	hasPass    bool
	host       string
	port       string // 原样,可能为空或带多端口写法
	query      url.Values
	fragment   string
}

func splitURI(raw string) (*parsed, error) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return nil, fmt.Errorf("不是链接")
	}
	rest := raw[i+3:]
	p := &parsed{}
	if j := strings.Index(rest, "#"); j >= 0 {
		p.fragment, _ = url.PathUnescape(rest[j+1:])
		rest = rest[:j]
	}
	if j := strings.Index(rest, "?"); j >= 0 {
		q, _ := url.ParseQuery(rest[j+1:])
		p.query = q
		rest = rest[:j]
	} else {
		p.query = url.Values{}
	}
	rest = strings.TrimRight(rest, "/")
	if j := strings.Index(rest, "/"); j >= 0 { // 路径部分(如 /?...)忽略
		rest = rest[:j]
	}
	auth := rest
	if j := strings.LastIndex(rest, "@"); j >= 0 {
		ui := rest[:j]
		auth = rest[j+1:]
		if k := strings.Index(ui, ":"); k >= 0 {
			p.user, _ = url.PathUnescape(ui[:k])
			p.pass, _ = url.PathUnescape(ui[k+1:])
			p.hasPass = true
		} else {
			p.user, _ = url.PathUnescape(ui)
		}
	}
	host, port := auth, ""
	if strings.HasPrefix(auth, "[") { // IPv6
		k := strings.Index(auth, "]")
		if k < 0 {
			return nil, fmt.Errorf("IPv6 地址缺少 ]")
		}
		host = auth[1:k]
		if len(auth) > k+1 {
			if auth[k+1] != ':' {
				return nil, fmt.Errorf("地址格式不对")
			}
			port = auth[k+2:]
		}
	} else if k := strings.LastIndex(auth, ":"); k >= 0 {
		host, port = auth[:k], auth[k+1:]
	}
	p.host = strings.TrimSpace(host)
	p.port = strings.TrimSpace(port)
	return p, nil
}

func (p *parsed) q(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(p.query.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("端口不合法:%q", s)
	}
	return n, nil
}

// endpoint 校验主机和端口,返回 server / server_port。
func (p *parsed) endpoint(defaultPort int) (string, int, error) {
	if p.host == "" {
		return "", 0, fmt.Errorf("缺少服务器地址")
	}
	if p.port == "" {
		if defaultPort == 0 {
			return "", 0, fmt.Errorf("缺少端口")
		}
		return p.host, defaultPort, nil
	}
	port, err := parsePort(p.port)
	if err != nil {
		return "", 0, err
	}
	return p.host, port, nil
}

func newNode(typ, tag, server string, port int, fields map[string]any) *Node {
	if strings.TrimSpace(tag) == "" {
		tag = server + ":" + strconv.Itoa(port)
	}
	return &Node{Tag: strings.TrimSpace(tag), Type: typ, Server: server, ServerPort: port, Fields: fields}
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func splitList(s string) []any {
	var out []any
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// tlsFrom 按 v2rayN / Xray 的 security / sni / alpn / fp / pbk / sid 约定生成 tls 字段。
func tlsFrom(p *parsed, security string, defaultOn bool, fallbackSNI string) map[string]any {
	switch strings.ToLower(security) {
	case "tls", "reality":
	case "none":
		return nil
	case "":
		if !defaultOn {
			return nil
		}
		security = "tls"
	default:
		if !defaultOn {
			return nil
		}
		security = "tls"
	}
	tls := map[string]any{"enabled": true}
	if sni := p.q("sni", "peer", "serverName"); sni != "" {
		tls["server_name"] = sni
	} else if fallbackSNI != "" {
		tls["server_name"] = fallbackSNI
	}
	if a := p.q("alpn"); a != "" {
		tls["alpn"] = splitList(a)
	}
	if fp := p.q("fp", "fingerprint"); fp != "" && fp != "none" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	if strings.EqualFold(security, "reality") {
		r := map[string]any{"enabled": true}
		if pbk := p.q("pbk", "publicKey"); pbk != "" {
			r["public_key"] = pbk
		}
		if sid := p.q("sid", "shortId"); sid != "" {
			r["short_id"] = sid
		}
		tls["reality"] = r
	}
	return tls
}

// transportFrom 按 type / host / path / serviceName 约定生成 transport 字段。tcp 返回 nil。
func transportFrom(netType, host, path, serviceName string) (map[string]any, error) {
	switch strings.ToLower(netType) {
	case "", "tcp", "raw":
		return nil, nil
	case "ws", "websocket":
		t := map[string]any{"type": "ws"}
		if path != "" {
			t["path"] = path
		}
		if host != "" {
			t["headers"] = map[string]any{"Host": host}
		}
		return t, nil
	case "grpc", "gun":
		t := map[string]any{"type": "grpc"}
		if serviceName == "" {
			serviceName = path
		}
		if serviceName != "" {
			t["service_name"] = serviceName
		}
		return t, nil
	case "h2", "http":
		t := map[string]any{"type": "http"}
		if host != "" {
			t["host"] = splitList(host)
		}
		if path != "" {
			t["path"] = path
		}
		return t, nil
	case "httpupgrade":
		t := map[string]any{"type": "httpupgrade"}
		if host != "" {
			t["host"] = host
		}
		if path != "" {
			t["path"] = path
		}
		return t, nil
	}
	return nil, &unsupportedError{reason: ReasonUnsupportedTransport, detail: netType}
}

func parseVLESS(raw string) (*Node, error) {
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	if p.user == "" {
		return nil, fmt.Errorf("缺少 uuid")
	}
	server, port, err := p.endpoint(0)
	if err != nil {
		return nil, err
	}
	f := map[string]any{"uuid": p.user}
	if flow := p.q("flow"); flow != "" {
		f["flow"] = flow
	}
	if tls := tlsFrom(p, p.q("security"), false, ""); tls != nil {
		f["tls"] = tls
	}
	tr, err := transportFrom(p.q("type"), p.q("host"), p.q("path"), p.q("serviceName", "service_name"))
	if err != nil {
		return nil, err
	}
	if tr != nil {
		f["transport"] = tr
	}
	return newNode("vless", p.fragment, server, port, f), nil
}

func parseTrojan(raw string) (*Node, error) {
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	pw := p.user
	if p.hasPass {
		pw = p.user + ":" + p.pass
	}
	if pw == "" {
		return nil, fmt.Errorf("缺少密码")
	}
	server, port, err := p.endpoint(443)
	if err != nil {
		return nil, err
	}
	f := map[string]any{"password": pw}
	if tls := tlsFrom(p, p.q("security"), true, ""); tls != nil {
		f["tls"] = tls
	}
	tr, err := transportFrom(p.q("type"), p.q("host"), p.q("path"), p.q("serviceName", "service_name"))
	if err != nil {
		return nil, err
	}
	if tr != nil {
		f["transport"] = tr
	}
	return newNode("trojan", p.fragment, server, port, f), nil
}

func parseAnyTLS(raw string) (*Node, error) {
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	pw := p.user
	if p.hasPass {
		pw = p.user + ":" + p.pass
	}
	if pw == "" {
		return nil, fmt.Errorf("缺少密码")
	}
	server, port, err := p.endpoint(443)
	if err != nil {
		return nil, err
	}
	f := map[string]any{"password": pw}
	if tls := tlsFrom(p, "tls", true, ""); tls != nil {
		f["tls"] = tls
	}
	return newNode("anytls", p.fragment, server, port, f), nil
}

func parseHysteria2(raw string) (*Node, error) {
	// 主机后可以直接写多端口:hy2://pw@host:443,5000-6000/?...
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	pw := p.user
	if p.hasPass {
		pw = p.user + ":" + p.pass
	}
	if pw == "" {
		return nil, fmt.Errorf("缺少密码")
	}
	hop := p.q("mport")
	if strings.ContainsAny(p.port, ",-") {
		parts := strings.SplitN(p.port, ",", 2)
		first := parts[0]
		if strings.Contains(first, "-") { // 只有一段范围:取整段做跳跃端口,起点做主端口
			hop = firstNonEmpty(hop, p.port)
			first = strings.SplitN(first, "-", 2)[0]
		} else if len(parts) == 2 {
			hop = firstNonEmpty(hop, parts[1])
		}
		p.port = first
	}
	server, port, err := p.endpoint(443)
	if err != nil {
		return nil, err
	}
	f := map[string]any{"password": pw}
	// hysteria2 走 QUIC:内核不支持 uTLS,fp 参数忽略(emit 也会丢弃)
	tls := map[string]any{"enabled": true}
	if sni := p.q("sni", "peer"); sni != "" {
		tls["server_name"] = sni
	}
	if a := p.q("alpn"); a != "" {
		tls["alpn"] = splitList(a)
	}
	f["tls"] = tls
	if o := p.q("obfs"); o != "" && !strings.EqualFold(o, "none") {
		obfs := map[string]any{"type": o}
		if op := p.q("obfs-password", "obfs_password"); op != "" {
			obfs["password"] = op
		}
		f["obfs"] = obfs
	}
	if hop != "" {
		f["server_ports"] = hop
		if iv := p.q("hop-interval", "hop_interval"); iv != "" {
			f["hop_interval"] = iv
		}
	}
	return newNode("hysteria2", p.fragment, server, port, f), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func parseTUIC(raw string) (*Node, error) {
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	if p.user == "" {
		return nil, fmt.Errorf("缺少 uuid")
	}
	server, port, err := p.endpoint(0)
	if err != nil {
		return nil, err
	}
	f := map[string]any{"uuid": p.user, "password": p.pass}
	if cc := p.q("congestion_control", "congestion-control"); cc != "" {
		f["congestion_control"] = cc
	}
	tls := map[string]any{"enabled": true}
	if sni := p.q("sni"); sni != "" {
		tls["server_name"] = sni
	}
	if a := p.q("alpn"); a != "" {
		tls["alpn"] = splitList(a)
	}
	f["tls"] = tls
	return newNode("tuic", p.fragment, server, port, f), nil
}

func parseSocks(raw string) (*Node, error) {
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	user, pass := p.user, p.pass
	// 常见写法:socks://BASE64(user:pass)@host:port
	if !p.hasPass && user != "" {
		if d, ok := decodeBase64(user); ok && strings.Contains(d, ":") {
			kv := strings.SplitN(d, ":", 2)
			user, pass = kv[0], kv[1]
		}
	}
	server, port, err := p.endpoint(1080)
	if err != nil {
		return nil, err
	}
	f := map[string]any{}
	if user != "" {
		f["username"] = user
	}
	if pass != "" {
		f["password"] = pass
	}
	return newNode("socks", p.fragment, server, port, f), nil
}

func parseHTTPProxy(raw string, secure bool) (*Node, error) {
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	def := 80
	if secure {
		def = 443
	}
	server, port, err := p.endpoint(def)
	if err != nil {
		return nil, err
	}
	f := map[string]any{}
	if p.user != "" {
		f["username"] = p.user
	}
	if p.pass != "" {
		f["password"] = p.pass
	}
	if secure {
		tls := map[string]any{"enabled": true}
		if sni := p.q("sni"); sni != "" {
			tls["server_name"] = sni
		}
		f["tls"] = tls
	}
	return newNode("http", p.fragment, server, port, f), nil
}

// parseSS 支持 SIP002(ss://BASE64(method:pw)@host:port 与 ss://method:pw@host:port,可带 ?plugin=)
// 以及旧写法 ss://BASE64(method:pw@host:port)。
func parseSS(raw string) (*Node, error) {
	body := raw[len("ss://"):]
	frag := ""
	if j := strings.Index(body, "#"); j >= 0 {
		frag, _ = url.PathUnescape(body[j+1:])
		body = body[:j]
	}
	// 旧写法:没有 @,整段是 base64
	if !strings.Contains(body, "@") {
		q := ""
		if j := strings.Index(body, "?"); j >= 0 {
			q, body = body[j+1:], body[:j]
		}
		d, ok := decodeBase64(strings.TrimRight(body, "/"))
		if !ok {
			return nil, fmt.Errorf("无法解码")
		}
		raw = "ss://" + d
		if q != "" {
			raw += "?" + q
		}
	} else {
		raw = "ss://" + body
	}
	p, err := splitURI(raw)
	if err != nil {
		return nil, err
	}
	method, pw := p.user, p.pass
	if !p.hasPass { // userinfo 是 base64(method:password)
		d, ok := decodeBase64(p.user)
		if !ok || !strings.Contains(d, ":") {
			return nil, fmt.Errorf("缺少加密方式或密码")
		}
		kv := strings.SplitN(d, ":", 2)
		method, pw = kv[0], kv[1]
	}
	if method == "" {
		return nil, fmt.Errorf("缺少加密方式")
	}
	server, port, err := p.endpoint(0)
	if err != nil {
		return nil, err
	}
	f := map[string]any{"method": method, "password": pw}
	if pl := p.q("plugin"); pl != "" {
		name, opts := pl, ""
		if j := strings.Index(pl, ";"); j >= 0 {
			name, opts = pl[:j], pl[j+1:]
		}
		switch name {
		case "obfs-local", "simple-obfs", "v2ray-plugin":
			if name == "simple-obfs" {
				name = "obfs-local"
			}
			f["plugin"] = name
			if opts != "" {
				f["plugin_opts"] = opts
			}
		default:
			return nil, &unsupportedError{reason: ReasonUnsupportedPlugin, detail: name}
		}
	}
	return newNode("shadowsocks", frag, server, port, f), nil
}

// parseVMess 解析 v2rayN 的 vmess://BASE64(JSON)。
func parseVMess(raw string) (*Node, error) {
	body := raw[len("vmess://"):]
	if j := strings.Index(body, "#"); j >= 0 {
		body = body[:j]
	}
	d, ok := decodeBase64(strings.TrimRight(body, "/"))
	if !ok {
		return nil, fmt.Errorf("无法解码")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(d), &m); err != nil {
		return nil, fmt.Errorf("不是合法的 JSON:%v", err)
	}
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return strings.TrimSpace(v)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		return ""
	}
	server := str("add")
	if server == "" {
		return nil, fmt.Errorf("缺少服务器地址")
	}
	port, err := parsePort(str("port"))
	if err != nil {
		return nil, err
	}
	id := str("id")
	if id == "" {
		return nil, fmt.Errorf("缺少 uuid")
	}
	alter := 0
	if a := str("aid"); a != "" {
		alter, _ = strconv.Atoi(a)
	}
	f := map[string]any{"uuid": id, "alter_id": alter}
	if sc := str("scy"); sc != "" {
		f["security"] = sc
	}
	tr, err := transportFrom(str("net"), str("host"), str("path"), "")
	if err != nil {
		return nil, err
	}
	if tr != nil {
		f["transport"] = tr
	}
	if t := strings.ToLower(str("tls")); t == "tls" || t == "1" || t == "true" {
		tls := map[string]any{"enabled": true}
		sni := str("sni")
		if sni == "" && str("net") != "grpc" {
			sni = str("host")
		}
		if sni != "" {
			tls["server_name"] = sni
		}
		if a := str("alpn"); a != "" {
			tls["alpn"] = splitList(a)
		}
		if fp := str("fp"); fp != "" && fp != "none" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		f["tls"] = tls
	}
	return newNode("vmess", str("ps"), server, port, f), nil
}
