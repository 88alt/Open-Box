package contentfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const qxPat = `^https://a\.com/api`

func TestQXReject(t *testing.T) {
	cases := map[string]map[string]any{
		"reject":       {"status": 404, "body": "", "delayPolicy": "escalating"},
		"reject-200":   {"status": 200, "body": "", "delayPolicy": "none"},
		"reject-img":   {"status": 200, "body": tinyGIF, "delayPolicy": "none"},
		"reject-dict":  {"status": 200, "body": "e30=", "delayPolicy": "none"},
		"reject-array": {"status": 200, "body": "W10=", "delayPolicy": "none"},
	}
	for verb, params := range cases {
		wantOne(t, mustParse(t, QuantumultX, qxPat+" url "+verb), qxPat, "reject", params)
	}
	// 动词大小写不敏感;reject-drop 是 drop 动作,没有 params
	rs := mustParse(t, QuantumultX, qxPat+" URL REJECT-DROP")
	if len(rs) != 1 || rs[0].Action != "drop" || rs[0].Params != nil || rs[0].Dialect != QuantumultX {
		t.Fatalf("%+v", rs)
	}
}

func TestQXRedirect(t *testing.T) {
	wantOne(t, mustParse(t, QuantumultX, qxPat+" url 302 https://b.com/x"), qxPat, "redirect", map[string]any{"status": 302, "target": "https://b.com/x"})
	wantOne(t, mustParse(t, QuantumultX, qxPat+" url 307 https://b.com/x"), qxPat, "redirect", map[string]any{"status": 307, "target": "https://b.com/x"})
}

func TestQXReplace(t *testing.T) {
	// 查找、替换都是原文:带空格、带引号都照留
	wantOne(t, mustParse(t, QuantumultX, qxPat+` url response-body "ads": \[.*?\] response-body "ads": [] `), qxPat, "replace",
		map[string]any{"search": `"ads": \[.*?\]`, "replacement": `"ads": []`, "maxBytes": 512 * 1024})
	// 替换可以是空的
	wantOne(t, mustParse(t, QuantumultX, qxPat+" url response-body foo response-body"), qxPat, "replace",
		map[string]any{"search": "foo", "replacement": "", "maxBytes": 512 * 1024})
}

func TestQXScript(t *testing.T) {
	wantOne(t, mustParse(t, QuantumultX, qxPat+" url script-response-body https://s.example/x.js#k=v&a=1"), qxPat, "script",
		map[string]any{"phase": "response", "sourceUrl": "https://s.example/x.js", "sha256": "", "requiresBody": true, "argument": "k=v&a=1"})
	wantOne(t, mustParse(t, QuantumultX, qxPat+" url script-request-header https://s.example/x.js"), qxPat, "script",
		map[string]any{"phase": "request", "sourceUrl": "https://s.example/x.js", "sha256": "", "requiresBody": false})
	// # 后面是空的:没有 argument
	wantOne(t, mustParse(t, QuantumultX, qxPat+" url script-request-body https://s.example/x.js#"), qxPat, "script",
		map[string]any{"phase": "request", "sourceUrl": "https://s.example/x.js", "sha256": "", "requiresBody": true})
	for verb, want := range map[string]map[string]any{
		"script-response-header":       {"phase": "response", "requiresBody": false},
		"script-echo-response":         {"phase": "request", "requiresBody": false},
		"script-analyze-echo-response": {"phase": "request", "requiresBody": true},
	} {
		rs := mustParse(t, QuantumultX, qxPat+" url "+verb+" https://s.example/x.js")
		if rs[0].Params["phase"] != want["phase"] || rs[0].Params["requiresBody"] != want["requiresBody"] {
			t.Errorf("%s: %#v", verb, rs[0].Params)
		}
	}
}

