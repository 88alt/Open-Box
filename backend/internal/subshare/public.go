package subshare

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// 公开 /sub/<token> 下发(STATUS 2026-11-04 授权;对齐上游「凭 token 取订阅」的行为,实现自写)。

var (
	// ErrPublicNotFound 统一 404:token 不对、分享停用、没有节点等。
	ErrPublicNotFound = errors.New("not found")
	// ErrPublicLocked 限速。
	ErrPublicLocked = errors.New("locked")
)

// NodesFor 返回某几份订阅里的启用节点(调用方用订阅服务实现)。
type NodesFor func(ctx context.Context, subscriptionIDs []string) ([]sub.Node, *sub.Usage, error)

// Public 是对外下发服务。
type Public struct {
	Shares *Service
	Nodes  NodesFor
	Now    func() time.Time
	// 限速与缓存
	mu       sync.Mutex
	fails    map[string]failEntry
	cache    map[string]cacheEntry
	inflight int
}

type failEntry struct {
	n     int
	until time.Time
}

type cacheEntry struct {
	body     []byte
	etag     string
	userinfo string
	at       time.Time
	key      string
}

func (p *Public) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func tokenHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func validPublicToken(tok string) bool {
	if len(tok) < minTokenLen || len(tok) > maxTokenLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (p *Public) findByToken(ctx context.Context, tok string) (Share, error) {
	if !validPublicToken(tok) {
		return Share{}, ErrPublicNotFound
	}
	list, err := p.Shares.List(ctx)
	if err != nil {
		return Share{}, err
	}
	want := tokenHash(tok)
	for _, sh := range list {
		sum := sha256.Sum256([]byte(sh.Token))
		if hex.EncodeToString(sum[:]) == want {
			if !sh.Enabled {
				return Share{}, ErrPublicNotFound
			}
			return sh, nil
		}
	}
	return Share{}, ErrPublicNotFound
}

func (p *Public) rateOK(ip string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fails == nil {
		p.fails = map[string]failEntry{}
	}
	e := p.fails[ip]
	if e.until.After(p.now()) {
		return ErrPublicLocked
	}
	return nil
}

func (p *Public) rateFail(ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fails == nil {
		p.fails = map[string]failEntry{}
	}
	e := p.fails[ip]
	e.n++
	// 10 次后锁 1 分钟起
	if e.n >= 10 {
		d := time.Minute
		for i := 10; i < e.n && d < 15*time.Minute; i++ {
			d *= 2
		}
		if d > 15*time.Minute {
			d = 15 * time.Minute
		}
		e.until = p.now().Add(d)
	}
	p.fails[ip] = e
}

func (p *Public) rateOK2(ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.fails, ip)
}

// Format 解析 ?format= 与 User-Agent。
func DetectFormat(format, ua string) string {
	f := strings.ToLower(strings.TrimSpace(format))
	switch f {
	case "singbox", "sing-box", "sfa", "sfm", "sfi":
		return "singbox"
	case "clash", "clashmeta", "mihomo":
		return "clash"
	case "base64", "v2ray", "links", "":
		if f == "" {
			ua = strings.ToLower(ua)
			if strings.Contains(ua, "clash") || strings.Contains(ua, "mihomo") {
				return "clash"
			}
			if strings.Contains(ua, "sing-box") || strings.Contains(ua, "sfa") || strings.Contains(ua, "sfm") {
				return "singbox"
			}
		}
		return "base64"
	default:
		return "base64"
	}
}

// Result 是一次公开下发的结果。
type Result struct {
	Body        []byte
	ContentType string
	ETag        string
	Userinfo    string
}

// Serve 生成订阅正文。format 已是 DetectFormat 的结果。
func (p *Public) Serve(ctx context.Context, token, format, ip string) (Result, error) {
	if err := p.rateOK(ip); err != nil {
		return Result{}, err
	}
	sh, err := p.findByToken(ctx, token)
	if err != nil {
		p.rateFail(ip)
		return Result{}, ErrPublicNotFound
	}
	if p.Nodes == nil {
		p.rateFail(ip)
		return Result{}, ErrPublicNotFound
	}
	nodes, usage, err := p.Nodes(ctx, sh.SubscriptionIDs)
	if err != nil || len(nodes) == 0 {
		p.rateFail(ip)
		return Result{}, ErrPublicNotFound
	}
	p.rateOK2(ip)

	cacheKey := fmt.Sprintf("%s|%s|%d", sh.ID, format, sh.UpdatedAt)
	for _, n := range nodes {
		cacheKey += "|" + n.Tag
	}

	p.mu.Lock()
	if p.cache != nil {
		if c, ok := p.cache[sh.ID+"|"+format]; ok && c.key == cacheKey {
			out := Result{Body: c.body, ETag: c.etag, Userinfo: c.userinfo, ContentType: ctype(format)}
			p.mu.Unlock()
			return out, nil
		}
	}
	if p.inflight >= 2 {
		p.mu.Unlock()
		// 简单等待
		time.Sleep(50 * time.Millisecond)
		p.mu.Lock()
	}
	p.inflight++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inflight--
		p.mu.Unlock()
	}()

	var body []byte
	switch format {
	case "singbox":
		body, err = buildSingBox(nodes)
	case "clash":
		body, err = buildClash(nodes)
	default:
		body, err = buildBase64(nodes)
	}
	if err != nil {
		return Result{}, ErrPublicNotFound
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	ui := formatUserinfo(usage)
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]cacheEntry{}
	}
	p.cache[sh.ID+"|"+format] = cacheEntry{body: body, etag: etag, userinfo: ui, key: cacheKey, at: p.now()}
	p.mu.Unlock()
	return Result{Body: body, ContentType: ctype(format), ETag: etag, Userinfo: ui}, nil
}

