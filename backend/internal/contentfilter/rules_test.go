package contentfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, d Dialect, text string) []Rule {
	t.Helper()
	rs, err := ParseRules(d, text, nil)
	if err != nil {
		t.Fatalf("解析失败: %v\n%s", err, text)
	}
	return rs
}

func parseReason(t *testing.T, d Dialect, text string) (string, int) {
	t.Helper()
	_, err := ParseRules(d, text, nil)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("应当解析失败,得到 %v\n%s", err, text)
	}
	return pe.Reason, pe.Line
}

func wantOne(t *testing.T, rs []Rule, pattern, action string, params map[string]any) {
	t.Helper()
	if len(rs) != 1 {
		t.Fatalf("规则数 %d: %+v", len(rs), rs)
	}
	r := rs[0]
	if r.Pattern != pattern || r.Action != action || !reflect.DeepEqual(r.Params, params) {
		t.Fatalf("得到 %s %s %#v\n想要 %s %s %#v", r.Pattern, r.Action, r.Params, pattern, action, params)
	}
}

func TestURLRewrite(t *testing.T) {
	const pat = `^https?://ad\.example\.com/`
	wantOne(t, mustParse(t, Surge, "[URL Rewrite]\n"+pat+" - reject"), pat, "reject",
		map[string]any{"status": 404, "body": "", "delayPolicy": "none"})
	wantOne(t, mustParse(t, Surge, "[URL Rewrite]\n"+pat+" - REJECT-200"), pat, "reject",
		map[string]any{"status": 200, "body": "", "delayPolicy": "none"})
	wantOne(t, mustParse(t, Surge, "[URL Rewrite]\n"+pat+" - reject-img"), pat, "mapLocal",
		map[string]any{"contentType": "image/gif", "body": tinyGIF})
	wantOne(t, mustParse(t, Surge, "[URL Rewrite]\n"+pat+" - reject-dict"), pat, "mapLocal",
		map[string]any{"contentType": "application/json", "body": "e30="})
	wantOne(t, mustParse(t, Surge, "[URL Rewrite]\n"+pat+" - reject-array"), pat, "mapLocal",
		map[string]any{"contentType": "application/json", "body": "W10="})
	wantOne(t, mustParse(t, Shadowrocket, "[URL Rewrite]\n^https://a\\.com/(.*) https://b.com/$1 302"), `^https://a\.com/(.*)`, "redirect",
		map[string]any{"status": 302, "target": "https://b.com/$1"})
	wantOne(t, mustParse(t, Surge, "[URL Rewrite]\n^https://a\\.com/ https://b.com/ 307"), `^https://a\.com/`, "redirect",
		map[string]any{"status": 307, "target": "https://b.com/"})

	for _, c := range []struct{ text, reason string }{
		{"[URL Rewrite]\nonlyone", "surge_url_rewrite_too_short"},
		{"[URL Rewrite]\n^https://a\\.com/ 302", "surge_url_rewrite_missing_target"},
		{"[URL Rewrite]\n^https://a\\.com/ header", "surge_url_rewrite_header_unsupported"},
		{"[URL Rewrite]\n^https://a\\.com/ - reject-tinygif", "surge_url_rewrite_header_unsupported"},
		{"[URL Rewrite]\n\"\" - reject", "surge_url_rewrite_pattern_blank"},
		{"[URL Rewrite]\n^a\" - reject", "unterminated_quote"},
		{"[URL Rewrite]\n(?=x) - reject", "regex_unsupported_by_re2:pattern:"},
	} {
		reason, line := parseReason(t, Surge, c.text)
		if !strings.HasPrefix(reason, c.reason) || line != 2 {
			t.Errorf("%q: 得到 %q 第 %d 行,想要 %q 第 2 行", c.text, reason, line, c.reason)
		}
	}
}

func TestHeaderRewrite(t *testing.T) {
	const pat = `^https://a\.com/`
	wantOne(t, mustParse(t, Surge, "[Header Rewrite]\nhttp-request "+pat+" header-replace User-Agent Foo Bar/1.0"), pat, "header",
		map[string]any{"direction": "request", "op": "set", "name": "User-Agent", "value": "Foo Bar/1.0"})
	wantOne(t, mustParse(t, Surge, "[Header Rewrite]\n"+pat+" http-response header-del Set-Cookie"), pat, "header",
		map[string]any{"direction": "response", "op": "remove", "name": "Set-Cookie", "value": ""})
	wantOne(t, mustParse(t, Surge, "[Header Rewrite]\n"+pat+" header-add X-Test 1"), pat, "header",
		map[string]any{"direction": "request", "op": "add", "name": "X-Test", "value": "1"})
	for _, c := range []struct{ text, reason string }{
		{"[Header Rewrite]\nhttp-request", "surge_header_rewrite_too_short"},
		{"[Header Rewrite]\n" + pat + " header-bogus X 1", "surge_header_op_unsupported:header-bogus"},
		{"[Header Rewrite]\n" + pat, "surge_header_op_unsupported:"},
		{"[Header Rewrite]\n" + pat + " header-add", "surge_header_missing_name"},
		{"[Header Rewrite]\n" + pat + " header-add X-Test", "surge_header_missing_value"},
	} {
		if reason, _ := parseReason(t, Surge, c.text); reason != c.reason {
			t.Errorf("%q: 得到 %q,想要 %q", c.text, reason, c.reason)
		}
	}
}

