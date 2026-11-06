package contentfilter

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// shippedSet 是随包 defaults.json 里的一份(上游算好的结构化结果,当标准答案用)。
type shippedSet struct {
	ID, Name, Dialect, SourceURL, Text string
	Rules                              []map[string]any
	Hostnames                          []string
	Arguments                          struct {
		Values   map[string]string
		Declared []Argument
	}
	Scripts map[string]string
}

func shippedSets(t *testing.T) []shippedSet {
	t.Helper()
	var f struct{ Sets []shippedSet }
	if err := json.Unmarshal(defaultsJSON, &f); err != nil {
		t.Fatal(err)
	}
	return f.Sets
}

func asMap(t *testing.T, r Rule) map[string]any {
	t.Helper()
	b, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var hashID = regexp.MustCompile(`^r-[0-9a-f]{8}$`)

func ruleKey(m map[string]any) string {
	p, _ := json.Marshal(m["params"])
	return m["dialect"].(string) + "|" + m["action"].(string) + "|" + m["pattern"].(string) + "|" + string(p)
}

// 前四步的解析器对着上游随包的结构化结果验一遍。
func TestAnalyzeAgainstShippedDefaults(t *testing.T) {
	for _, s := range shippedSets(t) {
		a, err := Analyze(Dialect(s.Dialect), s.Text, s.Arguments.Values, s.SourceURL)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		if !reflect.DeepEqual(a.Hostnames, s.Hostnames) {
			t.Errorf("%s: 主机名不一致(我们 %d 个,上游 %d 个)", s.Name, len(a.Hostnames), len(s.Hostnames))
		}
		if len(s.Arguments.Declared) > 0 || len(a.Declared) > 0 {
			if !reflect.DeepEqual(a.Declared, s.Arguments.Declared) {
				t.Errorf("%s: 声明的参数不一致\n%v\n%v", s.Name, a.Declared, s.Arguments.Declared)
			}
			if !reflect.DeepEqual(a.Values, s.Arguments.Values) {
				t.Errorf("%s: 参数值不一致\n%v\n%v", s.Name, a.Values, s.Arguments.Values)
			}
		}
		theirs := map[string]map[string]any{}
		ids := map[string]map[string]bool{} // 同一条写了两遍时有两个 id
		for _, w := range s.Rules {
			k := ruleKey(w)
			theirs[k] = w
			if ids[k] == nil {
				ids[k] = map[string]bool{}
			}
			ids[k][w["id"].(string)] = true
		}
		ours := map[string]bool{}
		for _, r := range a.Rules {
			m := asMap(t, r)
			if p, ok := m["params"].(map[string]any); ok && m["action"] == "script" {
				p["sha256"] = "" // 我们这一步还没抓脚本,sha256 先空
			}
			key := ruleKey(m)
			w, ok := theirs[strings.Replace(key, `"sha256":""`, `"sha256":"`+scriptSHA(theirs, key)+`"`, 1)]
			if !ok {
				w, ok = theirs[key]
			}
			if !ok {
				t.Errorf("%s: 我们解出、上游没有的规则 %v", s.Name, key)
				continue
			}
			wk := ruleKey(w)
			ours[wk] = true
			// 上游的 id 是「哈希形」时我们必须解出同一个 id(同一行文本、同一行号)
			hashed := false
			for id := range ids[wk] {
				hashed = hashed || hashID.MatchString(id)
			}
			if hashed && !ids[wk][m["id"].(string)] {
				t.Errorf("%s: id 不一致 %v 不在 %v: %s", s.Name, m["id"], ids[wk], w["pattern"])
			}
		}
		// 上游有、我们没有的,只允许两类:正则 RE2 不认(随包数据里留着上游自己手加的 lookaround 写法),
		// 或 id 是手起的(不是哈希形,上游对规则做过人工改写)。
		for k, w := range theirs {
			if ours[k] {
				continue
			}
			if _, err := regexp.Compile(w["pattern"].(string)); err != nil {
				continue
			}
			if id, _ := w["id"].(string); !hashID.MatchString(id) {
				continue
			}
			t.Errorf("%s: 上游有、我们没有的规则 %v", s.Name, k)
		}
		t.Logf("%s: 我们 %d 条 / 上游 %d 条;跳过 脚本%d 不支持%d 路由%d 表达不了%d(屏蔽 QUIC %d)", s.Name, len(a.Rules), len(s.Rules),
			a.SkippedScripts, a.SkippedUnsupported, a.SkippedRouting, a.SkippedUnrepresentable, a.CoveredQuic)
	}
}

// scriptSHA 在上游的 script 规则里按「去掉 sha256 之后相同」找出它的 sha256。
func scriptSHA(theirs map[string]map[string]any, key string) string {
	for _, w := range theirs {
		if w["action"] != "script" {
			continue
		}
		p := w["params"].(map[string]any)
		sha := p["sha256"].(string)
		if strings.Replace(ruleKey(w), `"sha256":"`+sha+`"`, `"sha256":""`, 1) == key {
			return sha
		}
	}
	return ""
}

func TestDeclaredArguments(t *testing.T) {
	got := DeclaredArguments(Surge, "#!name=x\r\n#!arguments = a:1, b:, c ,d:x:y\r\n[Rule]\r\n", "")
	want := []Argument{{"a", "1"}, {"b", ""}, {"c", ""}, {"d", "x:y"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	loon := "[Argument]\nk1 = input,\"v1\",tag=a\nk2 = switch,true\nk3 = \"plain\"\nnoequals\n[Rewrite]\n"
	if got := DeclaredArguments(Loon, loon, ""); !reflect.DeepEqual(got, []Argument{{"k1", "v1"}, {"k2", "true"}, {"k3", "plain"}}) {
		t.Fatalf("%v", got)
	}
	got = DeclaredArguments(QuantumultX, "x", "https://a.com/f.conf#a=1&b=%E4%B8%AD+x&c=")
	if !reflect.DeepEqual(got, []Argument{{"a", "1"}, {"b", "中 x"}, {"c", ""}}) {
		t.Fatalf("%v", got)
	}
	if DeclaredArguments(QuantumultX, "x", "https://a.com/f.conf") != nil || DeclaredArguments(Stash, "x", "u#a=1") != nil {
		t.Fatal("不该有声明")
	}
}

func TestAnalyzeSubstitution(t *testing.T) {
	text := "#!arguments=host:ad.com,mode:a\n[URL Rewrite]\n^https://{{{host}}}/ - reject\n[Script]\ns = type=http-response, pattern=^https://x/, script-path=https://s/a.js, argument={{{mode}}}\n[MITM]\nhostname = %APPEND% A.com, b.com,, a.com\n"
	a, err := Analyze(Surge, text, map[string]string{"mode": "b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Rules[0].Pattern != "^https://ad.com/" || a.Rules[1].Params["argument"] != "b" {
		t.Fatalf("%+v", a.Rules)
	}
	if !reflect.DeepEqual(a.Hostnames, []string{"a.com", "b.com"}) || a.Values["host"] != "ad.com" || a.Values["mode"] != "b" {
		t.Fatalf("%v %v", a.Hostnames, a.Values)
	}
	// 规则行里的占位符没有值 → 报错;[Script] 的非 pattern 选项里没有值只是提示
	_, err = Analyze(Surge, "[URL Rewrite]\n^https://{{{nope}}}/ - reject\n[MITM]\nhostname = a.com\n", nil, "")
	if pe := ParseErrorOf(err); pe == nil || pe.Reason != "surge_unresolved_argument:{{{nope}}}" || pe.Line != 2 {
		t.Fatalf("%v", err)
	}
	a, err = Analyze(Surge, "[Script]\ns = type=http-request, pattern=^https://x/, script-path=https://s/a.js, argument={{{nope}}}\n", nil, "")
	if err != nil || a.UnresolvedScriptArgs != 1 {
		t.Fatalf("%v %+v", err, a)
	}
	// Loon 的占位符是 [{名}]
	a, err = Analyze(Loon, "[Argument]\nh = input,\"x.com\"\n[Rewrite]\n^https://[{h}]/ reject\n[MITM]\nhostname = x.com\n", nil, "")
	if err != nil || a.Rules[0].Pattern != "^https://x.com/" {
		t.Fatalf("%v %+v", err, a)
	}
}

func TestAnalyzeSkipCounts(t *testing.T) {
	text := "[URL Rewrite]\n^https://a/(?=x) - reject\n[Rule]\nAND,((PROTOCOL,UDP),(DEST-PORT,443)),REJECT\nDOMAIN,a.com,REJECT\n[Script]\ns = type=cron, pattern=^a, script-path=https://s/a.js\n[MITM]\nhostname = a.com\n"
	a, err := Analyze(Surge, text, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Rules) != 0 || a.SkippedUnsupported != 1 || a.CoveredQuic != 1 || a.SkippedRouting != 1 || a.SkippedUnrepresentable != 1 || a.SkippedScripts != 0 || a.Skipped() != 3 {
		t.Fatalf("%+v", a)
	}
}

func TestHostnamesPerDialect(t *testing.T) {
	if got := Hostnames(Loon, "[Rewrite]\nhostname = no.com\n[MITM]\nhostname = a.com, *.B.com\n"); !reflect.DeepEqual(got, []string{"a.com", "*.b.com"}) {
		t.Fatalf("%v", got)
	}
	// Quantumult X:[mitm] 段和没有段头的行都认,别的段不认
	qx := "hostname = top.com\n[rewrite_local]\n^https://a url reject\n[mitm]\nhostname = x.com, top.com\n[task_local]\nhostname = no.com\n"
	if got := Hostnames(QuantumultX, qx); !reflect.DeepEqual(got, []string{"top.com", "x.com"}) {
		t.Fatalf("%v", got)
	}
	st := "http:\n  mitm:\n    - \"a.com\"\n    - 'B.com'\n    - a.com\n  url-rewrite:\n    - ^https://a/ - reject\n"
	if got := Hostnames(Stash, st); !reflect.DeepEqual(got, []string{"a.com", "b.com"}) {
		t.Fatalf("%v", got)
	}
	if got := Hostnames(Surge, "[Rule]\nDOMAIN,a.com,REJECT\n"); got == nil || len(got) != 0 {
		t.Fatalf("没有主机名应当是空数组: %#v", got)
	}
}
