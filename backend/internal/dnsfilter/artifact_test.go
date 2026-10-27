package dnsfilter

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, body string) *Parsed {
	t.Helper()
	p, err := ParseList([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const listBody = `! 测试名单
||ads.example.com^
||ads.example.com^
0.0.0.0 tracker.test
/^ad[0-9]+\.x\.com$/
||imp.test^$important
@@||ok.ads.example.com^
@@||fine.test^$important
`

func settingsWith(lists ...List) Settings {
	s := Default()
	s.Enabled = true
	s.Lists = lists
	return s
}

func TestCompileGroupsAndTags(t *testing.T) {
	s := settingsWith(List{ID: "a", Name: "A", URL: "https://x/a", Enabled: true})
	s.AllowDomains = []string{"*.Safe.com", "exact.org", "exact.org"}
	art := Compile(s, map[string]*Parsed{"a": mustParse(t, listBody)})

	if len(art.Blocks) != 2 || !art.Blocks[0].Important || art.Blocks[1].Important {
		t.Fatalf("应先 important 后普通共两条拦截: %+v", art.Blocks)
	}
	if len(art.Allow) != 1 || len(art.AllowImportant) != 1 || len(art.UserAllow) != 1 {
		t.Fatalf("放行组数不对: %+v", art)
	}
	byTag := map[string]Group{}
	for _, g := range art.Groups {
		if !strings.HasPrefix(g.Tag, TagPrefix+"a-") && !strings.HasPrefix(g.Tag, TagPrefix+"user-") {
			t.Errorf("tag 前缀不对: %s", g.Tag)
		}
		byTag[g.Tag] = g
	}
	normal := byTag[art.Blocks[1].Tag].Rule
	if got := normal["domain"]; !reflect.DeepEqual(got, []string{"tracker.test"}) {
		t.Errorf("普通拦截的精确域名: %v", got)
	}
	if got := normal["domain_suffix"]; !reflect.DeepEqual(got, []string{"ads.example.com"}) {
		t.Errorf("重复条目应去重: %v", got)
	}
	if _, ok := normal["domain_regex"]; !ok {
		t.Error("正则条目丢了")
	}
	user := byTag[art.UserAllow[0]].Rule
	if !reflect.DeepEqual(user["domain"], []string{"exact.org"}) || !reflect.DeepEqual(user["domain_suffix"], []string{"safe.com"}) {
		t.Errorf("用户放行: %v", user)
	}

	// 同样的输入 → 同样的 tag;内容变了 → tag 变了
	again := Compile(s, map[string]*Parsed{"a": mustParse(t, listBody)})
	if !reflect.DeepEqual(art.Blocks, again.Blocks) || !reflect.DeepEqual(art.Allow, again.Allow) {
		t.Error("相同输入的 tag 应当稳定")
	}
	changed := Compile(s, map[string]*Parsed{"a": mustParse(t, listBody+"||more.test^\n")})
	if changed.Blocks[1].Tag == art.Blocks[1].Tag {
		t.Error("内容变了 tag 应当变")
	}
	if changed.Blocks[0].Tag != art.Blocks[0].Tag {
		t.Error("没变的组 tag 不该变(文件不用重编)")
	}
}

func TestCompileSkipsDisabledAndMissing(t *testing.T) {
	s := settingsWith(
		List{ID: "off", URL: "https://x/off", Enabled: false},
		List{ID: "none", URL: "https://x/none", Enabled: true},
		List{ID: "ok", URL: "https://x/ok", Enabled: true},
	)
	art := Compile(s, map[string]*Parsed{
		"off": mustParse(t, "||a.test^\n"),
		"ok":  mustParse(t, "||b.test^\n"),
	})
	if len(art.Blocks) != 1 || !strings.HasPrefix(art.Blocks[0].Tag, TagPrefix+"ok-") {
		t.Fatalf("只该有 ok 一份: %+v", art.Blocks)
	}
	if len(art.UserAllow) != 0 {
		t.Error("没有放行域名不该有用户放行集")
	}
}

func TestSettingsKey(t *testing.T) {
	base := settingsWith(List{ID: "a", Name: "A", URL: "https://x/a", Enabled: true})
	base.AllowDomains = []string{"b.com", "a.com"}
	k := SettingsKey(base)

	same := base
	same.Lists = []List{{ID: "a", Name: "改名了", URL: "https://x/a", Enabled: true}, {ID: "z", URL: "https://x/z", Enabled: false}}
	same.AllowDomains = []string{"a.com", "b.com"}
	same.AutoUpdate = AutoUpdate{Enabled: true, Days: 7, Hour: 3}
	same.Enabled = false
	if SettingsKey(same) != k {
		t.Error("名字、关着的名单、放行域名顺序、自动更新计划、总开关都不该影响 key")
	}
	url := base
	url.Lists = []List{{ID: "a", URL: "https://x/other", Enabled: true}}
	allow := base
	allow.AllowDomains = []string{"a.com"}
	if SettingsKey(url) == k || SettingsKey(allow) == k {
		t.Error("地址或放行域名变了 key 应当变")
	}
	if len(k) != 16 {
		t.Errorf("key 应为 16 位十六进制: %q", k)
	}
}

func TestForCoreShape(t *testing.T) {
	empty := Compile(settingsWith(), nil).ForCore("/r")
	b, _ := json.Marshal(empty)
	for _, f := range []string{`"sets":[]`, `"blocks":[]`, `"allow":[]`, `"allowImportant":[]`, `"userAllow":[]`} {
		if !strings.Contains(string(b), f) {
			t.Errorf("%s 应是空数组而不是 null: %s", f, b)
		}
	}
	s := settingsWith(List{ID: "a", URL: "https://x/a", Enabled: true})
	art := Compile(s, map[string]*Parsed{"a": mustParse(t, "||a.test^\n")})
	sets := art.ForCore("/r")["sets"].([]any)
	m := sets[0].(map[string]any)
	if m["type"] != "local" || m["format"] != "binary" || m["path"] != filepath.Join("/r", art.Groups[0].Tag+".srs") {
		t.Errorf("规则集条目: %v", m)
	}
}
