package contentfilter

// Quantumult X 的 [rewrite_local] 解析成结构化规则(口径和来源见 rules.go 的文件头)。
//
// 一行的写法:`<正则> url <动词> [参数……]`。动词分几类:
//   - reject 一族(reject / reject-200 / reject-img / reject-dict / reject-array / reject-drop):拒绝或给假响应;
//   - 302 / 307:跳转,后面跟目标;
//   - response-body:`<正则> url response-body <查找> response-body <替换>`,查找和替换都是原文(可以带空格,引号不当引号用);
//   - script-*:`script-response-body <脚本地址>[#参数]` 等六种;
//   - 认得但不支持的(request-body / echo-response / request-header / response-header / url-and-header):给原因码。
//
// 和 Surge 一族的几处差别(跟上游一致):QX 的 reject-img 是 `reject` 动作带 GIF 响应体、没有 contentType;
// reject 单独是 `drop` 之外最重的一档,延迟策略 escalating;reject-drop 直接丢包,动作是 `drop`、没有 params;
// 段只认 [rewrite_local](和没有段头的纯规则文件),[filter_local] / [task_local] 等别的段是 `quantumultx_unsupported_section:*`;
// [mitm] 整段忽略,`hostname =` 行不是规则。

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

type qxVerb struct {
	drop        bool
	status      int
	body        string
	delayPolicy string
}

var qxRejectVerbs = map[string]qxVerb{
	"reject":       {status: 404, body: "", delayPolicy: "escalating"},
	"reject-200":   {status: 200, body: "", delayPolicy: "none"},
	"reject-img":   {status: 200, body: tinyGIF, delayPolicy: "none"},
	"reject-dict":  {status: 200, body: "e30=", delayPolicy: "none"},
	"reject-array": {status: 200, body: "W10=", delayPolicy: "none"},
	"reject-drop":  {drop: true},
}

type qxScriptVerb struct {
	phase        string
	requiresBody bool
}

var qxScriptVerbs = map[string]qxScriptVerb{
	"script-request-header":        {"request", false},
	"script-request-body":          {"request", true},
	"script-response-header":       {"response", false},
	"script-response-body":         {"response", true},
	"script-echo-response":         {"request", false},
	"script-analyze-echo-response": {"request", true},
}

// qxSkippable 是 SkipFunc 可以放过的原因:正则 RE2 不支持、脚本动词不认得、以及四种「认得但不支持」的写法。
var qxSkipReasons = map[string]bool{
	"quantumultx_echo_response_unsupported":      true,
	"quantumultx_request_body_unsupported":       true,
	"quantumultx_raw_header_rewrite_unsupported": true,
	"quantumultx_url_and_header_unsupported":     true,
}

func qxSkippable(reason string) bool {
	if strings.HasPrefix(reason, "regex_unsupported_by_re2:") || strings.HasPrefix(reason, "quantumultx_script_") {
		return true
	}
	head, _, _ := strings.Cut(reason, ":")
	return qxSkipReasons[head]
}

var hostnameLine = regexp.MustCompile(`(?i)^hostname\s*=`)

