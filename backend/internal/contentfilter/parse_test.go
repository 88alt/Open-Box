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
	qx := "[general]\nforce-http-engine-hosts = a.com\n[filter_local]\nhost, a.com, reject\nhost-suffix, b.com, proxy\nip-cidr, 1.1.1.0/24, direct\nnonsense\n" +
		"[rewrite_local]\n^https://a/ url reject-200\n^https://b/ url 302 https://c/\n^https://d/ url script-response-body https://s/d.js\n" +
		"^https://e/ url request-header (\\r\\n)A:.+ request-header (\\r\\n)A: b\nweird line here\n" +
		"[rewrite_remote]\nhttps://x/r.conf, tag=r, update-interval=86400, enabled=true\n" +
		"[task_local]\n0 8 * * * https://s/t.js, tag=t, enabled=true\nevent-interaction https://s/e.js, tag=e\nbad task\n[mitm]\nhostname = a.com\n"
	stash := "name: 测试\ndesc: x\nrules:\n  - DOMAIN-SUFFIX,ads.com,REJECT\n  - \"DOMAIN,b.com,REJECT\"\n  - bogus\n" +
		"http:\n  mitm:\n    - \"a.com\"\n  url-rewrite:\n    - ^https?://a/ - reject\n    - ^http://b/ https://b/ 302\n    - nonsense\n" +
		"  header-rewrite:\n    - http-request ^https://c/ header-del Cookie\n  body-rewrite:\n    - http-response-jq ^https://d/ '.a=1'\n" +
		"  map-local:\n    - match: ^https://e/\n      data: \"{}\"\n" +
		"  script:\n    - match: ^https://f/\n      name: f\n      type: response\n    - match: ^https://g/\n      name: g\n" +
		"script-providers:\n  f:\n    url: https://s/f.js\ncron:\n  script:\n    - name: f\n      cron: \"0 8 * * *\"\n"
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
		{"quantumultx", QuantumultX, qx, Counts{Rules: 6, Scripts: 3, Skipped: 3}},
		{"quantumultx 纯规则集", QuantumultX, "host-suffix, a.com, proxy\nDOMAIN,a.com\n", Counts{Rules: 1, Skipped: 1}},
		{"stash", Stash, stash, Counts{Rules: 7, Scripts: 3, Skipped: 2}},
		{"stash 没有 YAML 结构", Stash, "hello\nworld\n", Counts{}},
		{"surge 里的 loon 段不认", Surge, "[Rewrite]\n^a reject\n", Counts{}},
		{"空文本", Surge, "", Counts{}},
	}
	for _, c := range cases {
		got, err := Count(c.d, c.text)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v err=%v, want %+v", c.name, got, err, c.want)
		}
	}
	if _, err := Count(Dialect("clash"), "x"); err != ErrUnsupported || Supported("clash") {
		t.Error("不认识的格式应返回 ErrUnsupported")
	}
	for _, d := range []Dialect{Surge, QuantumultX, Loon, Shadowrocket, Stash} {
		if !Supported(d) {
			t.Errorf("%s 应已支持", d)
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
	if len(ds) != 3 {
		t.Fatalf("上游对齐应有三份默认规则,got %d", len(ds))
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
