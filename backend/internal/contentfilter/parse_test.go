package contentfilter

import "testing"

func TestCount(t *testing.T) {
	surgeModule := "\ufeff#!name=测试\r\n#!desc=说明\r\n[Rule]\r\nDOMAIN-SUFFIX,ads.example.com,REJECT\r\nURL-REGEX,^https?://x/,REJECT\r\nbogus line\r\n" +
		"[URL Rewrite]\n^https?://a/ - reject\n^http://b/ https://b/ 302\nnot a rewrite\n" +
		"[Header Rewrite]\nhttp-request ^https://c/ header-del Cookie\nbad\n" +
		"[Body Rewrite]\nhttp-response-jq ^https://d/ '.a=1'\n" +
		"[Map Local]\n^https://e/ data=\"{}\"\n" +
		"[Script]\nj = type=http-response, pattern=^https://f/, script-path=https://s/j.js, requires-body=true\ncron-ish = type=cron, cronexp=0 0 * * *\n" +
		"[MITM]\nhostname = %APPEND% a.com\n[General]\nfoo = bar\n"
	loonPlugin := "#!name=x\n[Rule]\nDOMAIN,a.com,REJECT\nIP-CIDR,1.1.1.0/24,REJECT,no-resolve\n" +
		"[Rewrite]\n^https://a/ reject-200\n^https://b/ 302 https://c/\n^https://d/ script-response-body https://s/d.js\n^https://e/ response-body-json-del data.ads\nweird\n" +
		"[Script]\nhttp-response ^https://f/ script-path=https://s/f.js, tag=f\ncron \"0 8 * * *\" script-path=https://s/c.js\nnope\n[MITM]\nhostname = a.com\n"
	cases := []struct {
		name string
		d    Dialect
		text string
		want Counts
	}{
		{"surge 模块", Surge, surgeModule, Counts{Rules: 7, Scripts: 1, Skipped: 4}},
		{"shadowrocket 同 surge", Shadowrocket, surgeModule, Counts{Rules: 7, Scripts: 1, Skipped: 4}},
		{"loon 插件", Loon, loonPlugin, Counts{Rules: 5, Scripts: 3, Skipped: 2}},
		{"纯规则集没有段头", Surge, "# c\nDOMAIN,a.com\nAND,((DOMAIN,a),(DST-PORT,443)),REJECT\nfoo\n", Counts{Rules: 2, Skipped: 1}},
		{"surge 里的 loon 段不认", Surge, "[Rewrite]\n^a reject\n", Counts{}},
		{"空文本", Surge, "", Counts{}},
	}
	for _, c := range cases {
		got, err := Count(c.d, c.text)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v err=%v, want %+v", c.name, got, err, c.want)
		}
	}
	for _, d := range []Dialect{QuantumultX, Stash} {
		if _, err := Count(d, "x"); err != ErrUnsupported || Supported(d) {
			t.Errorf("%s 应暂不支持", d)
		}
	}
}

func TestSubstitute(t *testing.T) {
	vals := map[string]any{"host": "a.com", "on": true, "n": float64(3), "frac": 1.5}
	cases := []struct{ in, want string }{
		{"x={{{host}}}", "x=a.com"},
		{"{{{ host }}} {{{on}}} {{{n}}} {{{frac}}}", "a.com true 3 1.5"},
		{"{{{missing}}} 保持原样", "{{{missing}}} 保持原样"},
		{"{{host}} 两层括号不算", "{{host}} 两层括号不算"},
	}
	for _, c := range cases {
		if got := Substitute(c.in, vals); got != c.want {
			t.Errorf("Substitute(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Substitute("{{{a}}}", map[string]any{"a": map[string]any{}}); got != "{{{a}}}" {
		t.Errorf("非标量值应保持原样,得到 %q", got)
	}
	if got := Substitute("{{{a}}}", nil); got != "{{{a}}}" {
		t.Errorf("没有参数应原样,得到 %q", got)
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"\n \n", ""},
		{"\ufeffa  \r\nb\t\r\n\r\n\r\n", "a\nb\n"},
		{"a\rb", "a\nb\n"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuiltinDefaultsParse(t *testing.T) {
	ds, err := BuiltinDefaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds { // 以后往 defaults.json 里加数据,这里负责把关:每份都要合法、能数出内容
		dl, ok := ParseDialect(string(d.Dialect))
		if !ok || !idRe.MatchString(d.ID) {
			t.Fatalf("默认规则 %q 的 id 或格式不合法", d.ID)
		}
		if _, _, err := prepare(dl, d.Text); err != nil {
			t.Fatalf("默认规则 %q: %v", d.ID, err)
		}
	}
}
