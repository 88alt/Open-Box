// Package contentfilter 管理「内容过滤」:规则集和脚本的快照存在路由器上,同步到 iOS 后在手机执行。
// 本包只管记录、文本和计数;路由器自己不执行这些规则。
//
// 自己的设计,依据前端 api/clientApp.ts 的 OpenboxContentFilter* 类型、ContentFilterCard.vue 的用法,
// 以及 Surge / Loon / Shadowrocket 各自公开的配置文档;没有参照上游。口径见 docs/ENDPOINTS.md 的 content-filter 一节。
// (说明:本文件的计数口径是第一版自己的设计,与上游不同;给 App 的结构化规则见 rules.go,计数换成「结构化规则条数」是 STATUS 里内容过滤第五步。)
//
// 计数口径(逐行、按段落,认不出的行不报错):
//   - ruleCount:规则段里认得出的规则行(含各种重写条目);
//   - scriptCount:脚本段里的脚本条目,或带脚本的重写条目(Loon 的 script-* 重写);
//   - skipped:上面这些段里认不出的行。注释、空行、元信息行(#!name=...)、不关心的段(MITM / General 等)不计。
package contentfilter

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Dialect 是规则的格式(哪个 App 的写法)。
type Dialect string

// 五种格式。
const (
	Surge        Dialect = "surge"
	QuantumultX  Dialect = "quantumultx"
	Loon         Dialect = "loon"
	Shadowrocket Dialect = "shadowrocket"
	Stash        Dialect = "stash"
)

// ErrUnsupported 表示这个格式还没做(quantumultx、stash 排在第二批)。
var ErrUnsupported = errors.New("该规则格式暂不支持")

// ParseDialect 把前端传来的字符串变成 Dialect;不认识返回 false。
func ParseDialect(s string) (Dialect, bool) {
	switch d := Dialect(strings.TrimSpace(s)); d {
	case Surge, QuantumultX, Loon, Shadowrocket, Stash:
		return d, true
	}
	return "", false
}

// Supported 说明这个格式现在能不能解析。
func Supported(d Dialect) bool {
	_, ok := tables[d]
	return ok || d == Stash
}

// Counts 是一份文本按某个格式数出来的结果。
type Counts struct{ Rules, Scripts, Skipped int }

type kind int

const (
	kRule          kind = iota // 规则行
	kURLRewrite                // Surge / Shadowrocket 的 [URL Rewrite]
	kHeaderRewrite             // [Header Rewrite]
	kBodyRewrite               // [Body Rewrite]
	kMapLocal                  // [Map Local]
	kLoonRewrite               // Loon 的 [Rewrite]
	kScript                    // [Script]
	kQXRule                    // Quantumult X 的 [filter_local]
	kQXRewrite                 // [rewrite_local]
	kQXTask                    // [task_local]
)

var surgeLike = map[string]kind{
	"rule": kRule, "url rewrite": kURLRewrite, "header rewrite": kHeaderRewrite,
	"body rewrite": kBodyRewrite, "map local": kMapLocal, "script": kScript,
}

// tables:每种格式认哪些段。没列出的段(MITM / General / Host ……)整段忽略。
var tables = map[Dialect]map[string]kind{
	Surge:        surgeLike,
	Shadowrocket: surgeLike,
	Loon:         {"rule": kRule, "rewrite": kLoonRewrite, "script": kScript},
	// [rewrite_remote] 只是别的文件的链接,[mitm] / [general] 不关心:都整段忽略
	QuantumultX: {"filter_local": kQXRule, "rewrite_local": kQXRewrite, "task_local": kQXTask},
}

var ruleTypes = setOf(
	"DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "DOMAIN-WILDCARD", "DOMAIN-REGEX", "DOMAIN-SET",
	"IP-CIDR", "IP-CIDR6", "IP-ASN", "GEOIP", "SRC-IP", "SRC-PORT", "DST-PORT", "DEST-PORT", "IN-PORT",
	"USER-AGENT", "URL-REGEX", "PROCESS-NAME", "PROTOCOL", "SUBNET", "DEVICE-NAME", "CELLULAR-RADIO",
	"HOSTNAME-TYPE", "RULE-SET", "SCRIPT", "AND", "OR", "NOT", "FINAL",
)

