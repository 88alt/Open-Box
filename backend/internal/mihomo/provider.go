package mihomo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 规则集 → Mihomo rule-provider(双内核 3e-3;对应上游 v0.1.462 Node 的 Du / cg)。
//
// 输入是 sing-box 的「源文件」规则集 JSON(`sing-box rule-set decompile` 的产物,也可以是代码里拼的),
// 输出是 Mihomo 的 file 型 provider:yaml 载荷 + 行为(domain / classical / ipcidr)。
// 只认上游认的那几种字段(domain / domain_suffix / domain_keyword / domain_regex 和 ip_cidr),别的字段整体报错,
// 不静默丢弃(Mihomo 表达不了的整体报错,是 STATUS 双内核条目里的边界)。

// 域名类字段 → Mihomo 规则类型(上游 NE)。
var domainFieldTypes = map[string]string{
	"domain":         "DOMAIN",
	"domain_suffix":  "DOMAIN-SUFFIX",
	"domain_keyword": "DOMAIN-KEYWORD",
	"domain_regex":   "DOMAIN-REGEX",
}

// Field 是源文件里一条规则的一个字段。Values 已把「单个值 / 数组」统一成切片(上游 uC)。
type Field struct {
	Key    string
	Values []any
}

// Rule 是一条规则的字段,顺序与源文件一致(上游靠对象键顺序决定 yaml 里条目的顺序)。nil 或空表示「空规则」,会报错。
type Rule []Field

// Source 是 sing-box 源文件规则集:version 必须是 1~4,rules 必须是数组。
type Source struct {
	Version int // 0 = 缺失或不是 1~4 的整数
	Rules   []Rule
	hasRule bool
}

// Strs 方便代码里拼一个字段(值全是字符串)。
func Strs(key string, values ...string) Field {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return Field{Key: key, Values: vs}
}

// NewSource 给代码里拼的规则集用(例如 ob-local-dns、ob-entry-direct)。
func NewSource(version int, rules ...Rule) Source {
	return Source{Version: version, Rules: rules, hasRule: true}
}

// ParseSource 解析 sing-box 源文件规则集 JSON,保留规则里字段的先后顺序。
// 不是对象、不是合法 JSON 都报错;version / rules 的合法性留给 BuildProvider 判断(上游也是在 Du 里判断)。
func ParseSource(raw []byte) (Source, error) {
	var src Source
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return src, errors.New("规则集源文件不是有效的 JSON")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return src, nil // 不是对象:Version 为 0,BuildProvider 报「无效的规则集源文件」
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return Source{}, errors.New("规则集源文件不是有效的 JSON")
		}
		key, _ := kt.(string)
		switch key {
		case "version":
			var v any
			if err := dec.Decode(&v); err != nil {
				return Source{}, errors.New("规则集源文件不是有效的 JSON")
			}
			if n, ok := v.(json.Number); ok {
				if f, err := n.Float64(); err == nil && f >= 1 && f <= 4 && f == float64(int(f)) {
					src.Version = int(f)
				}
			}
		case "rules":
			var items []json.RawMessage
			if err := dec.Decode(&items); err != nil {
				// rules 存在但不是数组:Du 的 Array.isArray 会失败。吞掉这个值,hasRule 保持 false。
				continue
			}
			if items == nil { // "rules": null 不是数组
				continue
			}
			src.hasRule = true
			src.Rules = make([]Rule, 0, len(items))
			for _, it := range items {
				src.Rules = append(src.Rules, parseOrderedRule(it))
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return Source{}, errors.New("规则集源文件不是有效的 JSON")
			}
		}
	}
	return src, nil
}

// parseOrderedRule 读一个 JSON 对象的键值,保持键的顺序。不是对象(字符串、数组、null、数字)返回 nil。
func parseOrderedRule(raw json.RawMessage) Rule {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || trim[0] != '{' {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trim))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var rule Rule
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil
		}
		key, _ := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		f := Field{Key: key}
		if list, ok := v.([]any); ok {
			f.Values = list
		} else {
			f.Values = []any{v}
		}
		// JS 对象里重复键:后者覆盖值、位置保持第一次出现的地方。
		replaced := false
		for i := range rule {
			if rule[i].Key == key {
				rule[i] = f
				replaced = true
				break
			}
		}
		if !replaced {
			rule = append(rule, f)
		}
	}
	return rule
}

