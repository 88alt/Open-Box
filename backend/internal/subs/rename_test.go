package subs

import (
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

func mk(names ...string) []sub.Node {
	out := make([]sub.Node, len(names))
	for i, n := range names {
		out[i] = sub.Node{Tag: n, OriginalTag: n, Type: "trojan", Server: "s.example.com", ServerPort: 443}
	}
	return out
}

func tags(a Applied) []string {
	out := []string{}
	for _, n := range a.Nodes {
		out = append(out, n.Tag)
	}
	return out
}

func run(t *testing.T, opts map[string]any, subName string, names ...string) Applied {
	t.Helper()
	return apply(mk(names...), parseRenameOpts(opts), subName, map[string]bool{})
}

func TestRenameDefaults(t *testing.T) {
	a := run(t, nil, "",
		"HK-01 IEPL", "🇭🇰 香港 02", "Hong Kong 3", "US-1", "美国 CN2 GIA", "Russia 1", "回国 1", "SG IPLC", "中国香港 01")
	want := []string{"香港-IEPL-01", "香港-01", "香港-02", "美国-01", "美国-02", "其他-01", "中国-01", "新加坡-IPLC-01", "香港-03"}
	if !reflect.DeepEqual(tags(a), want) {
		t.Fatalf("got  %q\nwant %q", tags(a), want)
	}
	codes := []string{}
	for _, p := range a.Preview {
		codes = append(codes, p.RegionCode)
	}
	if !reflect.DeepEqual(codes, []string{"HK", "HK", "HK", "US", "US", "", "CN", "SG", "HK"}) {
		t.Fatalf("%q", codes)
	}
	if a.Nodes[0].RegionCode != "HK" || a.Nodes[0].OriginalTag != "HK-01 IEPL" {
		t.Fatalf("%+v", a.Nodes[0])
	}
}

func TestRenameDisabledKeepsNamesButRegion(t *testing.T) {
	a := run(t, map[string]any{"enabled": false, "usePrefix": true}, "破晓", "HK-1", "foo")
	if !reflect.DeepEqual(tags(a), []string{"破晓 | HK-1", "破晓 | foo"}) || a.Preview[0].RegionCode != "HK" || a.Preview[1].RegionCode != "" {
		t.Fatalf("%q %+v", tags(a), a.Preview)
	}
}

func TestRenameTemplatePadAndOverrides(t *testing.T) {
	a := run(t, map[string]any{"template": "{seq}-{region}", "seqPad": float64(3), "overrides": map[string]any{"HK-1": "我的香港"}},
		"", "HK-1", "HK-2", "HK-3")
	if !reflect.DeepEqual(tags(a), []string{"我的香港", "001-香港", "002-香港"}) { // 改过名的不占序号
		t.Fatalf("%q", tags(a))
	}
	// 没特征时 {feature} 连同相邻的「-」一起去掉;自定义未识别标签
	a = run(t, map[string]any{"template": "{feature}-{region}", "unknownLabel": "杂"}, "", "HK-1", "foo", "HK-2 iepl")
	if !reflect.DeepEqual(tags(a), []string{"香港", "杂", "IEPL-香港"}) {
		t.Fatalf("%q", tags(a))
	}
}

func TestRenameFeatureOptions(t *testing.T) {
	// 旧档案的 featureDict 扁平化
	legacy := map[string]any{"featureDict": []any{map[string]any{"label": "游戏", "keywords": []any{"game", "游戏"}}}}
	if a := run(t, legacy, "", "HK game 1"); tags(a)[0] != "香港-GAME-01" {
		t.Fatalf("%q", tags(a))
	}
	// 显式空数组 = 不加特征(默认词 iepl 不再生效)
	if a := run(t, map[string]any{"featureKeywords": []any{}}, "", "HK IEPL 1"); tags(a)[0] != "香港-01" {
		t.Fatalf("%q", tags(a))
	}
	// 只取列表里第一个命中的词
	if a := run(t, map[string]any{"featureKeywords": []any{"ipv6", "iepl"}}, "", "HK IEPL ipv6"); tags(a)[0] != "香港-IPV6-01" {
		t.Fatalf("%q", tags(a))
	}
}

func TestRenameRegionDictOptions(t *testing.T) {
	// 显式空 regionDict:全部归到「其他」,但不报错
	a := run(t, map[string]any{"regionDict": []any{}}, "", "HK-1", "US-1")
	if !reflect.DeepEqual(tags(a), []string{"其他-01", "其他-02"}) || a.Preview[0].RegionCode != "" {
		t.Fatalf("%q", tags(a))
	}
	// 顺序即优先级;没填国家代码的地区也能命名,只是没有 regionCode
	d := []any{
		map[string]any{"code": "", "name": "自建", "keywords": []any{"home"}},
		map[string]any{"code": "jp", "name": "日本", "keywords": []any{"tokyo"}},
	}
	a = run(t, map[string]any{"regionDict": d}, "", "home tokyo 1", "tokyo 2")
	if !reflect.DeepEqual(tags(a), []string{"自建-01", "日本-01"}) || a.Preview[0].RegionCode != "" || a.Preview[1].RegionCode != "JP" {
		t.Fatalf("%q %+v", tags(a), a.Preview)
	}
}

func TestRenameExclude(t *testing.T) {
	a := run(t, nil, "", "官网 example.com", "HK-1", "联系客服")
	if len(a.Nodes) != 1 || len(a.Excluded) != 2 {
		t.Fatalf("%+v", a)
	}
	a = run(t, map[string]any{"excludeKeywords": []any{}}, "", "官网 example.com", "HK-1")
	if len(a.Nodes) != 2 || len(a.Excluded) != 0 {
		t.Fatalf("显式空数组应当不过滤:%+v", a)
	}
}

func TestRenameUniqueAcrossSubscriptions(t *testing.T) {
	a := apply(mk("HK-1", "HK-2"), parseRenameOpts(nil), "", map[string]bool{"香港-01": true})
	if !reflect.DeepEqual(tags(a), []string{"香港-01 2", "香港-02"}) {
		t.Fatalf("%q", tags(a))
	}
}

func TestHasKeywordBoundaries(t *testing.T) {
	cases := []struct {
		name, kw string
		want     bool
	}{
		{"russia 1", "us", false},
		{"us-01", "us", true},
		{"hk01", "hk", true},
		{"hk 01", "hk", true},
		{"sweden", "de", false},
		{"de-1", "de", true},
		{"香港hk", "hk", true},
		{"x2x", "2x", false},
		{"hk 2x", "2x", true},
		{"hong kong", "hong kong", true},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := hasKeyword(c.name, c.kw); got != c.want {
			t.Errorf("hasKeyword(%q, %q) = %v", c.name, c.kw, got)
		}
	}
}