var surgeRewriteTypes = setOf("reject", "reject-200", "reject-img", "reject-dict", "reject-array", "reject-tinygif", "reject-drop", "302", "307", "header")

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// Count 按格式数一份文本。格式还没做返回 ErrUnsupported。
func Count(d Dialect, text string) (Counts, error) {
	if d == Stash {
		return countStash(text), nil
	}
	tab, ok := tables[d]
	if !ok {
		return Counts{}, ErrUnsupported
	}
	var c Counts
	cur, active := kRule, true // 第一个段头之前的行按规则行算(纯规则集文件没有段头)
	if d == QuantumultX {
		cur = kQXRule
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if line == "" || line[0] == '#' || line[0] == ';' || strings.HasPrefix(line, "//") {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			cur, active = tab[strings.ToLower(strings.TrimSpace(line[1:len(line)-1]))]
			continue
		}
		if !active {
			continue
		}
		switch classify(cur, line) {
		case 1:
			c.Rules++
		case 2:
			c.Scripts++
		default:
			c.Skipped++
		}
	}
	return c, nil
}

// classify 返回 1 = 规则,2 = 脚本,0 = 认不出。
func classify(k kind, line string) int {
	f := strings.Fields(line)
	switch k {
	case kRule:
		if i := strings.IndexByte(line, ','); i > 0 && ruleTypes[strings.ToUpper(strings.TrimSpace(line[:i]))] {
			return 1
		}
	case kURLRewrite:
		if len(f) >= 3 && surgeRewriteTypes[strings.ToLower(f[len(f)-1])] {
			return 1
		}
	case kHeaderRewrite:
		if h := strings.ToLower(f[0]); len(f) >= 3 && (h == "http-request" || h == "http-response") {
			return 1
		}
	case kBodyRewrite:
		if len(f) >= 3 && strings.HasPrefix(strings.ToLower(f[0]), "http-") {
			return 1
		}
	case kMapLocal:
		if strings.Contains(line, "data=") || strings.Contains(line, "data-type=") || strings.Contains(line, "data-path=") {
			return 1
		}
	case kLoonRewrite:
		if len(f) >= 2 {
			t := strings.ToLower(f[1])
			if strings.HasPrefix(t, "script-") {
				return 2
			}
			if loonRewriteOK(t) {
				return 1
			}
		}
	case kScript:
		if strings.Contains(line, "script-path=") {
			return 2
		}
	case kQXRule:
		if i := strings.IndexByte(line, ','); i > 0 && qxRuleTypes[strings.ToUpper(strings.TrimSpace(line[:i]))] {
			return 1
		}
	case kQXRewrite:
		// <正则> url <类型> [值]
		if len(f) >= 3 && strings.EqualFold(f[1], "url") {
			t := strings.ToLower(f[2])
			if strings.HasPrefix(t, "script-") {
				return 2
			}
			if qxRewriteOK(t) {
				return 1
			}
		}
	case kQXTask:
		// <cron 表达式 5 段> <脚本路径>, tag=...;或 event-interaction / event-network <脚本路径>
		if strings.Contains(line, ".js") && (len(f) >= 6 || strings.HasPrefix(strings.ToLower(f[0]), "event-")) {
			return 2
		}
	}
	return 0
}

var qxRuleTypes = setOf(
	"HOST", "HOST-SUFFIX", "HOST-KEYWORD", "HOST-WILDCARD", "IP-CIDR", "IP6-CIDR", "IP-ASN",
	"GEOIP", "USER-AGENT", "URL-REGEX", "FINAL",
)