var (
	plainDomainItem = regexp.MustCompile(`(?i)^DOMAIN(?:-SUFFIX)?,[a-z0-9_-]+(?:\.[a-z0-9_-]+)*$`)
)

// jsSpace 对应 JS String.prototype.trim 会去掉的字符(比 unicode.IsSpace 多 U+FEFF,少 U+0085)。
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func jsTrimmed(s string) bool { return strings.TrimFunc(s, jsSpace) == s }

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// validCIDR 对应上游:node 的 isIP 认的地址 + 1~3 位十进制前缀 + 不带区域标识 + 只有一个 `/`。
func validCIDR(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	addr, bits := parts[0], parts[1]
	if strings.Contains(addr, "%") {
		return false
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	if len(bits) < 1 || len(bits) > 3 {
		return false
	}
	n := 0
	for _, c := range bits {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	max := 128
	if ip.Is4() {
		max = 32
	}
	return n <= max
}

// Provider 是写进 mihomo.yaml 的 rule-providers 条目(file 型)。
type Provider struct {
	Type     string `json:"type"`
	Behavior string `json:"behavior"`
	Format   string `json:"format"`
	Path     string `json:"path"`
}

// BuildProvider 对应上游 Du:kind 是 "domain" 或 "ipcidr"(由规则集名决定,见 RuleSetKind)。
// 返回 provider 的行为和 yaml 载荷。错误文案与上游一致。
func BuildProvider(src Source, kind string) (behavior, payload string, err error) {
	if src.Version < 1 || src.Version > 4 || !src.hasRule || (kind != "domain" && kind != "ipcidr") {
		return "", "", errors.New("无效的规则集源文件")
	}
	var items []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			items = append(items, s)
		}
	}
	for _, rule := range src.Rules {
		if len(rule) == 0 {
			return "", "", errors.New("无法转换空规则")
		}
		for _, f := range rule {
			if kind == "ipcidr" {
				if f.Key != "ip_cidr" {
					return "", "", fmt.Errorf("无法转换规则字段 %s", f.Key)
				}
			} else if _, ok := domainFieldTypes[f.Key]; !ok {
				return "", "", fmt.Errorf("无法转换规则字段 %s", f.Key)
			}
			for _, v := range f.Values {
				s, ok := v.(string)
				if !ok || s == "" || !jsTrimmed(s) || hasControl(s) {
					return "", "", fmt.Errorf("无效的规则值 %s", f.Key)
				}
				if kind == "ipcidr" {
					if !validCIDR(s) {
						return "", "", errors.New("无效的 IP 网段")
					}
					add(s)
				} else {
					if f.Key != "domain_regex" && strings.Contains(s, ",") {
						return "", "", fmt.Errorf("无效的规则值 %s", f.Key)
					}
					add(domainFieldTypes[f.Key] + "," + s)
				}
			}
		}
	}
	if len(items) == 0 {
		return "", "", errors.New("规则集没有可用条目")
	}
	allPlain := kind == "domain"
	if allPlain {
		for _, it := range items {
			if !plainDomainItem.MatchString(it) {
				allPlain = false
				break
			}
		}
	}
	out := items
	switch {
	case allPlain:
		behavior = "domain"
		out = make([]string, len(items))
		for i, it := range items {
			if strings.HasPrefix(it, "DOMAIN-SUFFIX,") {
				out[i] = "+." + it[len("DOMAIN-SUFFIX,"):]
			} else {
				out[i] = strings.TrimPrefix(it, "DOMAIN,")
			}
		}
	case kind == "domain":
		behavior = "classical"
	default:
		behavior = "ipcidr"
	}
	return behavior, yamlPayload(out), nil
}

// yamlPayload 等价于 yaml.stringify({payload: list}, {lineWidth: 0}):
//
//	payload:
//	  - a
//	  - b
func yamlPayload(list []string) string {
	var b strings.Builder
	b.WriteString("payload:\n")
	for _, s := range list {
		b.WriteString("  - ")
		b.WriteString(yamlScalar(s))
		b.WriteByte('\n')
	}
	return b.String()
}

