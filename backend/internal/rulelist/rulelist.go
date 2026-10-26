// Package rulelist 拉取并解析「规则集链接」指向的文本名单(M1)。
//
// 认的写法(都是公开的通用约定,没有参考上游实现):
//   - Clash 经典名单:`DOMAIN,x` / `DOMAIN-SUFFIX,x` / `DOMAIN-KEYWORD,x` / `DOMAIN-REGEX,x` / `IP-CIDR,x` / `IP-CIDR6,x`
//     (后面的 no-resolve 等参数忽略),`#` `;` `//` 开头是注释;
//   - YAML payload:`payload:` 下每行 `- '...'`,内容同上,或下面两种裸写法;
//   - 裸写法:`+.example.com` / `.example.com` / `*.example.com` = 域名后缀,`example.com` = 完整域名,
//     `1.2.3.0/24`、`1.2.3.4`、IPv6 = IP 段(单个地址补 /32 或 /128)。
//
// 二进制名单(mihomo .mrs、sing-box .srs)不支持:解压 / 解码需要额外依赖,先明确报错。
package rulelist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxBodyBytes 是名单内容上限(路由器内存小)。
	MaxBodyBytes = 8 << 20
	// MaxEntries 是解析出的条目上限。
	MaxEntries   = 200000
	fetchTimeout = 30 * time.Second
	userAgent    = "Open-Box/rulelist"
)

// 条目类型,与前端 helper/ruleType.ts 一致。
const (
	TypeDomain        = "domain"
	TypeDomainSuffix  = "domain_suffix"
	TypeDomainKeyword = "domain_keyword"
	TypeDomainRegex   = "domain_regex"
	TypeIPCIDR        = "ip_cidr"
)

// Entry 是名单里的一条规则。
type Entry struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Result 是解析结果。Skipped 是认不得而忽略的行数(进程名、GEOIP 等)。
type Result struct {
	Entries []Entry
	Skipped int
}

// 业务错误。
var (
	ErrBinary = errors.New("这是二进制名单(.mrs / .srs),暂不支持;请换成文本名单(Clash 的 .list / .yaml 或一行一条的域名 / IP 列表)")
	ErrEmpty  = errors.New("名单里没有可用的规则")
)

// FetchError 是拉取失败(502)。
type FetchError struct{ Msg string }

func (e *FetchError) Error() string { return e.Msg }

func fetchErr(f string, a ...any) error { return &FetchError{Msg: fmt.Sprintf(f, a...)} }

// InputError 是地址有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Fetch 拉取名单文本。只收 http / https;超过 MaxBodyBytes 报错而不是截断。
func Fetch(ctx context.Context, client *http.Client, raw string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, &InputError{Msg: "规则集链接必须是 http:// 或 https:// 开头的有效地址"}
	}
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &InputError{Msg: "规则集链接无效"}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fetchErr("拉取规则集已取消或超时(%s)", u.Hostname())
		}
		return nil, fetchErr("无法连接 %s", u.Hostname())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fetchErr("%s 返回 HTTP %d", u.Hostname(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, fetchErr("读取规则集失败(%s)", u.Hostname())
	}
	if len(body) > MaxBodyBytes {
		return nil, fetchErr("规则集超过 %d MiB", MaxBodyBytes>>20)
	}
	return body, nil
}

func looksBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return !utf8.Valid(b) && utf8.RuneCount(b) < len(b)/2
}

// Parse 解析名单文本,条目去重并保持首次出现的顺序。
func Parse(body []byte) (*Result, error) {
	if looksBinary(body) {
		return nil, ErrBinary
	}
	text := strings.TrimPrefix(string(body), "\ufeff")
	res := &Result{Entries: []Entry{}}
	seen := map[Entry]bool{}
	add := func(t, v string) {
		e := Entry{t, v}
		if v == "" || seen[e] {
			return
		}
		seen[e] = true
		res.Entries = append(res.Entries, e)
	}
	for _, line := range strings.Split(text, "\n") {
		if len(res.Entries) >= MaxEntries {
			return nil, fmt.Errorf("名单条目超过 %d 条", MaxEntries)
		}
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") || strings.HasPrefix(s, "//") {
			continue
		}
		if strings.HasPrefix(s, "payload:") || s == "rules:" {
			continue
		}
		if strings.HasPrefix(s, "- ") || s == "-" {
			s = strings.TrimSpace(strings.TrimPrefix(s, "-"))
		}
		s = trimQuotes(stripComment(s))
		if s == "" {
			continue
		}
		t, v, ok := parseItem(s)
		if !ok {
			res.Skipped++
			continue
		}
		add(t, v)
	}
	if len(res.Entries) == 0 {
		return nil, ErrEmpty
	}
	return res, nil
}

func stripComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func trimQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func parseItem(s string) (typ, val string, ok bool) {
	if i := strings.IndexByte(s, ','); i >= 0 {
		kind := strings.ToUpper(strings.TrimSpace(s[:i]))
		rest := strings.TrimSpace(s[i+1:])
		if j := strings.IndexByte(rest, ','); j >= 0 && kind != "DOMAIN-REGEX" {
			rest = strings.TrimSpace(rest[:j])
		}
		switch kind {
		case "DOMAIN":
			return domainEntry(TypeDomain, rest)
		case "DOMAIN-SUFFIX":
			return domainEntry(TypeDomainSuffix, rest)
		case "DOMAIN-KEYWORD":
			if rest == "" {
				return "", "", false
			}
			return TypeDomainKeyword, strings.ToLower(rest), true
		case "DOMAIN-REGEX":
			if rest == "" {
				return "", "", false
			}
			return TypeDomainRegex, rest, true
		case "IP-CIDR", "IP-CIDR6":
			return cidrEntry(rest)
		}
		return "", "", false
	}
	// 裸写法
	switch {
	case strings.HasPrefix(s, "+."):
		return domainEntry(TypeDomainSuffix, s[2:])
	case strings.HasPrefix(s, "*."):
		return domainEntry(TypeDomainSuffix, s[2:])
	case strings.HasPrefix(s, "."):
		return domainEntry(TypeDomainSuffix, s[1:])
	}
	if t, v, ok := cidrEntry(s); ok {
		return t, v, true
	}
	return domainEntry(TypeDomain, s)
}

func domainEntry(t, v string) (string, string, bool) {
	v = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(v), "."))
	if !validHost(v) {
		return "", "", false
	}
	return t, v, true
}

// validHost 只做宽松检查(允许 IDN 和下划线):非空、无空白和分隔符、不超长。
func validHost(v string) bool {
	if v == "" || len(v) > 253 || strings.HasPrefix(v, "-") || strings.Contains(v, "..") {
		return false
	}
	for _, r := range v {
		if r <= ' ' || strings.ContainsRune(`/\:,;'"<>@#?*+=()[]{}|^~%&$!`, r) {
			return false
		}
	}
	return true
}

func cidrEntry(v string) (string, string, bool) {
	v = strings.TrimSpace(v)
	if p, err := netip.ParsePrefix(v); err == nil {
		return TypeIPCIDR, p.Masked().String(), true
	}
	if a, err := netip.ParseAddr(v); err == nil {
		a = a.Unmap()
		return TypeIPCIDR, netip.PrefixFrom(a, a.BitLen()).String(), true
	}
	return "", "", false
}

// Filter 按关键词(不区分大小写,匹配条目的值)过滤。q 为空返回全部。
func Filter(entries []Entry, q string) []Entry {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return entries
	}
	out := []Entry{}
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Value), q) {
			out = append(out, e)
		}
	}
	return out
}