func parseQuantumultX(text string, skip SkipFunc) ([]Rule, error) {
	var out []Rule
	for _, it := range sections(text) {
		if it.section == "mitm" {
			continue
		}
		if it.section != "" && it.section != "rewrite_local" {
			return nil, perr("quantumultx_unsupported_section:"+it.section, it)
		}
		if hostnameLine.MatchString(it.text) {
			continue
		}
		r, err := qxRewrite(it)
		if err != nil {
			var pe *ParseError
			if skip != nil && errors.As(err, &pe) && qxSkippable(pe.Reason) {
				skip(pe.Reason, pe.Line)
				continue
			}
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

type span struct {
	val        string
	start, end int
}

// rawTokens 按空白切词并记下每个词在原文里的位置;引号不当引号用(QX 的替换文本里引号是内容)。
func rawTokens(s string) []span {
	var out []span
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) || r == '\ufeff' {
			if start >= 0 {
				out = append(out, span{s[start:i], start, i})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, span{s[start:], start, len(s)})
	}
	return out
}

func qxRewrite(it item) (Rule, error) {
	toks := rawTokens(it.text)
	n := -1
	for i, t := range toks {
		if l := strings.ToLower(t.val); l == "url" || l == "url-and-header" {
			n = i
			break
		}
	}
	if n < 0 {
		return Rule{}, perr("quantumultx_missing_match_type", it)
	}
	match := strings.ToLower(toks[n].val)
	want := 1
	if match == "url-and-header" {
		want = 2
	}
	if n != want {
		return Rule{}, perr("quantumultx_pattern_field_count:"+itoa(n), it)
	}
	if match == "url-and-header" {
		return Rule{}, perr("quantumultx_url_and_header_unsupported", it)
	}
	pattern := toks[0].val
	if blank(pattern) {
		return Rule{}, perr("quantumultx_pattern_blank", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	if n+1 >= len(toks) {
		return Rule{}, perr("quantumultx_missing_verb", it)
	}
	verb := strings.ToLower(toks[n+1].val)
	args := toks[n+2:]
	id := ruleID(QuantumultX, it.text, it.line)
	base := Rule{ID: id, Dialect: QuantumultX, Pattern: pattern}

	if v, ok := qxRejectVerbs[verb]; ok {
		if v.drop {
			base.Action = "drop"
			return base, nil
		}
		base.Action = "reject"
		base.Params = map[string]any{"status": v.status, "body": v.body, "delayPolicy": v.delayPolicy}
		return base, nil
	}
	switch verb {
	case "302", "307":
		target := ""
		if len(args) > 0 {
			target = args[0].val
		}
		if blank(target) {
			return Rule{}, perr("quantumultx_redirect_missing_target", it)
		}
		status := 307
		if verb == "302" {
			status = 302
		}
		base.Action, base.Params = "redirect", map[string]any{"status": status, "target": target}
		return base, nil
	case "response-body":
		sep := -1
		for i, a := range args {
			if strings.ToLower(a.val) == "response-body" {
				sep = i
				break
			}
		}
		if sep < 0 {
			return Rule{}, perr("quantumultx_replace_missing_separator", it)
		}
		first, second := toks[n+1], toks[n+2+sep]
		search := trimJS(it.text[first.end:second.start])
		replacement := trimJS(it.text[second.end:])
		if search == "" {
			return Rule{}, perr("quantumultx_replace_missing_search", it)
		}
		if err := checkRegex(search, "search", it); err != nil {
			return Rule{}, err
		}
		base.Action = "replace"
		base.Params = map[string]any{"search": search, "replacement": replacement, "maxBytes": maxReplaceBytes}
		return base, nil
	case "request-body":
		return Rule{}, perr("quantumultx_request_body_unsupported", it)
	case "echo-response":
		return Rule{}, perr("quantumultx_echo_response_unsupported", it)
	case "request-header", "response-header":
		return Rule{}, perr("quantumultx_raw_header_rewrite_unsupported:"+verb, it)
	}
	if sv, ok := qxScriptVerbs[verb]; ok {
		path := ""
		if len(args) > 0 {
			path = args[0].val
		}
		if blank(path) {
			return Rule{}, perr("quantumultx_script_no_path", it)
		}
		url, argument := splitArgument(path)
		if blank(url) {
			return Rule{}, perr("quantumultx_script_no_path", it)
		}
		p := map[string]any{"phase": sv.phase, "sourceUrl": url, "sha256": "", "requiresBody": sv.requiresBody}
		if argument != "" {
			p["argument"] = argument
		}
		base.Action, base.Params = "script", p
		return base, nil
	}
	if strings.HasPrefix(verb, "script-") {
		return Rule{}, perr("quantumultx_script_unsupported:"+verb, it)
	}
	return Rule{}, perr("quantumultx_unknown_verb:"+verb, it)
}

// splitArgument 把 `地址#参数` 拆开:第一个 # 之后是参数;# 后面是空的当没有参数。
func splitArgument(s string) (url, argument string) {
	i := strings.IndexByte(s, '#')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}
