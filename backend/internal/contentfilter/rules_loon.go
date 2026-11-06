package contentfilter

// Loon 的结构化解析(内容过滤结构化解析第三步)。
//
// 对齐上游 v0.1.402(v0.1.410 的同一段逻辑与原因码核对过,没有变化;只读参考 catch-up-upstream 的面板服务端,
// 代码全部自写):认 `[Rewrite]`、`[URL Rewrite]`(旧写法)、`[Script]` 三段,`[MITM]` 和 `[Argument]` 整段忽略,
// 别的段和段外的行是 `loon_unsupported_section:*` / `loon_line_outside_section`;`[Rule]` 等分流段不属于
// 内容过滤,按「不认得的段」处理——和上游一样直接报错,不是静默跳过。
// SkipFunc 只放过 `regex_unsupported_by_re2:*` 和 `loon_script_*`。
//
// 和 Surge 一族的差别(都跟上游一致):
//   - `reject` 是 drop(无 params);`reject-200 / -img / -dict / -array` 是 reject(延迟策略 none),
//     reject-img 带 GIF 体、没有 contentType,不像 Surge 那样变成 mapLocal;
//   - `[Rewrite]` 的头部动词是 `header-add` 等六个,不看 `http-request` / `http-response` 前缀;
//   - `[Script]` 一行是 `http-request|http-response <正则> script-path=…, requires-body=…`,
//     选项从正则之后的文本里取,没有 `name =` 前缀。

import (
	"errors"
	"math"
	"strings"
)

// loonRejects 是 [Rewrite] 里 reject 一族;drop 的 value 为 nil。
var loonRejects = map[string]map[string]any{
	"reject":       nil,
	"reject-200":   {"status": 200, "body": "", "delayPolicy": "none"},
	"reject-img":   {"status": 200, "body": tinyGIF, "delayPolicy": "none"},
	"reject-dict":  {"status": 200, "body": "e30=", "delayPolicy": "none"},
	"reject-array": {"status": 200, "body": "W10=", "delayPolicy": "none"},
}

var loonHeaderVerbs = map[string]struct{ direction, op string }{
	"header-add":              {"request", "add"},
	"header-del":              {"request", "remove"},
	"header-replace":          {"request", "set"},
	"response-header-add":     {"response", "add"},
	"response-header-del":     {"response", "remove"},
	"response-header-replace": {"response", "set"},
}

func loonSkippable(reason string) bool {
	return strings.HasPrefix(reason, "regex_unsupported_by_re2:") || strings.HasPrefix(reason, "loon_script_")
}