func ctype(format string) string {
	switch format {
	case "singbox":
		return "application/json; charset=utf-8"
	case "clash":
		return "text/yaml; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

func formatUserinfo(u *sub.Usage) string {
	if u == nil {
		return ""
	}
	parts := []string{}
	if u.Upload != nil {
		parts = append(parts, fmt.Sprintf("upload=%d", *u.Upload))
	}
	if u.Download != nil {
		parts = append(parts, fmt.Sprintf("download=%d", *u.Download))
	}
	if u.Total != nil {
		parts = append(parts, fmt.Sprintf("total=%d", *u.Total))
	}
	if u.Expire != nil {
		parts = append(parts, fmt.Sprintf("expire=%d", *u.Expire))
	}
	return strings.Join(parts, "; ")
}

func buildSingBox(nodes []sub.Node) ([]byte, error) {
	outs := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		o := map[string]any{"tag": n.Tag, "type": n.Type, "server": n.Server, "server_port": n.ServerPort}
		for k, v := range n.Fields {
			o[k] = v
		}
		outs = append(outs, o)
	}
	return json.Marshal(map[string]any{"outbounds": outs})
}

func buildBase64(nodes []sub.Node) ([]byte, error) {
	var lines []string
	for _, n := range nodes {
		if l := encodeLink(n); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		// 退回:把 sing-box 出站 JSON 做 base64,客户端至少能拿到内容
		b, err := buildSingBox(nodes)
		if err != nil {
			return nil, err
		}
		return []byte(base64.StdEncoding.EncodeToString(b)), nil
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))), nil
}

func buildClash(nodes []sub.Node) ([]byte, error) {
	// 最小 YAML:proxies 列表(字段名尽量通用)
	var b strings.Builder
	b.WriteString("proxies:\n")
	for _, n := range nodes {
		b.WriteString("  - name: ")
		b.WriteString(yamlQuote(n.Tag))
		b.WriteString("\n    type: ")
		b.WriteString(clashType(n.Type))
		b.WriteString("\n    server: ")
		b.WriteString(yamlQuote(n.Server))
		b.WriteString(fmt.Sprintf("\n    port: %d\n", n.ServerPort))
		if pw, ok := n.Fields["password"].(string); ok {
			b.WriteString("    password: ")
			b.WriteString(yamlQuote(pw))
			b.WriteString("\n")
		}
		if method, ok := n.Fields["method"].(string); ok {
			b.WriteString("    cipher: ")
			b.WriteString(yamlQuote(method))
			b.WriteString("\n")
		}
		if uuid, ok := n.Fields["uuid"].(string); ok {
			b.WriteString("    uuid: ")
			b.WriteString(yamlQuote(uuid))
			b.WriteString("\n")
		}
	}
	return []byte(b.String()), nil
}

func clashType(t string) string {
	switch t {
	case "shadowsocks":
		return "ss"
	default:
		return t
	}
}

func yamlQuote(s string) string {
	if s == "" || strings.ContainsAny(s, ":#{}[],&*?|-<>=!%@`\"'") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func encodeLink(n sub.Node) string {
	switch n.Type {
	case "shadowsocks", "ss":
		method, _ := n.Fields["method"].(string)
		pw, _ := n.Fields["password"].(string)
		if method == "" || pw == "" {
			return ""
		}
		userinfo := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(method + ":" + pw))
		name := urlQueryEscape(n.Tag)
		return fmt.Sprintf("ss://%s@%s:%d#%s", userinfo, n.Server, n.ServerPort, name)
	case "trojan":
		pw, _ := n.Fields["password"].(string)
		if pw == "" {
			return ""
		}
		return fmt.Sprintf("trojan://%s@%s:%d#%s", urlQueryEscape(pw), n.Server, n.ServerPort, urlQueryEscape(n.Tag))
	case "vless":
		uuid, _ := n.Fields["uuid"].(string)
		if uuid == "" {
			return ""
		}
		return fmt.Sprintf("vless://%s@%s:%d?encryption=none#%s", uuid, n.Server, n.ServerPort, urlQueryEscape(n.Tag))
	default:
		return ""
	}
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// WriteHTTP 写公开响应。
func WriteHTTP(w http.ResponseWriter, r *http.Request, res Result) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", res.ContentType)
	if res.ETag != "" {
		w.Header().Set("ETag", res.ETag)
		if match := r.Header.Get("If-None-Match"); match != "" && match == res.ETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if res.Userinfo != "" {
		w.Header().Set("Subscription-Userinfo", res.Userinfo)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Body)
}
