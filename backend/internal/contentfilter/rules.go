package contentfilter

// 结构化规则:把方言原文(Surge / Shadowrocket 的 [URL Rewrite] [Header Rewrite] [Map Local] [Body Rewrite] [Script])
// 解析成 App 能直接执行的规则对象。App 拿到的内容过滤快照里是这些规则,不是方言原文。
//
// 口径对齐上游 v0.1.402(维护者授权只读参考 catch-up-upstream 面板服务端;代码全部自写,没有粘贴源码):
// 规则字段、action 取值、各种「不支持」的原因码、规则 id 的算法都跟上游一致,这样同一份文本两边解出同样的规则。
// 有意的差异:正则合法性直接用 Go 的 regexp(就是 RE2,App 执行用的那一套),上游是自己写了一个检查器去近似 RE2,
// 所以 `regex_unsupported_by_re2:<字段>:<原因>` 里「原因」那一段的措辞不同,前缀和字段名一样。
//
// 本文件做 Surge / Shadowrocket(QuantumultX 在 rules_qx.go);Loon 在 rules_loon.go,Stash 在 rules_stash.go(见 STATUS.md)。

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"
)

// Rule 是一条结构化规则。Params 的键随 Action 而定:
//
//	reject    {status, body, delayPolicy}
//	mapLocal  {contentType, body}                (body 是 base64)
//	redirect  {status, target}
//	header    {direction, op, name, value}       (direction: request|response;op: set|add|remove)
//	replace   {search, replacement, maxBytes}    (只对响应体)
//	script    {phase, sourceUrl, sha256, requiresBody, timeoutMs?, maxBytes?, argument?, binaryBodyMode?}
type Rule struct {
	ID        string         `json:"id"`
	RuleSetID string         `json:"ruleSetId,omitempty"` // 合并成快照文档时才有
	Dialect   Dialect        `json:"dialect"`
	Pattern   string         `json:"pattern"`
	Action    string         `json:"action"`
	Params    map[string]any `json:"params,omitempty"`
}

// ParseError 是解析一行时的失败:Reason 是机器可读的原因码(前端和日志用),Line 从 1 起。
type ParseError struct {
	Reason string
	Line   int
	Source string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("filter parse failed at line %d: %s", e.Line, e.Reason)
}

func perr(reason string, it item) *ParseError {
	return &ParseError{Reason: reason, Line: it.line, Source: it.text}
}

// SkipFunc 收到「这一行跳过了」的通知。传 nil 表示这类问题也当成失败返回。
// 只有三类原因可以跳过:正则 RE2 不支持、Surge 的路由规则(`surge_routing_*`)、不支持的脚本条目(`surge_script_*`)。
type SkipFunc func(reason string, line int)

const (
	// maxReplaceBytes 是 Body Rewrite 规则允许改写的响应体上限(512 KiB)。
	maxReplaceBytes = 512 * 1024
	// tinyGIF 是 1×1 透明 GIF 的 base64。
	tinyGIF = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"
)

// rejectKinds 是 URL Rewrite 里能直接变成「拒绝 / 假响应」的几种写法。
var rejectKinds = map[string]struct {
	status      int
	body        string
	contentType string
}{
	"reject":       {404, "", ""},
	"reject-200":   {200, "", ""},
	"reject-img":   {200, tinyGIF, "image/gif"},
	"reject-dict":  {200, base64.StdEncoding.EncodeToString([]byte("{}")), "application/json"},
	"reject-array": {200, base64.StdEncoding.EncodeToString([]byte("[]")), "application/json"},
}

// ParseRules 把一份方言原文解析成结构化规则。格式还没做返回 ErrUnsupported。
func ParseRules(d Dialect, text string, skip SkipFunc) ([]Rule, error) {
	switch d {
	case Surge, Shadowrocket:
		return parseSurgeLike(text, d, skip)
	case QuantumultX:
		return parseQuantumultX(text, skip)
	case Loon:
		return parseLoon(text, skip)
	case Stash:
		return parseStash(text) // Stash 没有可跳过的行,skip 不起作用(上游也一样)
	}
	return nil, ErrUnsupported
}

// item 是去掉注释和空行之后的一行,带它所在的段(小写)。
type item struct {
	section string
	line    int
	text    string
}

// trimJS 去掉首尾空白,含字节序标记(U+FEFF)。
func trimJS(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' })
}

func blank(s string) bool { return trimJS(s) == "" }

var sectionHeader = regexp.MustCompile(`^\[([^\]]+)\]$`)

