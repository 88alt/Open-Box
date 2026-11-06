package contentfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

const stashPat = `^https://a\.com/api`

func stashDocText(key string, lines ...string) string {
	s := "name: demo\nhttp:\n  " + key + ":\n"
	for _, l := range lines {
		s += "    - " + l + "\n"
	}
	return s
}

func TestStashURLRewrite(t *testing.T) {
	cases := map[string]struct {
		action string
		params map[string]any
	}{
		"reject":       {"reject", map[string]any{"status": 404, "body": "", "delayPolicy": "none"}},
		"reject-200":   {"reject", map[string]any{"status": 200, "body": "", "delayPolicy": "none"}},
		"reject-img":   {"mapLocal", map[string]any{"contentType": "image/gif", "body": tinyGIF}},
		"reject-dict":  {"mapLocal", map[string]any{"contentType": "application/json", "body": "e30="}},
		"reject-array": {"mapLocal", map[string]any{"contentType": "application/json", "body": "W10="}},
	}
	for verb, c := range cases {
		wantOne(t, mustParse(t, Stash, stashDocText("url-rewrite", stashPat+" - "+verb)), stashPat, c.action, c.params)
	}
	wantOne(t, mustParse(t, Stash, stashDocText("url-rewrite", stashPat+" https://b.com/x 302")), stashPat, "redirect",
		map[string]any{"status": 302, "target": "https://b.com/x"})
	wantOne(t, mustParse(t, Stash, stashDocText("url-rewrite", `"`+stashPat+` https://b.com/x 307"`)), stashPat, "redirect",
		map[string]any{"status": 307, "target": "https://b.com/x"})
}

func TestStashHeaderRewrite(t *testing.T) {
	cases := []struct{ line, dir, op, name, value string }{
		{"request-add X-A 1", "request", "add", "X-A", "1"},
		{"request-replace User-Agent Mozilla/5.0 (iPhone)", "request", "set", "User-Agent", "Mozilla/5.0 (iPhone)"},
		{"request-del Cookie", "request", "remove", "Cookie", ""},
		{"response-add X-A 1", "response", "add", "X-A", "1"},
		{"response-replace Cache-Control no-store", "response", "set", "Cache-Control", "no-store"},
		{"response-del Set-Cookie", "response", "remove", "Set-Cookie", ""},
	}
	for _, c := range cases {
		wantOne(t, mustParse(t, Stash, stashDocText("header-rewrite", stashPat+" "+c.line)), stashPat, "header",
			map[string]any{"direction": c.dir, "op": c.op, "name": c.name, "value": c.value})
	}
}

func TestStashDocumentShape(t *testing.T) {
	text := "# 注释\nname: demo\nscript-providers:\n  x:\n    url: https://x\nhttp:\n  mitm:\n    - \"a.com\"\n  header-rewrite:\n    - " + stashPat + " request-add X-A 1\n  url-rewrite:\n    - " + stashPat + " - reject\n\nrules:\n  - DOMAIN,a.com,DIRECT\n"
	rs := mustParse(t, Stash, text)
	// 键按第一次出现的顺序处理:header-rewrite 在前
	if len(rs) != 2 || rs[0].Action != "header" || rs[1].Action != "reject" {
		t.Fatalf("%+v", rs)
	}
	// 块外的行(rules: 等)不看;`- ` 与 `http:` 同一缩进的列表项仍属于它
	if rs := mustParse(t, Stash, "http:\nurl-rewrite:\n"); len(rs) != 0 {
		t.Fatalf("%+v", rs)
	}
	if rs := mustParse(t, Stash, "nothing: here\n"); len(rs) != 0 {
		t.Fatalf("%+v", rs)
	}
	// 同一个键出现两次,条目接在一起
	rs = mustParse(t, Stash, "http:\n  url-rewrite:\n    - "+stashPat+" - reject\n  header-rewrite:\n    - "+stashPat+" request-del A\n  url-rewrite:\n    - "+stashPat+" - reject-200\n")
	if len(rs) != 3 || rs[1].Action != "reject" || rs[2].Action != "header" {
		t.Fatalf("%+v", rs)
	}
}