func TestMapLocal(t *testing.T) {
	const pat = `^https://a\.com/x`
	wantOne(t, mustParse(t, Surge, "[Map Local]\n"+pat+` data="hello world" header="Content-Type: application/json"`), pat, "mapLocal",
		map[string]any{"contentType": "application/json", "body": "aGVsbG8gd29ybGQ="})
	wantOne(t, mustParse(t, Surge, "[Map Local]\n"+pat+" data=hi"), pat, "mapLocal",
		map[string]any{"contentType": "text/plain; charset=utf-8", "body": "aGk="})
	wantOne(t, mustParse(t, Surge, "[Map Local]\n"+pat+" data-type=base64 data=AAEC status-code=200"), pat, "mapLocal",
		map[string]any{"contentType": "application/octet-stream", "body": "AAEC"})
	wantOne(t, mustParse(t, Surge, "[Map Local]\n"+pat+" data-type=tiny-gif"), pat, "mapLocal",
		map[string]any{"contentType": "image/gif", "body": tinyGIF})
	for _, c := range []struct{ text, reason string }{
		{"[Map Local]\n" + pat + " data", "surge_map_local_bad_option:data"},
		{"[Map Local]\n" + pat + " status-code=404", "surge_map_local_status_unsupported:404"},
		{"[Map Local]\n" + pat + " data-type=base64 data=AAE", "surge_map_local_bad_base64"},
		{"[Map Local]\n" + pat + " data-type=file data=x", "surge_map_local_data_type_unsupported:file"},
	} {
		if reason, _ := parseReason(t, Surge, c.text); reason != c.reason {
			t.Errorf("%q: 得到 %q,想要 %q", c.text, reason, c.reason)
		}
	}
}

func TestBodyRewrite(t *testing.T) {
	// 引号只用来包住带空格的词,不留在词里(跟上游一样):"ads":\[...\] 读成 ads:\[...\]
	rs := mustParse(t, Surge, `[Body Rewrite]
http-response ^https://a\.com/ "ads":\[.*?\] "ads":[] foo bar`)
	if len(rs) != 2 || rs[0].Action != "replace" || rs[1].Action != "replace" || rs[0].ID == rs[1].ID {
		t.Fatalf("%+v", rs)
	}
	if !reflect.DeepEqual(rs[0].Params, map[string]any{"search": `ads:\[.*?\]`, "replacement": `ads:[]`, "maxBytes": 512 * 1024}) ||
		!reflect.DeepEqual(rs[1].Params, map[string]any{"search": "foo", "replacement": "bar", "maxBytes": 512 * 1024}) {
		t.Fatalf("%#v %#v", rs[0].Params, rs[1].Params)
	}
	for _, c := range []struct{ text, reason string }{
		{"[Body Rewrite]\nhttp-request ^x a b", "surge_body_rewrite_direction_unsupported:http-request"},
		{"[Body Rewrite]\nhttp-response", "surge_body_rewrite_too_short"},
		{"[Body Rewrite]\nhttp-response ^x a", "surge_body_rewrite_unpaired"},
		{"[Body Rewrite]\nhttp-response ^x", "surge_body_rewrite_unpaired"},
		{`[Body Rewrite]` + "\n" + `http-response ^x "" b`, "surge_body_rewrite_missing_search"},
		{"[Body Rewrite]\nhttp-response ^x (?=a) b", "regex_unsupported_by_re2:search:"},
	} {
		if reason, _ := parseReason(t, Surge, c.text); !strings.HasPrefix(reason, c.reason) {
			t.Errorf("%q: 得到 %q,想要 %q", c.text, reason, c.reason)
		}
	}
}

