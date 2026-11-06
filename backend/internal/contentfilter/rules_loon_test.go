package contentfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

const loonPat = `^https://a\.com/api`

func loonText(section, lines string) string { return "[" + section + "]\n" + lines }

func TestLoonRewriteReject(t *testing.T) {
	rs := mustParse(t, Loon, loonText("Rewrite", loonPat+" reject"))
	if len(rs) != 1 || rs[0].Action != "drop" || rs[0].Params != nil || rs[0].Dialect != Loon {
		t.Fatalf("%+v", rs)
	}
	cases := map[string]map[string]any{
		"reject-200":   {"status": 200, "body": "", "delayPolicy": "none"},
		"reject-img":   {"status": 200, "body": tinyGIF, "delayPolicy": "none"},
		"reject-dict":  {"status": 200, "body": "e30=", "delayPolicy": "none"},
		"reject-array": {"status": 200, "body": "W10=", "delayPolicy": "none"},
	}
	for verb, params := range cases {
		// 动词大小写不敏感;reject-img 是带 GIF 体的 reject,不是 mapLocal
		wantOne(t, mustParse(t, Loon, loonText("rewrite", loonPat+" "+verb)), loonPat, "reject", params)
	}
	// 同一个 params 表不能被两条规则共用
	a := mustParse(t, Loon, loonText("Rewrite", loonPat+" reject-200"))[0]
	a.Params["status"] = 999
	wantOne(t, mustParse(t, Loon, loonText("Rewrite", loonPat+" reject-200")), loonPat, "reject",
		map[string]any{"status": 200, "body": "", "delayPolicy": "none"})
}

func TestLoonRewriteRedirectAndHeader(t *testing.T) {
	wantOne(t, mustParse(t, Loon, loonText("Rewrite", loonPat+" 302 https://b.com/x")), loonPat, "redirect",
		map[string]any{"status": 302, "target": "https://b.com/x"})
	wantOne(t, mustParse(t, Loon, loonText("Rewrite", loonPat+" 307 https://b.com/x")), loonPat, "redirect",
		map[string]any{"status": 307, "target": "https://b.com/x"})
	cases := []struct {
		line, dir, op, name, value string
	}{
		{"header-add X-A 1", "request", "add", "X-A", "1"},
		{"header-replace User-Agent Mozilla/5.0 (iPhone)", "request", "set", "User-Agent", "Mozilla/5.0 (iPhone)"},
		{"header-del Cookie", "request", "remove", "Cookie", ""},
		{"response-header-add X-A 1", "response", "add", "X-A", "1"},
		{"response-header-replace Cache-Control no-store", "response", "set", "Cache-Control", "no-store"},
		{"response-header-del Set-Cookie", "response", "remove", "Set-Cookie", ""},
	}
	for _, c := range cases {
		wantOne(t, mustParse(t, Loon, loonText("Rewrite", loonPat+" "+c.line)), loonPat, "header",
			map[string]any{"direction": c.dir, "op": c.op, "name": c.name, "value": c.value})
	}
}

func TestLoonURLRewrite(t *testing.T) {
	r := mustParse(t, Loon, loonText("URL Rewrite", loonPat+" - reject"))
	if len(r) != 1 || r[0].Action != "drop" {
		t.Fatalf("%+v", r)
	}
	wantOne(t, mustParse(t, Loon, loonText("URL Rewrite", loonPat+" https://b.com/ 302")), loonPat, "redirect",
		map[string]any{"status": 302, "target": "https://b.com/"})
}

func TestLoonScript(t *testing.T) {
	line := "http-response " + loonPat + ` script-path=https://x.com/a.js, requires-body=true, timeout=30, max-size=2048, argument=a,b, binary-body-mode=1, tag=demo`
	wantOne(t, mustParse(t, Loon, loonText("Script", line)), loonPat, "script", map[string]any{
		"phase": "response", "sourceUrl": "https://x.com/a.js", "sha256": "", "requiresBody": true,
		"timeoutMs": int64(30000), "maxBytes": int64(2048), "argument": "a,b", "binaryBodyMode": true})
	wantOne(t, mustParse(t, Loon, loonText("Script", "http-request "+loonPat+" script-path=https://x.com/a.js")), loonPat, "script",
		map[string]any{"phase": "request", "sourceUrl": "https://x.com/a.js", "sha256": "", "requiresBody": false})
}