// sections 按行切开,跳过空行和 `#` `//` `;` 开头的注释,记下 `[段名]`。
func sections(text string) []item {
	var out []item
	cur := ""
	for i, raw := range strings.Split(text, "\n") {
		s := trimJS(strings.TrimSuffix(raw, "\r"))
		if s == "" || s[0] == '#' || s[0] == ';' || strings.HasPrefix(s, "//") {
			continue
		}
		if m := sectionHeader.FindStringSubmatch(s); m != nil {
			cur = strings.ToLower(trimJS(m[1]))
			continue
		}
		out = append(out, item{section: cur, line: i + 1, text: s})
	}
	return out
}

// tokens 按空白切词,单引号 / 双引号里的空白不切,引号本身不留在词里;引号没收口返回 ok=false。
func tokens(s string) (vals []string, ok bool) {
	var cur strings.Builder
	var quote rune
	has := false
	for _, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote, has = r, true
			continue
		}
		if unicode.IsSpace(r) {
			if cur.Len() > 0 || has {
				vals = append(vals, cur.String())
				cur.Reset()
				has = false
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 || has {
		vals = append(vals, cur.String())
	}
	return vals, quote == 0
}

func words(it item) ([]string, error) {
	v, ok := tokens(it.text)
	if !ok {
		return nil, perr("unterminated_quote", it)
	}
	return v, nil
}

// checkRegex 用 Go 的 RE2 编一遍;编不过就是 `regex_unsupported_by_re2:<字段>:<原因>`。
func checkRegex(expr, field string, it item) error {
	if _, err := regexp.Compile(expr); err != nil {
		reason := err.Error()
		var se *syntax.Error
		if errors.As(err, &se) {
			reason = string(se.Code)
		}
		return perr("regex_unsupported_by_re2:"+field+":"+reason, it)
	}
	return nil
}

// ruleID 是规则 id:`r-` 加 sha256(`方言|行号|文本`) 的前 8 位十六进制。同一份文本每次解出同样的 id。
func ruleID(d Dialect, text string, line int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", d, line, text)))
	return "r-" + hex.EncodeToString(sum[:])[:8]
}

func at(v []string, i int) string {
	if i < len(v) {
		return v[i]
	}
	return ""
}

func parseSurgeLike(text string, d Dialect, skip SkipFunc) ([]Rule, error) {
	var out []Rule
	for _, it := range sections(text) {
		if it.section == "mitm" {
			continue
		}
		rs, err := surgeItem(it, d)
		if err != nil {
			var pe *ParseError
			if skip != nil && errors.As(err, &pe) && skippable(pe.Reason) {
				skip(pe.Reason, pe.Line)
				continue
			}
			return nil, err
		}
		out = append(out, rs...)
	}
	return out, nil
}

func skippable(reason string) bool {
	return strings.HasPrefix(reason, "regex_unsupported_by_re2:") ||
		strings.HasPrefix(reason, "surge_routing_") || strings.HasPrefix(reason, "surge_script_")
}

func surgeItem(it item, d Dialect) ([]Rule, error) {
	switch it.section {
	case "url rewrite":
		r, err := surgeURLRewrite(it, d)
		return one(r, err)
	case "header rewrite":
		r, err := surgeHeaderRewrite(it, d)
		return one(r, err)
	case "map local":
		r, err := surgeMapLocal(it, d)
		return one(r, err)
	case "body rewrite":
		return surgeBodyRewrite(it, d)
	case "script":
		r, err := surgeScript(it, d)
		return one(r, err)
	case "rule":
		if routingIsQUICReject(it.text) {
			return nil, perr("surge_routing_quic_covered", it)
		}
		return nil, perr("surge_routing_unsupported", it)
	case "":
		return nil, perr("surge_line_outside_section", it)
	}
	return nil, perr("surge_unsupported_section:"+it.section, it)
}

func one(r Rule, err error) ([]Rule, error) {
	if err != nil {
		return nil, err
	}
	return []Rule{r}, nil
}

var quicReject = regexp.MustCompile(`\bPROTOCOL\s*,\s*(UDP|QUIC)\b`)
var rejectTail = regexp.MustCompile(`\bREJECT(-\w+)?\s*$`)

// routingIsQUICReject:`PROTOCOL,QUIC,REJECT` 这类路由规则内核已经自己处理了,单独给个原因码,别当成「不认识」。
func routingIsQUICReject(text string) bool {
	up := strings.ToUpper(text)
	return quicReject.MatchString(up) && rejectTail.MatchString(strings.Join(strings.Fields(up), " "))
}

func surgeURLRewrite(it item, d Dialect) (Rule, error) {
	v, err := words(it)
	if err != nil {
		return Rule{}, err
	}
	if len(v) < 2 {
		return Rule{}, perr("surge_url_rewrite_too_short", it)
	}
	kind := strings.ToLower(v[len(v)-1])
	_, isReject := rejectKinds[kind]
	if kind != "302" && kind != "307" && !isReject {
		kind = "header"
	}
	pattern := v[0]
	if blank(pattern) {
		return Rule{}, perr("surge_url_rewrite_pattern_blank", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	id := ruleID(d, it.text, it.line)
	if kind == "header" {
		return Rule{}, perr("surge_url_rewrite_header_unsupported", it)
	}
	if rj, ok := rejectKinds[kind]; ok {
		if rj.contentType != "" {
			return Rule{ID: id, Dialect: d, Pattern: pattern, Action: "mapLocal",
				Params: map[string]any{"contentType": rj.contentType, "body": rj.body}}, nil
		}
		return Rule{ID: id, Dialect: d, Pattern: pattern, Action: "reject",
			Params: map[string]any{"status": rj.status, "body": rj.body, "delayPolicy": "none"}}, nil
	}
	target := at(v, 1)
	if len(v) < 3 || blank(target) {
		return Rule{}, perr("surge_url_rewrite_missing_target", it)
	}
	status := 307
	if kind == "302" {
		status = 302
	}
	return Rule{ID: id, Dialect: d, Pattern: pattern, Action: "redirect",
		Params: map[string]any{"status": status, "target": target}}, nil
}

var headerDirections = map[string]string{"http-request": "request", "http-response": "response"}
var headerOps = map[string]string{"header-replace": "set", "header-add": "add", "header-del": "remove"}

func surgeHeaderRewrite(it item, d Dialect) (Rule, error) {
	v, err := words(it)
	if err != nil {
		return Rule{}, err
	}
	n := 0
	dir := "request"
	lead, hasLead := headerDirections[strings.ToLower(at(v, n))]
	if hasLead {
		dir = lead
		n++
	}
	pattern := at(v, n)
	n++
	if blank(pattern) {
		return Rule{}, perr("surge_header_rewrite_too_short", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	if !hasLead {
		if after, ok := headerDirections[strings.ToLower(at(v, n))]; ok {
			dir = after
			n++
		}
	}
	opWord := strings.ToLower(at(v, n))
	n++
	op, ok := headerOps[opWord]
	if !ok {
		return Rule{}, perr("surge_header_op_unsupported:"+opWord, it)
	}
	name := at(v, n)
	n++
	if blank(name) {
		return Rule{}, perr("surge_header_missing_name", it)
	}
	value := ""
	if op != "remove" {
		if n < len(v) {
			value = strings.Join(v[n:], " ")
		}
		if value == "" {
			return Rule{}, perr("surge_header_missing_value", it)
		}
	}
	return Rule{ID: ruleID(d, it.text, it.line), Dialect: d, Pattern: pattern, Action: "header",
		Params: map[string]any{"direction": dir, "op": op, "name": name, "value": value}}, nil
}

var contentTypeInHeader = regexp.MustCompile(`(?i)content-type\s*:\s*(\S+)`)
var base64Body = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)

func surgeMapLocal(it item, d Dialect) (Rule, error) {
	v, err := words(it)
	if err != nil {
		return Rule{}, err
	}
	pattern := at(v, 0)
	if blank(pattern) {
		return Rule{}, perr("surge_map_local_too_short", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	opts := map[string]string{}
	for _, o := range v[1:] {
		eq := strings.IndexByte(o, '=')
		if eq < 0 {
			return Rule{}, perr("surge_map_local_bad_option:"+o, it)
		}
		opts[strings.ToLower(o[:eq])] = o[eq+1:]
	}
	if s, ok := opts["status-code"]; ok && s != "200" {
		return Rule{}, perr("surge_map_local_status_unsupported:"+s, it)
	}
	dataType := "text"
	if t, ok := opts["data-type"]; ok {
		dataType = strings.ToLower(t)
	}
	data := opts["data"]
	ct := ""
	if m := contentTypeInHeader.FindStringSubmatch(opts["header"]); m != nil {
		ct = m[1]
	}
	orDefault := func(def string) string {
		if ct != "" {
			return ct
		}
		return def
	}
	var contentType, body string
	switch dataType {
	case "text":
		contentType, body = orDefault("text/plain; charset=utf-8"), base64.StdEncoding.EncodeToString([]byte(data))
	case "base64":
		if len(data)%4 != 0 || !base64Body.MatchString(data) {
			return Rule{}, perr("surge_map_local_bad_base64", it)
		}
		contentType, body = orDefault("application/octet-stream"), data
	case "tiny-gif":
		contentType, body = orDefault("image/gif"), tinyGIF
	default:
		return Rule{}, perr("surge_map_local_data_type_unsupported:"+dataType, it)
	}
	return Rule{ID: ruleID(d, it.text, it.line), Dialect: d, Pattern: pattern, Action: "mapLocal",
		Params: map[string]any{"contentType": contentType, "body": body}}, nil
}

func surgeBodyRewrite(it item, d Dialect) ([]Rule, error) {
	v, err := words(it)
	if err != nil {
		return nil, err
	}
	if dir := strings.ToLower(at(v, 0)); dir != "http-response" {
		return nil, perr("surge_body_rewrite_direction_unsupported:"+dir, it)
	}
	pattern := at(v, 1)
	if blank(pattern) {
		return nil, perr("surge_body_rewrite_too_short", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return nil, err
	}
	var pairs []string
	if len(v) > 2 {
		pairs = v[2:]
	}
	if len(pairs) == 0 || len(pairs)%2 != 0 {
		return nil, perr("surge_body_rewrite_unpaired", it)
	}
	var out []Rule
	for i := 0; i < len(pairs); i += 2 {
		search := pairs[i]
		if search == "" {
			return nil, perr("surge_body_rewrite_missing_search", it)
		}
		if err := checkRegex(search, "search", it); err != nil {
			return nil, err
		}
		out = append(out, Rule{
			ID: ruleID(d, fmt.Sprintf("%s#%d", it.text, i), it.line), Dialect: d, Pattern: pattern, Action: "replace",
			Params: map[string]any{"search": search, "replacement": pairs[i+1], "maxBytes": maxReplaceBytes},
		})
	}
	return out, nil
}

// scriptOptionKeys 是 [Script] 一行里认得的选项名;逗号后面跟着这些名字加 `=` 才算新选项,
// 其余逗号属于上一个值(比如 argument 里可以有逗号)。
var scriptOptionKeys = map[string]bool{
	"type": true, "pattern": true, "script-path": true, "requires-body": true, "timeout": true, "max-size": true,
	"argument": true, "binary-body-mode": true, "debug": true, "engine": true, "tag": true, "script-update-interval": true,
}

var optionStart = regexp.MustCompile(`(?i),\s*([a-z][a-z0-9-]*)=`)

// scriptOptions 把 `type=http-response, pattern=..., script-path=...` 拆成键值表(键小写,值去掉外层双引号)。
func scriptOptions(s string) map[string]string {
	var pieces []string
	last := 0
	for _, m := range optionStart.FindAllStringSubmatchIndex(s, -1) {
		if scriptOptionKeys[strings.ToLower(s[m[2]:m[3]])] {
			pieces = append(pieces, s[last:m[0]])
			last = m[2]
		}
	}
	pieces = append(pieces, s[last:])
	out := map[string]string{}
	for _, p := range pieces {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		val := trimJS(p[eq+1:])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		out[strings.ToLower(trimJS(p[:eq]))] = val
	}
	return out
}

func truthy(s string) bool {
	switch strings.ToLower(trimJS(s)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// number 按 JavaScript 的 Number() 的口径读一个有限数;空串是 0(跟 JS 一样),读不出或不是有限数返回 ok=false。
func number(s string) (float64, bool) {
	s = trimJS(s)
	if s == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// timeoutMs 读超时:大于 1000 的数当毫秒,否则当秒;没写或不是正数返回 0。
func timeoutMs(s string) int64 {
	if blank(s) {
		return 0
	}
	f, ok := number(s)
	if !ok || f <= 0 {
		return 0
	}
	if f > 1000 {
		return int64(math.Round(f))
	}
	return int64(math.Round(f * 1000))
}

func surgeScript(it item, d Dialect) (Rule, error) {
	eq := strings.IndexByte(it.text, '=')
	if eq <= 0 {
		return Rule{}, perr("surge_script_no_name", it)
	}
	o := scriptOptions(it.text[eq+1:])
	typ := strings.ToLower(o["type"])
	if typ != "http-request" && typ != "http-response" {
		return Rule{}, perr("surge_script_unsupported_type:"+typ, it)
	}
	pattern := o["pattern"]
	if blank(pattern) {
		return Rule{}, perr("surge_script_pattern_blank", it)
	}
	if err := checkRegex(pattern, "pattern", it); err != nil {
		return Rule{}, err
	}
	path := o["script-path"]
	if blank(path) {
		return Rule{}, perr("surge_script_no_path", it)
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
	return Rule{ID: ruleID(d, it.text, it.line), Dialect: d, Pattern: pattern, Action: "script", Params: p}, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