func TestQXErrors(t *testing.T) {
	for _, c := range []struct{ text, reason string }{
		{"onlyone", "quantumultx_missing_match_type"},
		{qxPat + " reject", "quantumultx_missing_match_type"},
		{qxPat + " extra url reject", "quantumultx_pattern_field_count:2"},
		{qxPat + " url-and-header reject", "quantumultx_pattern_field_count:1"},
		{qxPat + " x url-and-header reject", "quantumultx_url_and_header_unsupported"},
		{"(?=x) url reject", "regex_unsupported_by_re2:pattern:"},
		{qxPat + " url", "quantumultx_missing_verb"},
		{qxPat + " url 302", "quantumultx_redirect_missing_target"},
		{qxPat + " url response-body foo", "quantumultx_replace_missing_separator"},
		{qxPat + " url response-body response-body bar", "quantumultx_replace_missing_search"},
		{qxPat + " url response-body (?=a) response-body b", "regex_unsupported_by_re2:search:"},
		{qxPat + " url request-body a request-body b", "quantumultx_request_body_unsupported"},
		{qxPat + " url echo-response text/html echo-response body", "quantumultx_echo_response_unsupported"},
		{qxPat + " url request-header (\\r\\n)User-Agent:.+(\\r\\n) request-header $1", "quantumultx_raw_header_rewrite_unsupported:request-header"},
		{qxPat + " url response-header a response-header b", "quantumultx_raw_header_rewrite_unsupported:response-header"},
		{qxPat + " url script-response-body", "quantumultx_script_no_path"},
		{qxPat + " url script-response-body #arg", "quantumultx_script_no_path"},
		{qxPat + " url script-bogus x.js", "quantumultx_script_unsupported:script-bogus"},
		{qxPat + " url frobnicate", "quantumultx_unknown_verb:frobnicate"},
	} {
		reason, line := parseReason(t, QuantumultX, c.text)
		if !strings.HasPrefix(reason, c.reason) || line != 1 {
			t.Errorf("%q: 得到 %q 第 %d 行,想要 %q", c.text, reason, line, c.reason)
		}
	}
}

func TestQXSectionsAndSkip(t *testing.T) {
	text := strings.Join([]string{
		"hostname = a.com, b.com", // 没有段头时,hostname 行不是规则
		"[rewrite_local]",
		"// 注释",
		qxPat + " url reject",
		"HOSTNAME=c.com",
		qxPat + " url request-body a request-body b",
		qxPat + " url script-bogus x.js",
		"(?=x) url reject",
		"[mitm]",
		"hostname = a.com",
		"[rewrite_local]",
		qxPat + " url reject-drop",
	}, "\n")
	var skipped []string
	rs, err := ParseRules(QuantumultX, text, func(reason string, line int) {
		skipped = append(skipped, fmt.Sprintf("%d:%s", line, strings.SplitN(reason, ":", 2)[0]))
	})
	if err != nil || len(rs) != 2 || rs[0].Action != "reject" || rs[1].Action != "drop" {
		t.Fatalf("%v %+v", err, rs)
	}
	want := []string{"6:quantumultx_request_body_unsupported", "7:quantumultx_script_unsupported", "8:regex_unsupported_by_re2"}
	if !reflect.DeepEqual(skipped, want) {
		t.Fatalf("跳过的行 %v,想要 %v", skipped, want)
	}
	// 同样的文本,没有回调:第一个不支持的就失败
	if reason, line := parseReason(t, QuantumultX, text); reason != "quantumultx_request_body_unsupported" || line != 6 {
		t.Fatalf("%q %d", reason, line)
	}
	// 别的段不认(filter_local / task_local 等),哪怕有回调也失败
	for _, sec := range []string{"filter_local", "task_local", "rewrite_remote"} {
		if _, err := ParseRules(QuantumultX, "["+sec+"]\nx", func(string, int) { t.Error("不该跳过") }); err == nil ||
			!strings.Contains(err.Error(), "quantumultx_unsupported_section:"+sec) {
			t.Errorf("%s: %v", sec, err)
		}
	}
	// 位置不对的 url 关键字、规则 id 的算法
	rs = mustParse(t, QuantumultX, "[rewrite_local]\n"+qxPat+" url reject")
	sum := sha256.Sum256([]byte("quantumultx|2|" + qxPat + " url reject"))
	if want := "r-" + hex.EncodeToString(sum[:])[:8]; rs[0].ID != want {
		t.Fatalf("id %s,想要 %s", rs[0].ID, want)
	}
}