func TestScript(t *testing.T) {
	rs := mustParse(t, Surge, `[Script]
x = type=http-response, pattern=^https://a\.com/api, script-path=https://s.example/x.js, requires-body=1, timeout=5, max-size=1048576, argument="a=1,b=2", binary-body-mode=true, debug=1`)
	wantOne(t, rs, `^https://a\.com/api`, "script", map[string]any{
		"phase": "response", "sourceUrl": "https://s.example/x.js", "sha256": "", "requiresBody": true,
		"timeoutMs": int64(5000), "maxBytes": int64(1048576), "argument": "a=1,b=2", "binaryBodyMode": true,
	})
	rs = mustParse(t, Shadowrocket, `[Script]
y = type=http-request, pattern=^https://b\.com/, script-path=https://s.example/y.js, timeout=3000`)
	wantOne(t, rs, `^https://b\.com/`, "script", map[string]any{
		"phase": "request", "sourceUrl": "https://s.example/y.js", "sha256": "", "requiresBody": false, "timeoutMs": int64(3000),
	})
	for _, c := range []struct{ text, reason string }{
		{"[Script]\n= type=http-request", "surge_script_no_name"},
		{"[Script]\nx = type=cron, script-path=a", "surge_script_unsupported_type:cron"},
		{"[Script]\nx = type=http-request, script-path=a", "surge_script_pattern_blank"},
		{"[Script]\nx = type=http-request, pattern=^a", "surge_script_no_path"},
		{"[Script]\nx = type=http-request, pattern=(?=a), script-path=a", "regex_unsupported_by_re2:pattern:"},
	} {
		if reason, _ := parseReason(t, Surge, c.text); !strings.HasPrefix(reason, c.reason) {
			t.Errorf("%q: 得到 %q,想要 %q", c.text, reason, c.reason)
		}
	}
}

func TestRoutingAndSkip(t *testing.T) {
	text := "[Rule]\nDOMAIN-SUFFIX,ad.com,REJECT\nPROTOCOL,QUIC,REJECT\n[URL Rewrite]\n^https://a\\.com/ - reject\n[Script]\nx = type=cron, script-path=a\n[Body Rewrite]\nhttp-response (?=x) a b"
	if reason, line := parseReason(t, Surge, text); reason != "surge_routing_unsupported" || line != 2 {
		t.Fatalf("%q %d", reason, line)
	}
	var skipped []string
	rs, err := ParseRules(Surge, text, func(reason string, line int) {
		skipped = append(skipped, fmt.Sprintf("%d:%s", line, strings.SplitN(reason, ":", 3)[0]))
	})
	if err != nil || len(rs) != 1 || rs[0].Action != "reject" {
		t.Fatalf("%v %+v", err, rs)
	}
	want := []string{"2:surge_routing_unsupported", "3:surge_routing_quic_covered", "7:surge_script_unsupported_type", "9:regex_unsupported_by_re2"}
	if !reflect.DeepEqual(skipped, want) {
		t.Fatalf("跳过的行: %v,想要 %v", skipped, want)
	}
	// 不在跳过之列的失败,有回调也照样失败
	if _, err := ParseRules(Surge, "[URL Rewrite]\nonlyone", func(string, int) { t.Error("不该跳过") }); err == nil {
		t.Fatal("too_short 应当失败")
	}
}

func TestSectionsAndIDs(t *testing.T) {
	// MITM 段整段忽略;注释、空行、BOM、CRLF 不影响行号
	text := "\ufeff#!name=x\r\n[MITM]\r\nhostname = a.com\r\n\r\n; c\r\n// c\r\n[ URL Rewrite ]\r\n^https://a\\.com/ - reject\r\n"
	rs := mustParse(t, Surge, text)
	if len(rs) != 1 {
		t.Fatalf("%+v", rs)
	}
	sum := sha256.Sum256([]byte("surge|8|^https://a\\.com/ - reject"))
	if want := "r-" + hex.EncodeToString(sum[:])[:8]; rs[0].ID != want {
		t.Fatalf("id %s,想要 %s", rs[0].ID, want)
	}
	if rs[0].Dialect != Surge {
		t.Fatal(rs[0].Dialect)
	}
	if again := mustParse(t, Shadowrocket, text); again[0].ID == rs[0].ID || again[0].Dialect != Shadowrocket {
		t.Fatal("方言不同,id 也该不同")
	}
	for _, c := range []struct{ text, reason string }{
		{"^https://a\\.com/ - reject", "surge_line_outside_section"},
		{"[General]\nx=1", "surge_unsupported_section:general"},
	} {
		if reason, line := parseReason(t, Surge, c.text); reason != c.reason || line != strings.Count(c.text, "\n")+1 {
			t.Errorf("%q: %q %d", c.text, reason, line)
		}
	}
	if rs := mustParse(t, Surge, "# nothing\n\n"); len(rs) != 0 {
		t.Fatal(rs)
	}
}

func TestParseRulesOtherDialects(t *testing.T) {
	for _, d := range []Dialect{"nope"} {
		if _, err := ParseRules(d, "x", nil); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", d, err)
		}
	}
}
