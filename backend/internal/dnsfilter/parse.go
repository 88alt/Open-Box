package dnsfilter

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// 解析器是自己设计的,依据公开的名单语法说明(hosts 文件格式、一行一个域名、
// AdGuard 的 DNS 过滤规则语法),没有参照上游。只认 DNS 层能做的那部分:
//
//	0.0.0.0 ads.example.com        hosts 行:拦截这些域名(精确匹配)
//	ads.example.com                纯域名行:拦截(精确匹配)
//	||example.org^                 拦截 example.org 及其所有子域名
//	|example.org^                  拦截 example.org(精确匹配)
//	@@||example.org^               放行(同样的写法前面加 @@)
//	/regex/                        拦截匹配正则的域名(要能被 Go 的 regexp 编译)
//	规则末尾 $important            优先级高于普通放行
//
// 其余(带 dnsrewrite / client / dnstype 等修饰符的规则、带通配符或路径的规则、
// 页面元素隐藏规则)不会被误当成别的意思:一律计入「不支持」。
// 注释行(! 或 # 开头)和 [Adblock Plus 2.0] 这类头部行直接略过,不计数。

// 条目类型,取值与前端 ruleTypeLabelKey 认得的 sing-box 规则字段名一致。
const (
	TypeDomain       = "domain"
	TypeDomainSuffix = "domain_suffix"
	TypeDomainRegex  = "domain_regex"
)

// 动作。
const (
	ActionBlock = "block"
	ActionAllow = "allow"
)

const (
	// MaxEntries 是一份名单解析出的条目上限,超了报错而不是截断(截断会让名单悄悄少一半)。
	MaxEntries = 200000
	// MaxUnsupportedExamples 是「不支持」的样例条数。
	MaxUnsupportedExamples = 5
	maxRuleLine            = 1024 // 超长的行直接算不支持
	maxRegexLen            = 256
	maxExampleLen          = 120
)

// ErrNoRules 在名单里一条可用规则都没有时返回。
var ErrNoRules = errors.New("名单里没有可用的规则")

// ErrTooMany 在条目数超过 MaxEntries 时返回。
var ErrTooMany = fmt.Errorf("名单条目超过 %d 条", MaxEntries)

// ErrBinary 在内容看起来是二进制时返回。
var ErrBinary = errors.New("这不是文本名单(像是二进制文件)")

// Entry 是一条解析出的规则,字段与前端 DnsFilterPreviewEntry 一一对应。
type Entry struct {
	Type        string `json:"type"`
	Value       string `json:"value"`
	Rule        string `json:"rule"`
	Action      string `json:"action"`
	Important   bool   `json:"important"`
	Conditional bool   `json:"conditional"`
}

// Parsed 是一份名单的解析结果。
type Parsed struct {
	Entries             []Entry
	Unsupported         int
	UnsupportedExamples []string
}

// RuleCount 是认出来的规则行总数(支持的加不支持的,重复的按一条算)。
func (p *Parsed) RuleCount() int { return len(p.Entries) + p.Unsupported }

type entryKey struct {
	typ, value, action string
	important          bool
}

// ParseList 解析名单文本。
func ParseList(body []byte) (*Parsed, error) {
	if looksBinary(body) {
		return nil, ErrBinary
	}
	out := &Parsed{Entries: []Entry{}, UnsupportedExamples: []string{}}
	seen := map[entryKey]bool{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), maxRuleLine*4)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" || skipLine(line) {
			continue
		}
		ents, ok := parseLine(line)
		if !ok {
			out.Unsupported++
			if len(out.UnsupportedExamples) < MaxUnsupportedExamples {
				out.UnsupportedExamples = append(out.UnsupportedExamples, clip(line, maxExampleLen))
			}
			continue
		}
		for _, e := range ents {
			k := entryKey{e.Type, e.Value, e.Action, e.Important}
			if seen[k] {
				continue
			}
			if len(out.Entries) >= MaxEntries {
				return nil, ErrTooMany
			}
			seen[k] = true
			out.Entries = append(out.Entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		// 一行超过缓冲上限:整份当作异常内容,不去猜
		return nil, errors.New("名单里有超长的行,不像是文本名单")
	}
	if len(out.Entries) == 0 && out.Unsupported == 0 {
		return nil, ErrNoRules
	}
	return out, nil
}

