package contentfilter

// 内容过滤结构化解析第五步 a:一份规则文本的完整分析(参数声明与代入、规则、主机名、跳过统计)。
//
// 对齐上游 v0.1.402(v0.1.410 核对过无变化;只读参考 catch-up-upstream 的面板服务端,代码全部自写)。
// 纯函数、不碰网络和磁盘:脚本抓取与落盘是 5b。
//
// 流程:先按方言读出「声明的参数」(Surge / Shadowrocket 的 `#!arguments=`、Loon 的 `[Argument]` 段、
// Quantumult X 来源链接 `#` 之后的查询串;Stash 没有),参数值 = 声明的默认值被调用方给的值覆盖;
// 再把文本里的占位符换成值(Surge / Shadowrocket 是 `{{{名}}}`,Loon 是 `[{名}]`,Quantumult X / Stash 不代入);
// 代完仍有占位符留在规则行里(`[Script]` 段只看 `pattern=` 选项)就是 `<方言>_unresolved_argument:<占位符>`;
// 最后解析规则、取主机名。

import (
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Argument 是文本里声明的一个参数。
type Argument struct {
	Key          string `json:"key"`
	DefaultValue string `json:"defaultValue"`
}

// Analysis 是一份文本的分析结果。
type Analysis struct {
	Dialect   Dialect
	Rules     []Rule
	Hostnames []string
	Declared  []Argument
	// Values 是每个声明参数最终取的值(给了用给的,没给用默认值)。
	Values map[string]string
	// 各类被跳过的行数。口径与上游一致:正则不是 RE2 → Unsupported;Surge 路由规则 → Routing(其中屏蔽 QUIC 的
	// 另记在 CoveredQuic);别的含 `_unsupported` 的「认得但表达不了」→ Unrepresentable;其余(脚本条目)→ Scripts。
	SkippedScripts, SkippedUnsupported, SkippedRouting, SkippedUnrepresentable int
	CoveredQuic                                                                int
	// UnresolvedScriptArgs 是 `[Script]` 段里代入后仍没有值的占位符个数(只提示,不算错)。
	UnresolvedScriptArgs int
}

// Skipped 是面板上显示的「跳过」总数(四类之和,不含 CoveredQuic)。
func (a *Analysis) Skipped() int {
	return a.SkippedScripts + a.SkippedUnsupported + a.SkippedRouting + a.SkippedUnrepresentable
}

var (
	surgeArgsLine = regexp.MustCompile(`(?m)^#!arguments\s*=\s*(.+)$`)
	surgePlace    = regexp.MustCompile(`\{\{\{[^}]+\}\}\}`)
	loonPlace     = regexp.MustCompile(`\[\{[^}]+\}\]`)
	loonArgTypes  = map[string]bool{"input": true, "select": true, "switch": true, "text": true}
)

func unquote(s string) string {
	s = trimJS(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// DeclaredArguments 读出文本里声明的参数。sourceURL 只有 Quantumult X 用得上(参数写在链接的 `#` 之后)。
func DeclaredArguments(d Dialect, text, sourceURL string) []Argument {
	var out []Argument
	switch d {
	case Surge, Shadowrocket:
		m := surgeArgsLine.FindStringSubmatch(text)
		if m == nil {
			return nil
		}
		for _, a := range strings.Split(strings.TrimSuffix(m[1], "\r"), ",") {
			a = trimJS(a)
			if a == "" {
				continue
			}
			key, def := a, ""
			if i := strings.Index(a, ":"); i >= 0 {
				key, def = trimJS(a[:i]), trimJS(a[i+1:])
			}
			if key != "" {
				out = append(out, Argument{key, def})
			}
		}
	case Loon:
		for _, it := range sections(text) {
			if it.section != "argument" {
				continue
			}
			i := strings.Index(it.text, "=")
			if i < 0 {
				continue
			}
			key := trimJS(it.text[:i])
			u := strings.Split(it.text[i+1:], ",")
			for j := range u {
				u[j] = trimJS(u[j])
			}
			v := at(u, 0)
			if loonArgTypes[strings.ToLower(at(u, 0))] {
				v = at(u, 1)
			}
			if key != "" {
				out = append(out, Argument{key, unquote(v)})
			}
		}
	case QuantumultX:
		i := strings.Index(sourceURL, "#")
		if i < 0 || i == len(sourceURL)-1 {
			return nil
		}
		for _, pair := range strings.Split(sourceURL[i+1:], "&") {
			if pair == "" {
				continue
			}
			k, v, _ := strings.Cut(pair, "=")
			ku, e1 := url.QueryUnescape(k)
			vu, e2 := url.QueryUnescape(v)
			if e1 != nil || e2 != nil {
				ku, vu = k, v
			}
			out = append(out, Argument{ku, vu})
		}
	}
	return out
}

// substituteArgs 把占位符换成值(空值不换);Quantumult X / Stash 原样返回。
// 值按「声明的先、给的后」的顺序依次替换,和上游一样,换进去的内容若还带别的占位符,会被后面的键继续换。
func substituteArgs(d Dialect, text string, declared []Argument, values map[string]string) string {
	if d == QuantumultX || d == Stash {
		return text
	}
	val := map[string]string{}
	var keys []string
	for _, a := range declared {
		if _, ok := val[a.Key]; !ok {
			keys = append(keys, a.Key)
		}
		val[a.Key] = a.DefaultValue
	}
	var extra []string
	for k := range values {
		if _, ok := val[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	for k, v := range values {
		val[k] = v
	}
	for _, k := range keys {
		v := val[k]
		if v == "" {
			continue
		}
		ph := "{{{" + k + "}}}"
		if d == Loon {
			ph = "[{" + k + "}]"
		}
		text = strings.ReplaceAll(text, ph, v)
	}
	return text
}

func placeholderRe(d Dialect) *regexp.Regexp {
	if d == Loon {
		return loonPlace
	}
	return surgePlace
}

// checkResolved:代入之后规则行里不能再有占位符。`[Script]` 段只看 `pattern=` 选项(别的选项里的占位符是脚本参数,不算)。
func checkResolved(d Dialect, text string) error {
	re := placeholderRe(d)
	for _, it := range sections(text) {
		line := it.text
		if it.section == "script" {
			line = ""
			if i := strings.Index(it.text, "="); i > 0 {
				line = scriptOptions(it.text[i+1:])["pattern"]
			}
		}
		if m := re.FindString(line); m != "" {
			return &ParseError{Reason: string(d) + "_unresolved_argument:" + m, Line: it.line, Source: m}
		}
	}
	return nil
}

func countScriptPlaceholders(d Dialect, text string) int {
	if d == QuantumultX || d == Stash {
		return 0
	}
	n := 0
	re := placeholderRe(d)
	for _, it := range sections(text) {
		if it.section == "script" {
			n += len(re.FindAllString(it.text, -1))
		}
	}
	return n
}

// Analyze 完整分析一份规则文本。values 是调用方给的参数值(可为 nil),sourceURL 只用来读 Quantumult X 的参数。
// 解析失败返回 *ParseError(可用 errors.As 取)。
func Analyze(d Dialect, text string, values map[string]string, sourceURL string) (*Analysis, error) {
	declared := DeclaredArguments(d, text, sourceURL)
	resolved := substituteArgs(d, text, declared, values)
	if d != QuantumultX && d != Stash {
		if err := checkResolved(d, resolved); err != nil {
			return nil, err
		}
	}
	a := &Analysis{Dialect: d, Declared: declared, Values: map[string]string{}}
	rules, err := ParseRules(d, resolved, func(reason string, _ int) {
		switch {
		case strings.HasPrefix(reason, "regex_unsupported_by_re2:"):
			a.SkippedUnsupported++
		case reason == "surge_routing_quic_covered":
			a.CoveredQuic++
		case strings.HasPrefix(reason, "surge_routing_"):
			a.SkippedRouting++
		case !strings.HasPrefix(reason, "quantumultx_script_") && strings.Contains(reason, "_unsupported"):
			a.SkippedUnrepresentable++
		default:
			a.SkippedScripts++
		}
	})
	if err != nil {
		return nil, err
	}
	a.Rules = rules
	if rules == nil {
		a.Rules = []Rule{}
	}
	a.Hostnames = Hostnames(d, resolved)
	for _, g := range declared {
		if v, ok := values[g.Key]; ok {
			a.Values[g.Key] = v
		} else {
			a.Values[g.Key] = g.DefaultValue
		}
	}
	a.UnresolvedScriptArgs = countScriptPlaceholders(d, resolved)
	return a, nil
}

// ParseErrorOf 取出错误里的 *ParseError(没有返回 nil)。
func ParseErrorOf(err error) *ParseError {
	var pe *ParseError
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}
