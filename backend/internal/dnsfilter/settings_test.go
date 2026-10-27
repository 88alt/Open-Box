package dnsfilter

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSettingsNormalizes(t *testing.T) {
	raw := `{"enabled":true,
	 "lists":[{"id":"list-a1","name":"  广告名单 ","url":" https://example.com/a.txt ","enabled":true},
	          {"id":"list-b2","name":"B","url":"http://example.org/b","enabled":false}],
	 "allowDomains":["Example.COM"," *.Foo.bar. ","example.com","a_b.c-d.net"],
	 "autoUpdate":{"enabled":true,"days":7,"hour":3}}`
	s, err := ParseSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled || len(s.Lists) != 2 {
		t.Fatalf("%+v", s)
	}
	if s.Lists[0].Name != "广告名单" || s.Lists[0].URL != "https://example.com/a.txt" {
		t.Fatalf("没去空白:%+v", s.Lists[0])
	}
	want := []string{"example.com", "*.foo.bar", "a_b.c-d.net"}
	if !reflect.DeepEqual(s.AllowDomains, want) {
		t.Fatalf("白名单 %v,想要 %v", s.AllowDomains, want)
	}
	if s.AutoUpdate != (AutoUpdate{Enabled: true, Days: 7, Hour: 3}) {
		t.Fatalf("%+v", s.AutoUpdate)
	}
}

func TestParseSettingsDefaults(t *testing.T) {
	s, err := ParseSettings([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled || s.Lists == nil || s.AllowDomains == nil || len(s.Lists) != 0 {
		t.Fatalf("%+v", s)
	}
	if s.AutoUpdate != (AutoUpdate{Days: 1, Hour: 4}) {
		t.Fatalf("autoUpdate 默认值不对:%+v", s.AutoUpdate)
	}
}

func TestParseSettingsRejects(t *testing.T) {
	list := func(id, name, url string) string {
		return `{"lists":[{"id":"` + id + `","name":"` + name + `","url":"` + url + `","enabled":true}]}`
	}
	tooMany := `{"lists":[` + strings.Repeat(`{"id":"x","name":"n","url":"https://a.com/"},`, MaxLists) + `{"id":"y","name":"n","url":"https://b.com/"}]}`
	cases := map[string]string{
		"不是 JSON":    `[`,
		"类型不对":       `{"enabled":"yes"}`,
		"id 空":       list("", "n", "https://a.com/x"),
		"id 有空格":     list("a b", "n", "https://a.com/x"),
		"名称空":        list("a", "  ", "https://a.com/x"),
		"链接空":        list("a", "n", ""),
		"链接 ftp":     list("a", "n", "ftp://a.com/x"),
		"链接带账号":      list("a", "n", "https://u:p@a.com/x"),
		"链接无主机":      list("a", "n", "https:///x"),
		"id 重复":      `{"lists":[{"id":"a","name":"n","url":"https://a.com/1"},{"id":"a","name":"m","url":"https://a.com/2"}]}`,
		"链接重复":       `{"lists":[{"id":"a","name":"n","url":"https://a.com/1"},{"id":"b","name":"m","url":"https://a.com/1"}]}`,
		"名单太多":       tooMany,
		"白名单 IP":     `{"allowDomains":["1.2.3.4"]}`,
		"白名单带路径":     `{"allowDomains":["a.com/x"]}`,
		"白名单带端口":     `{"allowDomains":["a.com:80"]}`,
		"白名单空串":      `{"allowDomains":["  "]}`,
		"白名单连字符开头":   `{"allowDomains":["-a.com"]}`,
		"白名单空标签":     `{"allowDomains":["a..com"]}`,
		"白名单只有通配符":   `{"allowDomains":["*."]}`,
		"白名单非 ASCII": `{"allowDomains":["例子.com"]}`,
		"间隔 0":       `{"autoUpdate":{"days":0,"hour":4}}`,
		"间隔 31":      `{"autoUpdate":{"days":31,"hour":4}}`,
		"小时 24":      `{"autoUpdate":{"days":1,"hour":24}}`,
		"小时负数":       `{"autoUpdate":{"days":1,"hour":-1}}`,
	}
	for name, raw := range cases {
		if _, err := ParseSettings([]byte(raw)); err == nil {
			t.Errorf("%s:应该被拒绝", name)
		}
	}
}

func TestSettingsRoundTripThroughProfile(t *testing.T) {
	in, err := ParseSettings([]byte(`{"enabled":true,"lists":[{"id":"a","name":"n","url":"https://a.com/x","enabled":true}],
		"allowDomains":["x.com"],"autoUpdate":{"enabled":true,"days":3,"hour":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := FromProfile(map[string]any{"dns": map[string]any{"filter": in.ToMap()}})
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("往返不一致:\n%+v\n%+v", got, in)
	}
}

func TestFromProfileTolerant(t *testing.T) {
	for _, p := range []map[string]any{nil, {}, {"dns": "x"}, {"dns": map[string]any{"filter": 3.0}}, {"dns": map[string]any{"filter": map[string]any{"lists": "bad"}}}} {
		got := FromProfile(p)
		if got.Enabled || got.Lists == nil || got.AllowDomains == nil || got.AutoUpdate.Days != 1 {
			t.Fatalf("%v → %+v", p, got)
		}
	}
	// 档案里只有 enabled 时,列表和白名单补成空数组(前端直接对它们取 length / map)
	got := FromProfile(map[string]any{"dns": map[string]any{"filter": map[string]any{"enabled": true}}})
	if !got.Enabled || got.Lists == nil || got.AllowDomains == nil {
		t.Fatalf("%+v", got)
	}
}

func TestBuildStatus(t *testing.T) {
	on := BuildStatus(Settings{Enabled: true, Lists: []List{}, AllowDomains: []string{}})
	if !on.Pending || on.Applied != nil || on.Connected || on.Busy || on.Lists == nil {
		t.Fatalf("%+v", on)
	}
	if BuildStatus(Default()).Pending {
		t.Fatal("关着、从没应用过,不该是待应用")
	}
}