// skipLine 判断注释行和头部行。「#」开头的单行注释要排除页面元素规则(含 ##、#@#、#?#、#$#)——
// 那些不是注释,是 DNS 用不上的规则,走「不支持」。
func skipLine(l string) bool {
	if l[0] == '!' {
		return true
	}
	if l[0] == '[' && strings.HasSuffix(l, "]") {
		return true
	}
	if l[0] == '#' {
		return !strings.HasPrefix(l, "##") && !strings.HasPrefix(l, "#@#") && !strings.HasPrefix(l, "#?#") && !strings.HasPrefix(l, "#$#")
	}
	return false
}

func parseLine(line string) ([]Entry, bool) {
	if len(line) > maxRuleLine {
		return nil, false
	}
	// hosts 行:第一段是 IP
	if f := strings.Fields(line); len(f) >= 2 {
		if _, err := netip.ParseAddr(f[0]); err == nil {
			return parseHosts(line, f)
		}
	}
	allow := false
	body := line
	if strings.HasPrefix(body, "@@") {
		allow = true
		body = body[2:]
	}
	important := false
	if i := strings.LastIndexByte(body, '$'); i >= 0 && !strings.HasPrefix(body, "/") {
		mods := strings.Split(body[i+1:], ",")
		for _, m := range mods {
			if strings.EqualFold(strings.TrimSpace(m), "important") {
				important = true
				continue
			}
			return nil, false // 其它修饰符一律不支持
		}
		body = body[:i]
	}
	action := ActionBlock
	if allow {
		action = ActionAllow
	}
	switch {
	case strings.HasPrefix(body, "||"):
		d, ok := sepDomain(body[2:])
		if !ok {
			return nil, false
		}
		return []Entry{{TypeDomainSuffix, d, line, action, important, false}}, true
	case strings.HasPrefix(body, "|"):
		d, ok := sepDomain(body[1:])
		if !ok {
			return nil, false
		}
		return []Entry{{TypeDomain, d, line, action, important, false}}, true
	case len(body) >= 2 && body[0] == '/' && body[len(body)-1] == '/':
		re := body[1 : len(body)-1]
		if re == "" || len(re) > maxRegexLen {
			return nil, false
		}
		if _, err := regexp.Compile(re); err != nil {
			return nil, false
		}
		return []Entry{{TypeDomainRegex, re, line, action, important, false}}, true
	}
	// 纯域名行(不带 @@ 和修饰符)
	if !allow && !important && body == line {
		if d, err := plainDomain(body); err == nil {
			return []Entry{{TypeDomain, d, line, ActionBlock, false, false}}, true
		}
	}
	return nil, false
}

// sepDomain 取 "example.org^" 里的域名:必须以 ^ 结尾、中间是合法域名(不带通配符、路径、端口)。
func sepDomain(s string) (string, bool) {
	if !strings.HasSuffix(s, "^") {
		return "", false
	}
	d, err := plainDomain(strings.TrimSuffix(s, "^"))
	if err != nil {
		return "", false
	}
	return d, true
}

func parseHosts(line string, f []string) ([]Entry, bool) {
	var out []Entry
	sawLocal := false
	for _, name := range f[1:] {
		if strings.HasPrefix(name, "#") {
			break // 行内注释
		}
		if isLocalName(strings.ToLower(name)) {
			sawLocal = true
			continue
		}
		d, err := plainDomain(name)
		if err != nil {
			continue
		}
		out = append(out, Entry{TypeDomain, d, line, ActionBlock, false, false})
	}
	if len(out) == 0 {
		// 只有 localhost 之类的本机条目:是 hosts 文件的固定开头,不算规则也不算不支持
		return nil, sawLocal
	}
	return out, true
}

// plainDomain 规整一个不带通配符的域名:小写、去末尾的点,段只含字母数字连字符下划线,
// 至少有一个点(单段的名字如 localhost 不当域名规则),不是 IP。
func plainDomain(s string) (string, error) {
	if strings.ContainsAny(s, "*") {
		return "", errors.New("带通配符")
	}
	d, err := NormalizeDomain(s)
	if err != nil {
		return "", err
	}
	if !strings.Contains(d, ".") {
		return "", errors.New("只有一段")
	}
	return d, nil
}

func isLocalName(d string) bool {
	switch d {
	case "localhost", "localhost.localdomain", "local", "broadcasthost", "ip6-localhost", "ip6-loopback",
		"ip6-localnet", "ip6-mcastprefix", "ip6-allnodes", "ip6-allrouters", "ip6-allhosts", "0.0.0.0":
		return true
	}
	return false
}

func looksBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	return bytes.IndexByte(b, 0) >= 0
}
