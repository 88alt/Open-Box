package contentfilter

// Stash 的结构化解析(内容过滤结构化解析第四步)。
//
// 对齐上游 v0.1.402(v0.1.410 的同一段逻辑与原因码核对过,没有变化;只读参考 catch-up-upstream 的面板服务端,
// 代码全部自写)。Stash 的覆写是 YAML,上游没有引 YAML 库,只认一个很窄的子集:
//
//	http:
//	  url-rewrite:
//	    - ^https://a\.com/ - reject
//	  header-rewrite:
//	    - ^https://a\.com/ request-add X-A 1
//	  mitm:
//	    - "a.com"
//
// 只读顶格的 `http:` 块,块里每个键下是 `- ` 开头的列表项;块外的行一概不看。
// 缩进用 tab、锚点 / 别名(`&x` `*x`)、块标量(`|` `>`)、行内写法(`key: value`、`[..]`、`{..}`)都是
// 明确的 `stash_*` 原因码。块里认得的键只有 url-rewrite / header-rewrite(规则)和 mitm(主机名,第五步用),
// 其它键(如 script)是 `stash_unsupported_http_key:*`。
//
// 和别的格式不同:Stash **没有跳过**——正则不是 RE2、动词不认得,一律直接失败,SkipFunc 不起作用(上游也没传)。
// 规则 id 取的是**未去空白的整行原文**(带缩进和 `- `)加行号。

import (
	"regexp"
	"strings"
)

var (
	stashAnchor      = regexp.MustCompile(`(^|\s)[&*][A-Za-z0-9_-]+`)
	stashBlockScalar = regexp.MustCompile(`:\s*[|>][-+0-9]*\s*$`)
	stashHTTPKey     = regexp.MustCompile(`^http\s*:\s*(.*)$`)
	stashKey         = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*:\s*(.*)$`)
)

// stashItem 是 `- ` 列表项:text 是整行原文(出错和算 id 用),value 是去掉引号的内容。
type stashItem struct {
	item
	value string
}

type stashDoc struct {
	keys  []string // 键第一次出现的顺序
	items map[string][]stashItem
}

var stashHeaderOps = map[string]struct{ direction, op string }{
	"request-add":      {"request", "add"},
	"request-del":      {"request", "remove"},
	"request-replace":  {"request", "set"},
	"response-add":     {"response", "add"},
	"response-del":     {"response", "remove"},
	"response-replace": {"response", "set"},
}

func stashShape(raw string, line int) error {
	it := item{line: line, text: raw}
	if strings.Contains(raw, "\t") {
		return perr("stash_tab_indent", it)
	}
	t := trimJS(raw)
	if stashAnchor.MatchString(t) {
		return perr("stash_anchor_unsupported", it)
	}
	if stashBlockScalar.MatchString(t) {
		return perr("stash_block_scalar_unsupported", it)
	}
	return nil
}

// stashScalar 取列表项的值:成对的首尾引号去掉;引号没收口、`[` `{` 开头(集合写法)失败。
func stashScalar(s string, it item) (string, error) {
	n := trimJS(s)
	if len(n) >= 2 && (n[0] == '"' || n[0] == '\'') && n[len(n)-1] == n[0] {
		return n[1 : len(n)-1], nil
	}
	if strings.HasPrefix(n, `"`) || strings.HasPrefix(n, "'") {
		return "", perr("stash_unterminated_quote", it)
	}
	if strings.HasPrefix(n, "[") || strings.HasPrefix(n, "{") {
		return "", perr("stash_flow_collection_unsupported", it)
	}
	return n, nil
}

func parseStashDoc(text string) (*stashDoc, error) {
	doc := &stashDoc{items: map[string][]stashItem{}}
	inHTTP := false
	cur, hasCur := "", false
	for i, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		line := i + 1
		p := trimJS(raw)
		if p == "" || p[0] == '#' {
			continue
		}
		if err := stashShape(raw, line); err != nil {
			return nil, err
		}
		it := item{line: line, text: raw}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		isItem := strings.HasPrefix(p, "- ") || p == "-"
		if inHTTP && indent <= 0 && !isItem {
			inHTTP, hasCur = false, false
		}
		if !inHTTP {
			if m := stashHTTPKey.FindStringSubmatch(p); m != nil && indent == 0 {
				if !blank(m[1]) {
					return nil, perr("stash_flow_collection_unsupported", it)
				}
				inHTTP, hasCur = true, false
			}
			continue
		}
		if isItem {
			if !hasCur {
				return nil, perr("stash_item_outside_key", it)
			}
			rest := ""
			if p != "-" {
				rest = p[2:]
			}
			v, err := stashScalar(rest, it)
			if err != nil {
				return nil, err
			}
			if v == "" {
				return nil, perr("stash_empty_item", it)
			}
			doc.items[cur] = append(doc.items[cur], stashItem{it, v})
			continue
		}
		m := stashKey.FindStringSubmatch(p)
		if m == nil {
			return nil, perr("stash_unreadable_line", it)
		}
		key := strings.ToLower(m[1])
		if !blank(m[2]) {
			return nil, perr("stash_inline_value_unsupported", it)
		}
		cur, hasCur = key, true
		if _, ok := doc.items[key]; !ok {
			doc.items[key] = nil
			doc.keys = append(doc.keys, key)
		}
	}
	return doc, nil
}