var (
	// 等价于 yaml 库 plainString 里「不能写成纯文本」的判断(单行、非键、非 flow)。
	yamlUnsafe = regexp.MustCompile("^[\\n\\t ,\\[\\]{}#&*!|>'\"%@`]|^[?-]$|^[?-][ \\t]|[\\n:][ \\t]|[ \\t]\\n|[\\n\\t ]#|[\\n\\t :]$")
	// 核心模式(YAML 1.2)下写成纯文本会被读成非字符串的几类。
	yamlNonString = []*regexp.Regexp{
		regexp.MustCompile(`^(?:~|[Nn]ull|NULL)?$`),
		regexp.MustCompile(`^(?:[Tt]rue|TRUE|[Ff]alse|FALSE)$`),
		regexp.MustCompile(`^[-+]?[0-9]+$`),
		regexp.MustCompile(`^0o[0-7]+$`),
		regexp.MustCompile(`^0x[0-9a-fA-F]+$`),
		regexp.MustCompile(`^(?:[-+]?\.(?:inf|Inf|INF)|\.nan|\.NaN|\.NAN)$`),
		regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)[eE][-+]?[0-9]+$`),
		regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+\.[0-9]*)$`),
	}
)

// yamlScalar 写一个字符串标量(块序列里的条目),口径与 yaml 库默认选项一致:
// 能写纯文本就写纯文本;会被误读(空串、true、123、1.5……)或有特殊字符就加引号:
// 只含 " 用单引号,其余用双引号。含控制字符一律双引号。多行字符串不会出现(BuildProvider 已拒绝控制字符)。
func yamlScalar(s string) string {
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return yamlDouble(s)
		}
	}
	if s == "" || yamlUnsafe.MatchString(s) {
		return yamlQuoted(s)
	}
	for _, re := range yamlNonString {
		if re.MatchString(s) {
			return yamlQuoted(s)
		}
	}
	return s
}

func yamlQuoted(s string) string {
	hasDouble := strings.Contains(s, `"`)
	hasSingle := strings.Contains(s, `'`)
	if hasDouble && !hasSingle {
		return "'" + s + "'"
	}
	return yamlDouble(s)
}

func yamlDouble(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			fmt.Fprintf(&b, `\x%02X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// FS 是写 provider 文件要用的最小文件系统接口(测试用假实现)。
type FS interface {
	Exists(path string) bool
	WriteFile(path string, data []byte, mode os.FileMode) error
	ReadFile(path string) ([]byte, error)
	MkdirAll(path string) error
	Remove(path string) error
}

// OSFS 是真实文件系统:WriteFile 先写同目录临时文件再改名,中途断电不会留下半个文件。
type OSFS struct{}

func (OSFS) Exists(path string) bool { _, err := os.Stat(path); return err == nil }

func (OSFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (OSFS) MkdirAll(path string) error { return os.MkdirAll(path, 0o700) }

func (OSFS) Remove(path string) error { return os.Remove(path) }

func (OSFS) WriteFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// ProviderFile 返回 provider 文件名:<tag>-<sha256(behavior \0 payload)>.yaml(上游 cg / $E)。
// 内容相同文件名就相同,所以已存在就不用再写。
func ProviderFile(tag, behavior, payload string) string {
	h := sha256.New()
	h.Write([]byte(behavior))
	h.Write([]byte{0})
	h.Write([]byte(payload))
	return tag + "-" + hex.EncodeToString(h.Sum(nil)) + ".yaml"
}

// WriteProvider 把载荷写进 <rulesDir>/<文件名>(0600;已存在不重写),返回 provider 条目。
// 条目的 path 是相对 Mihomo 工作目录(<DataDir>/mihomo)的 `./rules/<文件名>`。rulesDir 必须已存在。
func WriteProvider(fs FS, rulesDir, tag string, src Source, kind string) (Provider, error) {
	behavior, payload, err := BuildProvider(src, kind)
	if err != nil {
		return Provider{}, err
	}
	name := ProviderFile(tag, behavior, payload)
	full := filepath.Join(rulesDir, name)
	if !fs.Exists(full) {
		if err := fs.WriteFile(full, []byte(payload), 0o600); err != nil {
			return Provider{}, err
		}
	}
	return Provider{Type: "file", Behavior: behavior, Format: "yaml", Path: "./rules/" + name}, nil
}