func TestStashRuleID(t *testing.T) {
	// id 取的是整行原文(带缩进和 "- ")+行号,不是去掉空白后的值
	raw := "    - " + stashPat + " - reject"
	r := mustParse(t, Stash, stashDocText("url-rewrite", stashPat+" - reject"))[0]
	sum := sha256.Sum256([]byte("stash|4|" + raw))
	if want := "r-" + hex.EncodeToString(sum[:])[:8]; r.ID != want {
		t.Fatalf("id %s,想要 %s", r.ID, want)
	}
}

func TestStashErrors(t *testing.T) {
	d := func(lines ...string) string { return stashDocText("url-rewrite", lines...) }
	h := func(lines ...string) string { return stashDocText("header-rewrite", lines...) }
	for _, c := range []struct {
		name, text, reason string
		line               int
	}{
		{"tab", "http:\n\turl-rewrite:\n", "stash_tab_indent", 2},
		{"anchor", "http:\n  url-rewrite: &a\n", "stash_anchor_unsupported", 2},
		{"alias", "http:\n  url-rewrite:\n    - *a\n", "stash_anchor_unsupported", 3},
		{"block scalar", "http:\n  url-rewrite: |\n", "stash_block_scalar_unsupported", 2},
		{"http inline", "http: {url-rewrite: []}\n", "stash_flow_collection_unsupported", 1},
		{"flow item", d("[a, b]"), "stash_flow_collection_unsupported", 4},
		{"unterminated", d(`"abc`), "stash_unterminated_quote", 4},
		{"empty item", "http:\n  url-rewrite:\n    -\n", "stash_empty_item", 3},
		{"empty quoted item", d(`""`), "stash_empty_item", 4},
		{"item outside key", "http:\n  - a b c\n", "stash_item_outside_key", 2},
		{"unreadable", "http:\n  !!!\n", "stash_unreadable_line", 2},
		{"inline value", "http:\n  url-rewrite: x\n", "stash_inline_value_unsupported", 2},
		{"unsupported key", "http:\n  script:\n    - a\n    - b\n", "stash_unsupported_http_key:script", 3},
		{"unsupported key without items", "http:\n  script:\n", "stash_unsupported_http_key:script", 0},
		{"url too short", d(stashPat + " reject"), "stash_url_rewrite_too_short", 4},
		{"url missing target", d(stashPat + " - 302"), "stash_url_rewrite_missing_target", 4},
		{"url transparent", d(stashPat + " https://b.com/ transparent"), "stash_url_rewrite_transparent_unsupported", 4},
		{"url verb", d(stashPat + " - header"), "stash_url_rewrite_verb_unsupported:header", 4},
		{"header too short", h(stashPat + " request-del"), "stash_header_rewrite_too_short", 4},
		{"header op", h(stashPat + " request-mock A B"), "stash_header_op_unsupported:request-mock", 4},
		{"header regex", h(stashPat + " request-replace-regex A B"), "stash_header_replace_regex_unsupported:request-replace-regex", 4},
		{"header value", h(stashPat + " request-add X-A"), "stash_header_missing_value", 4},
		{"regex", d(`^https://a\.com/(?=x) - reject`), "regex_unsupported_by_re2:pattern:", 4},
	} {
		reason, line := parseReason(t, Stash, c.text)
		if line != c.line {
			t.Errorf("%s: 行号 %d,想要 %d", c.name, line, c.line)
		}
		if c.reason == "regex_unsupported_by_re2:pattern:" {
			if len(reason) < len(c.reason) || reason[:len(c.reason)] != c.reason {
				t.Errorf("%s: 原因 %q", c.name, reason)
			}
			continue
		}
		if reason != c.reason {
			t.Errorf("%s: 原因 %q,想要 %q", c.name, reason, c.reason)
		}
	}
}

func TestStashNeverSkips(t *testing.T) {
	// 上游对 Stash 不传 skip 回调:正则不合格也是失败,回调不会被调用
	called := false
	_, err := ParseRules(Stash, stashDocText("url-rewrite", `^https://a\.com/(?=x) - reject`), func(string, int) { called = true })
	if err == nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