func parseStash(text string) ([]Rule, error) {
	doc, err := parseStashDoc(text)
	if err != nil {
		return nil, err
	}
	var out []Rule
	for _, key := range doc.keys {
		items := doc.items[key]
		switch key {
		case "url-rewrite":
			for _, s := range items {
				r, err := stashURLRewrite(s)
				if err != nil {
					return nil, err
				}
				out = append(out, r)
			}
		case "header-rewrite":
			for _, s := range items {
				r, err := stashHeaderRewrite(s)
				if err != nil {
					return nil, err
				}
				out = append(out, r)
			}
		case "mitm":
		default:
			it := item{text: key}
			if len(items) > 0 {
				it = items[0].item
			}
			return nil, perr("stash_unsupported_http_key:"+key, it)
		}
	}
	return out, nil
}

func stashURLRewrite(s stashItem) (Rule, error) {
	v := strings.Fields(s.value)
	if len(v) < 3 {
		return Rule{}, perr("stash_url_rewrite_too_short", s.item)
	}
	pattern, verb := v[0], strings.ToLower(v[len(v)-1])
	if err := checkRegex(pattern, "pattern", s.item); err != nil {
		return Rule{}, err
	}
	id := ruleID(Stash, s.text, s.line)
	if rj, ok := rejectKinds[verb]; ok {
		if rj.contentType != "" {
			return Rule{ID: id, Dialect: Stash, Pattern: pattern, Action: "mapLocal",
				Params: map[string]any{"contentType": rj.contentType, "body": rj.body}}, nil
		}
		return Rule{ID: id, Dialect: Stash, Pattern: pattern, Action: "reject",
			Params: map[string]any{"status": rj.status, "body": rj.body, "delayPolicy": "none"}}, nil
	}
	if verb == "302" || verb == "307" {
		target := v[1]
		if target == "-" {
			return Rule{}, perr("stash_url_rewrite_missing_target", s.item)
		}
		return Rule{ID: id, Dialect: Stash, Pattern: pattern, Action: "redirect",
			Params: map[string]any{"status": redirectStatus(verb), "target": target}}, nil
	}
	if verb == "transparent" {
		return Rule{}, perr("stash_url_rewrite_transparent_unsupported", s.item)
	}
	return Rule{}, perr("stash_url_rewrite_verb_unsupported:"+verb, s.item)
}

func stashHeaderRewrite(s stashItem) (Rule, error) {
	v := strings.Fields(s.value)
	if len(v) < 3 {
		return Rule{}, perr("stash_header_rewrite_too_short", s.item)
	}
	pattern, verb := v[0], strings.ToLower(v[1])
	if err := checkRegex(pattern, "pattern", s.item); err != nil {
		return Rule{}, err
	}
	h, ok := stashHeaderOps[verb]
	if !ok {
		if strings.HasSuffix(verb, "-replace-regex") {
			return Rule{}, perr("stash_header_replace_regex_unsupported:"+verb, s.item)
		}
		return Rule{}, perr("stash_header_op_unsupported:"+verb, s.item)
	}
	value := ""
	if h.op != "remove" {
		value = strings.Join(v[3:], " ")
		if value == "" {
			return Rule{}, perr("stash_header_missing_value", s.item)
		}
	}
	return Rule{ID: ruleID(Stash, s.text, s.line), Dialect: Stash, Pattern: pattern, Action: "header",
		Params: map[string]any{"direction": h.direction, "op": h.op, "name": v[2], "value": value}}, nil
}