func TestLoonIgnoredAndErrors(t *testing.T) {
	// MITM 与 Argument 整段忽略
	text := "[Argument]\nk = input,\"v\"\n[MITM]\nhostname = a.com\n[Rewrite]\n" + loonPat + " reject"
	if rs := mustParse(t, Loon, text); len(rs) != 1 {
		t.Fatalf("%+v", rs)
	}
	for _, c := range []struct {
		text, reason string
		line         int
	}{
		{"[Rule]\nDOMAIN,a.com,REJECT", "loon_unsupported_section:rule", 2},
		{loonPat + " reject", "loon_line_outside_section", 1},
		{loonText("Rewrite", loonPat), "loon_rewrite_too_short", 2},
		{loonText("Rewrite", loonPat+" 302"), "loon_redirect_missing_target", 2},
		{loonText("Rewrite", loonPat+" header-add"), "loon_header_missing_name", 2},
		{loonText("Rewrite", loonPat+" header-add X-A"), "loon_header_missing_value", 2},
		{loonText("Rewrite", loonPat+" mock-response"), "loon_unknown_verb:mock-response", 2},
		{loonText("URL Rewrite", loonPat), "loon_url_rewrite_too_short", 2},
		{loonText("URL Rewrite", loonPat+" - header"), "loon_url_rewrite_header_unsupported", 2},
		{loonText("URL Rewrite", loonPat+" 302"), "loon_url_rewrite_missing_target", 2},
		{loonText("Script", "cron \"0 0 * * *\" script-path=a.js"), "loon_script_unsupported_type:cron", 2},
		{loonText("Script", "http-response "+loonPat), "loon_script_no_path", 2},
		{loonText("Rewrite", `^https://a\.com/(?=x) reject`), "regex_unsupported_by_re2:pattern:invalid or unsupported Perl syntax", 2},
		{loonText("Rewrite", `"^https://a reject`), "unterminated_quote", 2},
	} {
		reason, line := parseReason(t, Loon, c.text)
		if line != c.line {
			t.Errorf("%q: 行号 %d,想要 %d", c.text, line, c.line)
		}
		if c.reason[:5] == "regex" {
			if len(reason) < 33 || reason[:33] != "regex_unsupported_by_re2:pattern:" {
				t.Errorf("正则原因码 %q", reason)
			}
			continue
		}
		if reason != c.reason {
			t.Errorf("%q: 原因 %q,想要 %q", c.text, reason, c.reason)
		}
	}
}

func TestLoonSkip(t *testing.T) {
	text := "[Rewrite]\n" + loonPat + " reject\n" + `^https://a\.com/(?=x) reject` + "\n[Script]\nhttp-response " + loonPat + " requires-body=true\ncron \"0 0 * * *\" script-path=a.js\n"
	var skipped []string
	rs, err := ParseRules(Loon, text, func(reason string, line int) { skipped = append(skipped, fmt.Sprintf("%d:%s", line, reason)) })
	// cron 不是 loon_script_ 之外的原因吗?它是 loon_script_unsupported_type,也能跳过
	if err != nil || len(rs) != 1 || len(skipped) != 3 {
		t.Fatalf("rs=%d skipped=%v err=%v", len(rs), skipped, err)
	}
	// [Rule] 不在可跳过之列,即使给了 skip 也要失败
	if _, err := ParseRules(Loon, "[Rule]\nDOMAIN,a.com,REJECT", func(string, int) {}); err == nil {
		t.Fatal("[Rule] 应当报错")
	}
}

func TestLoonRuleID(t *testing.T) {
	line := loonPat + " reject"
	r := mustParse(t, Loon, loonText("Rewrite", line))[0]
	sum := sha256.Sum256([]byte("loon|2|" + line))
	if want := "r-" + hex.EncodeToString(sum[:])[:8]; r.ID != want {
		t.Fatalf("id %s,想要 %s", r.ID, want)
	}
}