func parseLoon(text string, skip SkipFunc) ([]Rule, error) {
	var out []Rule
	for _, it := range sections(text) {
		if it.section == "mitm" || it.section == "argument" {
			continue
		}
		var r Rule
		var err error
		switch it.section {
		case "rewrite":
			r, err = loonRewrite(it)
		case "url rewrite":
			r, err = loonURLRewrite(it)
		case "script":
			r, err = loonScript(it)
		case "":
			err = perr("loon_line_outside_section", it)
		default:
			err = perr("loon_unsupported_section:"+it.section, it)
		}
		if err != nil {
			var pe *ParseError
			if skip != nil && errors.As(err, &pe) && loonSkippable(pe.Reason) {
				skip(pe.Reason, pe.Line)
				continue
			}
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func loonRewrite(it item) (Rule, error) {
	v, err := words(it)
	if err != nil {
		return Rule{}, err
	}
	pattern, verb := at(v, 0), strings.ToLower(at(v, 1))
	if blank(pattern) || verb == "" {
		return Rule{}, perr("loon_rewrite_too_short", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	rest := []string{}
	if len(v) > 2 {
		rest = v[2:]
	}
	id := ruleID(Loon, it.text, it.line)
	if params, ok := loonRejects[verb]; ok {
		if params == nil {
			return Rule{ID: id, Dialect: Loon, Pattern: pattern, Action: "drop"}, nil
		}
		return Rule{ID: id, Dialect: Loon, Pattern: pattern, Action: "reject", Params: cloneParams(params)}, nil
	}
	if verb == "302" || verb == "307" {
		target := at(rest, 0)
		if blank(target) {
			return Rule{}, perr("loon_redirect_missing_target", it)
		}
		return Rule{ID: id, Dialect: Loon, Pattern: pattern, Action: "redirect",
			Params: map[string]any{"status": redirectStatus(verb), "target": target}}, nil
	}
	if h, ok := loonHeaderVerbs[verb]; ok {
		name := at(rest, 0)
		if blank(name) {
			return Rule{}, perr("loon_header_missing_name", it)
		}
		value := ""
		if h.op != "remove" {
			if len(rest) > 1 {
				value = strings.Join(rest[1:], " ")
			}
			if value == "" {
				return Rule{}, perr("loon_header_missing_value", it)
			}
		}
		return Rule{ID: id, Dialect: Loon, Pattern: pattern, Action: "header",
			Params: map[string]any{"direction": h.direction, "op": h.op, "name": name, "value": value}}, nil
	}
	return Rule{}, perr("loon_unknown_verb:"+verb, it)
}

func redirectStatus(verb string) int {
	if verb == "302" {
		return 302
	}
	return 307
}

func cloneParams(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// loonURLRewrite 是旧写法 [URL Rewrite]:`<正则> - reject` 或 `<正则> <目标> 302|307`。
func loonURLRewrite(it item) (Rule, error) {
	v, err := words(it)
	if err != nil {
		return Rule{}, err
	}
	if len(v) < 2 {
		return Rule{}, perr("loon_url_rewrite_too_short", it)
	}
	last, pattern := strings.ToLower(v[len(v)-1]), v[0]
	if blank(pattern) {
		return Rule{}, perr("loon_url_rewrite_pattern_blank", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	id := ruleID(Loon, it.text, it.line)
	if last == "reject" {
		return Rule{ID: id, Dialect: Loon, Pattern: pattern, Action: "drop"}, nil
	}
	if last != "302" && last != "307" {
		return Rule{}, perr("loon_url_rewrite_header_unsupported", it)
	}
	if len(v) < 3 || blank(v[1]) {
		return Rule{}, perr("loon_url_rewrite_missing_target", it)
	}
	return Rule{ID: id, Dialect: Loon, Pattern: pattern, Action: "redirect",
		Params: map[string]any{"status": redirectStatus(last), "target": v[1]}}, nil
}

func loonScript(it item) (Rule, error) {
	v, err := words(it)
	if err != nil {
		return Rule{}, err
	}
	typ := strings.ToLower(at(v, 0))
	if typ != "http-request" && typ != "http-response" {
		return Rule{}, perr("loon_script_unsupported_type:"+typ, it)
	}
	pattern := at(v, 1)
	if blank(pattern) {
		return Rule{}, perr("loon_script_pattern_blank", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	o := scriptOptions(it.text[strings.Index(it.text, pattern)+len(pattern):])
	path := o["script-path"]
	if blank(path) {
		return Rule{}, perr("loon_script_no_path", it)
	}
	phase := "response"
	if typ == "http-request" {
		phase = "request"
	}
	p := map[string]any{"phase": phase, "sourceUrl": path, "sha256": "", "requiresBody": truthy(o["requires-body"])}
	if ms := timeoutMs(o["timeout"]); ms > 0 {
		p["timeoutMs"] = ms
	}
	if f, ok := number(o["max-size"]); ok && f > 0 {
		if f == math.Trunc(f) && f < 1<<53 {
			p["maxBytes"] = int64(f)
		} else {
			p["maxBytes"] = f
		}
	}
	if arg := o["argument"]; !blank(arg) {
		p["argument"] = arg
	}
	if truthy(o["binary-body-mode"]) {
		p["binaryBodyMode"] = true
	}
	return Rule{ID: ruleID(Loon, it.text, it.line), Dialect: Loon, Pattern: pattern, Action: "script", Params: p}, nil
}