func qxRewriteOK(t string) bool {
	if t == "302" || t == "307" || t == "header" {
		return true
	}
	for _, p := range []string{"reject", "request-", "response-", "echo-", "jsonjq-", "mock-"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func loonRewriteOK(t string) bool {
	if t == "302" || t == "307" || t == "header" {
		return true
	}
	for _, p := range []string{"reject", "header-", "mock-", "request-", "response-"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

var argRe = regexp.MustCompile(`\{\{\{([^{}]+)\}\}\}`)

// Substitute 把文本里的 {{{名字}}} 换成 values 里的值;没给值的(或值不是字符串 / 数字 / 布尔的)保持原样。
func Substitute(text string, values map[string]any) string {
	if len(values) == 0 {
		return text
	}
	return argRe.ReplaceAllStringFunc(text, func(m string) string {
		name := strings.TrimSpace(m[3 : len(m)-3])
		switch v := values[name].(type) {
		case string:
			return v
		case bool:
			return strconv.FormatBool(v)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case int:
			return strconv.Itoa(v)
		}
		return m
	})
}

// Normalize 把文本规整成存盘的样子:去 BOM,换行统一成 \n,每行去掉尾部空白,去掉结尾空行,非空时以一个 \n 结尾。
func Normalize(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if out == "" {
		return ""
	}
	return out + "\n"
}

var (
	yamlKeyRe  = regexp.MustCompile(`^([A-Za-z0-9_-]+):`)
	bareKeyRe  = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*$`)
	entryKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+:`)
)

// countStash 数 Stash 覆写文件(YAML)。不做完整的 YAML 解析,只按缩进逐行认:
//   - 顶层 rules:列表里每个 `- 规则` 认得出的算规则;
//   - http: 下的 url-rewrite / header-rewrite / body-rewrite 是字符串列表,口径同 Surge;
//     map-local 的每个条目算一条规则,script 的每个条目算一个脚本(条目是多行映射,只数 `- ` 开头的那一行);
//   - cron: 下 script 的每个条目算一个脚本;
//   - mitm / script-providers / 其它顶层键整段忽略(元信息 name: desc: 也不计)。
func countStash(text string) Counts {
	var c Counts
	var top, sub string
	subIndent, itemIndent := -1, -1
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimRight(strings.TrimPrefix(raw, "\ufeff"), " \t\r")
		body := strings.TrimSpace(raw)
		if body == "" || body[0] == '#' || body[0] == ';' || strings.HasPrefix(body, "//") || body == "---" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		isItem := body == "-" || strings.HasPrefix(body, "- ")
		if !isItem {
			if indent == 0 {
				top, sub, subIndent, itemIndent = "", "", -1, -1
				if m := yamlKeyRe.FindStringSubmatch(body); m != nil {
					top = m[1]
				}
			} else if top == "http" || top == "cron" {
				if m := bareKeyRe.FindStringSubmatch(body); m != nil && (subIndent < 0 || indent == subIndent) {
					sub, subIndent, itemIndent = m[1], indent, -1
				}
			}
			continue
		}
		if itemIndent < 0 {
			itemIndent = indent
		}
		if indent != itemIndent { // 条目里嵌套的列表不数
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(body, "-"))
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		var res int
		switch {
		case top == "rules":
			res = stashLine(kRule, val)
		case top == "http" && sub == "url-rewrite":
			res = stashLine(kURLRewrite, val)
		case top == "http" && sub == "header-rewrite":
			res = stashLine(kHeaderRewrite, val)
		case top == "http" && sub == "body-rewrite":
			res = stashLine(kBodyRewrite, val)
		case top == "http" && sub == "map-local" && entryKeyRe.MatchString(val):
			res = 1
		case top == "http" && sub == "script" && entryKeyRe.MatchString(val):
			res = 2
		case top == "cron" && sub == "script" && entryKeyRe.MatchString(val):
			res = 2
		case top == "rules" || (top == "http" && (sub == "map-local" || sub == "script")) || (top == "cron" && sub == "script"):
			res = 0
		default:
			continue // 不关心的段(mitm 等)
		}
		switch res {
		case 1:
			c.Rules++
		case 2:
			c.Scripts++
		default:
			c.Skipped++
		}
	}
	return c
}

func stashLine(k kind, val string) int {
	if val == "" {
		return 0
	}
	return classify(k, val)
}
