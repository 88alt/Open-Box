package routing

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

// 自写测试向量:model.go 里的 ListTagForUrl / ParsePortSpec / normalizePolicy / normalizeCustomRule /
// normalizeCustomPolicy / normalizeRouting。输入自己拟,期望值按下面的规则人工推算,JSON 逐字节比(键序固定):
//   - ListTagForUrl:先 JS trim,空串得空串;否则对 UTF-16 码元做 FNV-1a 32 位(初值 0x811c9dc5、乘数 0x01000193),
//     结果写成 list- 加 8 位小写十六进制;哈希值全部用独立的 Python 小脚本另算;
//   - ParsePortSpec:用逗号或 JS 空白隔开,空段忽略;每段是 1~5 位 ASCII 数字或「数字-数字」,取值 1~65535 且起点不大于终点;
//     单个端口(含「80-80」)进 ports,真区间写成「起:止」进 ranges,两者各自保持出现顺序;一段写错整体失败并且什么都不返回;
//   - normalizePolicy:id / name / icon / default 取 trim 后的非空字符串,没有时 id 为 policy-<下标>、name 为 策略-<下标+1>、
//     icon / default 为空串;enabled 只有布尔 false 才为假;ruleUrls 只留 http(s):// 打头(不分大小写)的字符串,不去重;
//     rulesets 先放自己写的、再放 ruleUrls 折出来的 list- 标签,整体去重保序;随包规则库名单(geoTags)存在时,
//     geosite- / geoip- 打头(区分大小写、必须带横线)且不在名单里的规则集挪进 missingRulesets,名单不存在时什么都不挪;
//     domain / domainSuffix / domainKeyword / ipCidr 只收字符串并 trim,不去重、不转小写、不校验;
//   - normalizeCustomRule:type 必须是九种之一(原样比较、不 trim、区分大小写),value 与 outbound 取 trim 后的非空字符串;
//     ruleUrl 的 value 要 http(s):// 打头,port 的 value 要能被 ParsePortSpec 解析;note 取 trim 后的非空字符串并截到 60 个码元;
//   - normalizeCustomPolicy:name / icon 缺省为「前置自定义分流」/ misc:pin,enabled 只有 false 才为假,rules 非数组当空,
//     写错的行丢掉,geosite / geoip / ruleset 行引用的规则集不在名单里就整行丢掉并按行记进 missingRulesets(不去重);
//   - normalizeRouting:先迁移,再归一化;policies 里没有任何条件、名字叫「其他」或叫兜底名的丢掉(下标仍按原数组算),
//     activePolicies 是其中 enabled 的;兜底名 / 图标 / 默认值取 trim 后的非空字符串;
//     missingRulesets 先放前置自定义分流(只在它启用时)、再按归一化后全部站点集的顺序放启用的站点集(含被丢掉的)。

// pe 描述期望的站点集;空字段取缺省(列表为 [],notes 为 {})。
type pe struct {
	id, name, icon, def                         string
	disabled                                    bool
	ruleUrls, rulesets, domain, suffix, keyword string
	cidr, notes, missing                        string
}

func (p pe) String() string {
	d := func(s, def string) string {
		if s == "" {
			return def
		}
		return s
	}
	out := fmt.Sprintf(`{"id":%q,"name":%q,"icon":%q,"default":%q,"enabled":%t,"ruleUrls":%s,"rulesets":%s,"domain":%s,"domainSuffix":%s,"domainKeyword":%s,"ipCidr":%s,"notes":%s`,
		p.id, p.name, p.icon, p.def, !p.disabled, d(p.ruleUrls, "[]"), d(p.rulesets, "[]"), d(p.domain, "[]"),
		d(p.suffix, "[]"), d(p.keyword, "[]"), d(p.cidr, "[]"), d(p.notes, "{}"))
	if p.missing != "" {
		out += `,"missingRulesets":` + p.missing
	}
	return out + "}"
}

func jsonText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func withGeoList(geo []string) func() {
	if geo == nil {
		setAvailableGeoTags(nil)
	} else {
		setAvailableGeoTags(&geo)
	}
	return func() { setAvailableGeoTags(nil) }
}

func TestListTagForUrlVectors(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"\u3000\ufeff\t\n", ""},
		{"a", "list-e40c292c"},
		{"https://example.com/a.srs", "list-b99d2d1f"},
		{"  https://example.com/a.srs  ", "list-b99d2d1f"},
		{"\u3000https://example.com/a.srs\u00a0\ufeff", "list-b99d2d1f"},
		{"\u0085https://example.com/a.srs", ListTagForUrl("\u0085https://example.com/a.srs")},
		{"http://x", "list-7c1f3f49"},
		{"HTTPS://Example.com/a.srs", "list-cc49a29f"},
		{"https://例子.测试/规则", "list-ef34eef1"},
		{"https://e.com/😀", "list-c2f6a3a6"},
		// 哈希高位为 0 时仍补满 8 位(Python 另算)
		{"https://z.example/11.srs", "list-0aba4a77"},
	} {
		if got := ListTagForUrl(c.in); got != c.want {
			t.Errorf("ListTagForUrl(%q)=%q 要 %q", c.in, got, c.want)
		}
	}
	// U+0085 不是 JS 空白,不会被 trim 掉,所以和去掉它的结果不同
	if ListTagForUrl("\u0085https://example.com/a.srs") == "list-b99d2d1f" {
		t.Error("U+0085 不该被当成空白")
	}
	// 区分大小写
	if ListTagForUrl("https://example.com/a.srs") == ListTagForUrl("https://example.com/A.srs") {
		t.Error("大小写不同的链接标签不该相同")
	}
}

func TestParsePortSpecVectors(t *testing.T) {
	type res struct {
		ports  []int
		ranges []string
		ok     bool
	}
	for _, c := range []struct {
		in   string
		want res
	}{
		{"51820", res{[]int{51820}, nil, true}},
		{"1", res{[]int{1}, nil, true}},
		{"65535", res{[]int{65535}, nil, true}},
		{"1000-2000", res{nil, []string{"1000:2000"}, true}},
		{"1-65535", res{nil, []string{"1:65535"}, true}},
		{"80-80", res{[]int{80}, nil, true}},
		{"80,443", res{[]int{80, 443}, nil, true}},
		{"80 443", res{[]int{80, 443}, nil, true}},
		{"80, 443", res{[]int{80, 443}, nil, true}},
		{"80\t443\n8080", res{[]int{80, 443, 8080}, nil, true}},
		{"80\u3000443", res{[]int{80, 443}, nil, true}},
		{"80\u00a0443", res{[]int{80, 443}, nil, true}},
		{"80\u2028443", res{[]int{80, 443}, nil, true}},
		{",80,,443,", res{[]int{80, 443}, nil, true}},
		{"  80  ", res{[]int{80}, nil, true}},
		{"53,1000-2000,80,3000-3001", res{[]int{53, 80}, []string{"1000:2000", "3000:3001"}, true}},
		{"80,80", res{[]int{80, 80}, nil, true}},
		{"00080", res{[]int{80}, nil, true}},
		{"00001-00002", res{nil, []string{"1:2"}, true}},
		// 写错:整体失败,什么都不返回
		{"", res{nil, nil, false}},
		{"   ", res{nil, nil, false}},
		{",", res{nil, nil, false}},
		{"0", res{nil, nil, false}},
		{"65536", res{nil, nil, false}},
		{"99999", res{nil, nil, false}},
		{"000080", res{nil, nil, false}},
		{"0-10", res{nil, nil, false}},
		{"65535-65536", res{nil, nil, false}},
		{"2000-1000", res{nil, nil, false}},
		{"80-", res{nil, nil, false}},
		{"-80", res{nil, nil, false}},
		{"80-90-100", res{nil, nil, false}},
		{"80 - 90", res{nil, nil, false}},
		{"a", res{nil, nil, false}},
		{"80,abc", res{nil, nil, false}},
		{"80,70000,443", res{nil, nil, false}},
		{"80:90", res{nil, nil, false}},
		{"8.0", res{nil, nil, false}},
		{"+80", res{nil, nil, false}},
		{"８０", res{nil, nil, false}},
		{"80\u0085443", res{nil, nil, false}},
		{"80\u200b443", res{nil, nil, false}},
	} {
		p, r, ok := ParsePortSpec(c.in)
		got := res{p, r, ok}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParsePortSpec(%q)=%+v 要 %+v", c.in, got, c.want)
		}
	}
}

func TestNormalizePolicyVectors(t *testing.T) {
	u1, u2, u3 := "list-b99d2d1f", "list-7c1f3f49", "list-cc49a29f"
	long61 := strings.Repeat("字", 61)
	long60 := strings.Repeat("字", 60)
	cases := []struct {
		name string
		in   string
		idx  int
		geo  []string
		want pe
	}{
		{"空对象", `{}`, 0, nil, pe{id: "policy-0", name: "策略-1"}},
		{"下标 3", `{}`, 3, nil, pe{id: "policy-3", name: "策略-4"}},
		{"null", `null`, 2, nil, pe{id: "policy-2", name: "策略-3"}},
		{"数组", `[{"id":"x"}]`, 2, nil, pe{id: "policy-2", name: "策略-3"}},
		{"字符串", `"x"`, 2, nil, pe{id: "policy-2", name: "策略-3"}},
		{"数字", `5`, 2, nil, pe{id: "policy-2", name: "策略-3"}},
		{"布尔", `true`, 2, nil, pe{id: "policy-2", name: "策略-3"}},
		{"四个文本字段 trim", "{\"id\":\"  p1 \",\"name\":\"\\u3000名字\\ufeff\",\"icon\":\" i:x \",\"default\":\" proxy \"}", 0, nil,
			pe{id: "p1", name: "名字", icon: "i:x", def: "proxy"}},
		{"空白与空串用缺省", `{"id":"  ","name":"","icon":"  ","default":"\t"}`, 1, nil, pe{id: "policy-1", name: "策略-2"}},
		{"非字符串用缺省", `{"id":123,"name":["a"],"icon":null,"default":false}`, 1, nil, pe{id: "policy-1", name: "策略-2"}},
		{"中间空白保留", `{"name":"a  b"}`, 0, nil, pe{id: "policy-0", name: "a  b"}},
		// enabled:只有布尔 false 才为假
		{"enabled false", `{"enabled":false}`, 0, nil, pe{id: "policy-0", name: "策略-1", disabled: true}},
		{"enabled 0 仍启用", `{"enabled":0}`, 0, nil, pe{id: "policy-0", name: "策略-1"}},
		{"enabled null 仍启用", `{"enabled":null}`, 0, nil, pe{id: "policy-0", name: "策略-1"}},
		{"enabled 字符串 false 仍启用", `{"enabled":"false"}`, 0, nil, pe{id: "policy-0", name: "策略-1"}},
		{"enabled 空串仍启用", `{"enabled":""}`, 0, nil, pe{id: "policy-0", name: "策略-1"}},
		{"enabled true", `{"enabled":true}`, 0, nil, pe{id: "policy-0", name: "策略-1"}},
		// ruleUrls 与 rulesets
		{"ruleUrls 只留 http(s)", `{"ruleUrls":["https://example.com/a.srs"," http://x ","ftp://x","example.com","HTTPS://Example.com/a.srs",1,null,"  ","//x","xhttp://x"]}`, 0, nil,
			pe{id: "policy-0", name: "策略-1",
				ruleUrls: `["https://example.com/a.srs","http://x","HTTPS://Example.com/a.srs"]`,
				rulesets: `["` + u1 + `","` + u2 + `","` + u3 + `"]`}},
		{"ruleUrls 重复保留但标签去重", `{"ruleUrls":["https://example.com/a.srs","https://example.com/a.srs"]}`, 0, nil,
			pe{id: "policy-0", name: "策略-1", ruleUrls: `["https://example.com/a.srs","https://example.com/a.srs"]`, rulesets: `["` + u1 + `"]`}},
		{"rulesets 去重保序且在前", `{"rulesets":["b","a","b"," a ",1,null,""],"ruleUrls":["https://example.com/a.srs"]}`, 0, nil,
			pe{id: "policy-0", name: "策略-1", ruleUrls: `["https://example.com/a.srs"]`, rulesets: `["b","a","` + u1 + `"]`}},
		{"ruleUrl 标签已在 rulesets 里", `{"rulesets":["` + u1 + `","z"],"ruleUrls":["https://example.com/a.srs"]}`, 0, nil,
			pe{id: "policy-0", name: "策略-1", ruleUrls: `["https://example.com/a.srs"]`, rulesets: `["` + u1 + `","z"]`}},
		{"ruleUrl 在 rulesets 里不是链接的不折算", `{"rulesets":["https://example.com/a.srs"]}`, 0, nil,
			pe{id: "policy-0", name: "策略-1", rulesets: `["https://example.com/a.srs"]`}},
		// 四个文本列表
		{"非数组一律空", `{"rulesets":"a","ruleUrls":"https://x","domain":{"0":"x"},"domainSuffix":5,"domainKeyword":null,"ipCidr":true}`, 0, nil,
			pe{id: "policy-0", name: "策略-1"}},
		{"文本列表只收字符串、trim、不去重不转小写不校验",
			`{"domain":[" a.com ","A.com","a.com",3,null,"  "],"domainSuffix":[".cn","  ",["x"]],"domainKeyword":["Ad"],"ipCidr":["10.0.0.0/8","bad",1]}`, 0, nil,
			pe{id: "policy-0", name: "策略-1", domain: `["a.com","A.com","a.com"]`, suffix: `[".cn"]`, keyword: `["Ad"]`, cidr: `["10.0.0.0/8","bad"]`}},
		// 规则库名单
		{"名单里没有的 geo 规则集挪走", `{"rulesets":["geosite-cn","geosite-zzz","geoip-cn","custom","list-abcd","geosite-zzz"]}`, 0, []string{"geosite-cn"},
			pe{id: "policy-0", name: "策略-1", rulesets: `["geosite-cn","custom","list-abcd"]`, missing: `["geosite-zzz","geoip-cn"]`}},
		{"没有名单什么都不挪", `{"rulesets":["geosite-zzz","geoip-cn","custom"]}`, 0, nil,
			pe{id: "policy-0", name: "策略-1", rulesets: `["geosite-zzz","geoip-cn","custom"]`}},
		{"名单都在就没有 missing 键", `{"rulesets":["geosite-cn","geoip-cn"]}`, 0, []string{"geosite-cn", "geoip-cn", "x"},
			pe{id: "policy-0", name: "策略-1", rulesets: `["geosite-cn","geoip-cn"]`}},
		{"空名单:只有 geo 前缀的被挪走", `{"rulesets":["geosite-a","geoip-b","geosite","geosite_x","GEOSITE-a","xgeoip-1","plain"]}`, 0, []string{},
			pe{id: "policy-0", name: "策略-1", rulesets: `["geosite","geosite_x","GEOSITE-a","xgeoip-1","plain"]`, missing: `["geosite-a","geoip-b"]`}},
		{"全被挪走", `{"rulesets":["geosite-a"]}`, 0, []string{},
			pe{id: "policy-0", name: "策略-1", missing: `["geosite-a"]`}},
		{"链接折出来的标签不会缺", `{"ruleUrls":["https://example.com/a.srs"]}`, 0, []string{},
			pe{id: "policy-0", name: "策略-1", ruleUrls: `["https://example.com/a.srs"]`, rulesets: `["` + u1 + `"]`}},
		// 备注
		{"备注:键值要非空字符串,值 trim 后截 60 码元", `{"notes":{"a.com":"  备注 ","__proto__":"x","k":5,"e":"   ","  ":"y","l61":"` + long61 + `","l60":"` + long60 + `"}}`, 0, nil,
			pe{id: "policy-0", name: "策略-1", notes: `{"a.com":"备注","l60":"` + long60 + `","l61":"` + long60 + `"}`}},
		{"备注不是对象", `{"notes":["a"]}`, 0, nil, pe{id: "policy-0", name: "策略-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer withGeoList(c.geo)()
			in := mustJSON(t, c.in)
			before := jsonText(t, in)
			got := jsonText(t, normalizePolicy(in, c.idx))
			if got != c.want.String() {
				t.Errorf("不一致\n有: %s\n要: %s", got, c.want.String())
			}
			if after := jsonText(t, in); after != before {
				t.Errorf("改了入参\n前: %s\n后: %s", before, after)
			}
		})
	}
}

func TestNormalizePolicyHasCondition(t *testing.T) {
	defer withGeoList(nil)()
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`{}`, false},
		{`{"rulesets":["a"]}`, true},
		{`{"domain":["a"]}`, true},
		{`{"domainSuffix":["a"]}`, true},
		{`{"domainKeyword":["a"]}`, true},
		{`{"ipCidr":["1.1.1.1"]}`, true},
		{`{"ruleUrls":["https://example.com/a.srs"]}`, true},
		{`{"ruleUrls":["ftp://x"],"notes":{"a":"b"},"default":"proxy"}`, false},
		{`{"domain":["  "],"rulesets":[1]}`, false},
	} {
		if got := normalizePolicy(mustJSON(t, c.in), 0).hasCondition(); got != c.want {
			t.Errorf("%s hasCondition=%v 要 %v", c.in, got, c.want)
		}
	}
	// 引用的规则集全被名单挪走后,站点集没有条件
	defer withGeoList([]string{})()
	if normalizePolicy(mustJSON(t, `{"rulesets":["geosite-a"]}`), 0).hasCondition() {
		t.Error("规则集全被挪走后不该还有条件")
	}
	if !normalizePolicy(mustJSON(t, `{"rulesets":["geosite-a","plain"]}`), 0).hasCondition() {
		t.Error("还剩 plain 应有条件")
	}
}

func TestNormalizeCustomRuleVectors(t *testing.T) {
	defer withGeoList(nil)()
	long61 := strings.Repeat("a", 61)
	long60 := strings.Repeat("a", 60)
	emoji31 := strings.Repeat("😀", 31)
	emoji30 := strings.Repeat("😀", 30)
	cases := []struct {
		name string
		in   string
		want string // 空串表示被丢掉
	}{
		{"null", `null`, ``},
		{"数组", `[{"type":"domain","value":"a","outbound":"x"}]`, ``},
		{"字符串", `"domain"`, ``},
		{"空对象", `{}`, ``},
		{"domainSuffix", `{"type":"domainSuffix","value":".cn","outbound":"直连"}`, `{"type":"domainSuffix","value":".cn","outbound":"直连"}`},
		{"domain", `{"type":"domain","value":"a.com","outbound":"节点"}`, `{"type":"domain","value":"a.com","outbound":"节点"}`},
		{"domainKeyword", `{"type":"domainKeyword","value":"ad","outbound":"拒绝"}`, `{"type":"domainKeyword","value":"ad","outbound":"拒绝"}`},
		{"ipCidr 网段", `{"type":"ipCidr","value":"10.0.0.0/8","outbound":"x"}`, `{"type":"ipCidr","value":"10.0.0.0/8","outbound":"x"}`},
		{"ipCidr 单个地址不写前缀", `{"type":"ipCidr","value":"1.2.3.4","outbound":"x"}`, `{"type":"ipCidr","value":"1.2.3.4","outbound":"x"}`},
		{"ipCidr 前后空白先去掉", `{"type":"ipCidr","value":"  1.2.3.4/32  ","outbound":"x"}`, `{"type":"ipCidr","value":"1.2.3.4/32","outbound":"x"}`},
		{"ipCidr IPv6 网段", `{"type":"ipCidr","value":"2001:db8::/32","outbound":"x"}`, `{"type":"ipCidr","value":"2001:db8::/32","outbound":"x"}`},
		{"ipCidr IPv6 单个地址", `{"type":"ipCidr","value":"::1","outbound":"x"}`, `{"type":"ipCidr","value":"::1","outbound":"x"}`},
		{"ipCidr 主机位非零也收", `{"type":"ipCidr","value":"10.0.0.1/24","outbound":"x"}`, `{"type":"ipCidr","value":"10.0.0.1/24","outbound":"x"}`},
		{"ipCidr 前缀 0 与满长", `{"type":"ipCidr","value":"0.0.0.0/0","outbound":"x"}`, `{"type":"ipCidr","value":"0.0.0.0/0","outbound":"x"}`},
		{"ipCidr 写成别的东西", `{"type":"ipCidr","value":"garbage","outbound":"x"}`, ``},
		{"ipCidr 前缀越界 v4", `{"type":"ipCidr","value":"10.2.0.0/33","outbound":"x"}`, ``},
		{"ipCidr 前缀越界 v6", `{"type":"ipCidr","value":"::1/129","outbound":"x"}`, ``},
		{"ipCidr 前缀为空", `{"type":"ipCidr","value":"1.2.3.4/","outbound":"x"}`, ``},
		{"ipCidr 前缀带符号", `{"type":"ipCidr","value":"1.2.3.4/+8","outbound":"x"}`, ``},
		{"ipCidr 前缀有前导零", `{"type":"ipCidr","value":"1.2.3.4/024","outbound":"x"}`, ``},
		{"ipCidr 地址有前导零", `{"type":"ipCidr","value":"01.2.3.4","outbound":"x"}`, ``},
		{"ipCidr 八位组越界", `{"type":"ipCidr","value":"256.1.1.1","outbound":"x"}`, ``},
		{"ipCidr 段数不够", `{"type":"ipCidr","value":"1.2.3","outbound":"x"}`, ``},
		{"ipCidr 带区域", `{"type":"ipCidr","value":"fe80::1%eth0","outbound":"x"}`, ``},
		{"ipCidr 两个网段逗号连写", `{"type":"ipCidr","value":"1.1.1.1,8.8.8.8","outbound":"x"}`, ``},
		{"ipCidr 规则集名字不是网段", `{"type":"ipCidr","value":"geoip-a","outbound":"x"}`, ``},
		{"非 ipCidr 的 value 不按网段解析", `{"type":"domain","value":"garbage","outbound":"x"}`, `{"type":"domain","value":"garbage","outbound":"x"}`},
		{"geosite", `{"type":"geosite","value":"cn","outbound":"x"}`, `{"type":"geosite","value":"cn","outbound":"x"}`},
		{"geoip 不校验写法", `{"type":"geoip","value":"zz zz","outbound":"x"}`, `{"type":"geoip","value":"zz zz","outbound":"x"}`},
		{"ruleset", `{"type":"ruleset","value":"my-set","outbound":"x"}`, `{"type":"ruleset","value":"my-set","outbound":"x"}`},
		{"ruleUrl", `{"type":"ruleUrl","value":"https://example.com/a.srs","outbound":"x"}`, `{"type":"ruleUrl","value":"https://example.com/a.srs","outbound":"x"}`},
		{"port", `{"type":"port","value":"80,443","outbound":"x"}`, `{"type":"port","value":"80,443","outbound":"x"}`},
		{"多余键忽略", `{"type":"domain","value":"a","outbound":"x","extra":1,"enabled":false}`, `{"type":"domain","value":"a","outbound":"x"}`},
		// type
		{"type 不是字符串", `{"type":1,"value":"a","outbound":"x"}`, ``},
		{"type 缺失", `{"value":"a","outbound":"x"}`, ``},
		{"type 不认识", `{"type":"cidr","value":"a","outbound":"x"}`, ``},
		{"type 区分大小写", `{"type":"Domain","value":"a","outbound":"x"}`, ``},
		{"type 不 trim", `{"type":" domain","value":"a","outbound":"x"}`, ``},
		{"type 下划线写法不认", `{"type":"domain_suffix","value":"a","outbound":"x"}`, ``},
		{"type ipcidr 小写不认", `{"type":"ipcidr","value":"1.1.1.1","outbound":"x"}`, ``},
		// value / outbound
		{"value trim", "{\"type\":\"domain\",\"value\":\"\\u3000a.com \",\"outbound\":\"x\"}", `{"type":"domain","value":"a.com","outbound":"x"}`},
		{"outbound trim", `{"type":"domain","value":"a.com","outbound":"  x  "}`, `{"type":"domain","value":"a.com","outbound":"x"}`},
		{"value 空白", `{"type":"domain","value":"  ","outbound":"x"}`, ``},
		{"value 缺失", `{"type":"domain","outbound":"x"}`, ``},
		{"value 非字符串", `{"type":"domain","value":5,"outbound":"x"}`, ``},
		{"outbound 空串", `{"type":"domain","value":"a","outbound":""}`, ``},
		{"outbound 缺失", `{"type":"domain","value":"a"}`, ``},
		{"outbound 非字符串", `{"type":"domain","value":"a","outbound":["x"]}`, ``},
		// ruleUrl / port 的额外校验
		{"ruleUrl 大写协议", `{"type":"ruleUrl","value":"HTTP://x","outbound":"x"}`, `{"type":"ruleUrl","value":"HTTP://x","outbound":"x"}`},
		{"ruleUrl 前后空白先去掉", `{"type":"ruleUrl","value":"  https://x  ","outbound":"x"}`, `{"type":"ruleUrl","value":"https://x","outbound":"x"}`},
		{"ruleUrl 非 http", `{"type":"ruleUrl","value":"ftp://x","outbound":"x"}`, ``},
		{"ruleUrl 没协议", `{"type":"ruleUrl","value":"example.com/a.srs","outbound":"x"}`, ``},
		{"非 ruleUrl 的 value 不看协议", `{"type":"domain","value":"ftp://x","outbound":"x"}`, `{"type":"domain","value":"ftp://x","outbound":"x"}`},
		{"port 区间", `{"type":"port","value":" 1000-2000 ","outbound":"x"}`, `{"type":"port","value":"1000-2000","outbound":"x"}`},
		{"port 写错", `{"type":"port","value":"abc","outbound":"x"}`, ``},
		{"port 超界", `{"type":"port","value":"70000","outbound":"x"}`, ``},
		{"port 倒置", `{"type":"port","value":"2000-1000","outbound":"x"}`, ``},
		{"port 一段错整体丢", `{"type":"port","value":"80,abc","outbound":"x"}`, ``},
		{"非 port 的 value 不按端口解析", `{"type":"domain","value":"abc","outbound":"x"}`, `{"type":"domain","value":"abc","outbound":"x"}`},
		// note
		{"note trim", `{"type":"domain","value":"a","outbound":"x","note":"  说明  "}`, `{"type":"domain","value":"a","outbound":"x","note":"说明"}`},
		{"note 空白不带", `{"type":"domain","value":"a","outbound":"x","note":"   "}`, `{"type":"domain","value":"a","outbound":"x"}`},
		{"note 非字符串不带", `{"type":"domain","value":"a","outbound":"x","note":5}`, `{"type":"domain","value":"a","outbound":"x"}`},
		{"note 60 码元不动", `{"type":"domain","value":"a","outbound":"x","note":"` + long60 + `"}`, `{"type":"domain","value":"a","outbound":"x","note":"` + long60 + `"}`},
		{"note 超 60 截断", `{"type":"domain","value":"a","outbound":"x","note":"` + long61 + `"}`, `{"type":"domain","value":"a","outbound":"x","note":"` + long60 + `"}`},
		{"note 按 UTF-16 码元数(emoji 两个)", `{"type":"domain","value":"a","outbound":"x","note":"` + emoji31 + `"}`, `{"type":"domain","value":"a","outbound":"x","note":"` + emoji30 + `"}`},
		{"note 先 trim 再截", `{"type":"domain","value":"a","outbound":"x","note":"   ` + long61 + `   "}`, `{"type":"domain","value":"a","outbound":"x","note":"` + long60 + `"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := mustJSON(t, c.in)
			before := jsonText(t, in)
			r, ok := normalizeCustomRule(in)
			if c.want == "" {
				if ok {
					t.Errorf("该丢掉却收了:%s", jsonText(t, r))
				}
				if (r != CustomRule{}) {
					t.Errorf("丢掉时该返回零值:%+v", r)
				}
			} else {
				if !ok {
					t.Fatalf("不该丢掉,要 %s", c.want)
				}
				if got := jsonText(t, r); got != c.want {
					t.Errorf("不一致\n有: %s\n要: %s", got, c.want)
				}
			}
			if after := jsonText(t, in); after != before {
				t.Errorf("改了入参\n前: %s\n后: %s", before, after)
			}
		})
	}
}

func TestNormalizeCustomPolicyVectors(t *testing.T) {
	const def = `"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[]`
	r := func(typ, val string) string {
		return fmt.Sprintf(`{"type":%q,"value":%q,"outbound":"x"}`, typ, val)
	}
	cases := []struct {
		name string
		in   string
		geo  []string
		want string
	}{
		{"null", `null`, nil, `{` + def + `}`},
		{"数组", `[1]`, nil, `{` + def + `}`},
		{"字符串", `"a"`, nil, `{` + def + `}`},
		{"空对象", `{}`, nil, `{` + def + `}`},
		{"名字图标 trim", `{"name":"  我的 ","icon":" a:b "}`, nil, `{"name":"我的","icon":"a:b","iconScale":0,"enabled":true,"rules":[]}`},
		{"名字图标空白用缺省", `{"name":"  ","icon":"\t"}`, nil, `{` + def + `}`},
		{"名字图标非字符串用缺省", `{"name":1,"icon":["a"]}`, nil, `{` + def + `}`},
		{"enabled false", `{"enabled":false}`, nil, `{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":false,"rules":[]}`},
		{"enabled 0 仍启用", `{"enabled":0}`, nil, `{` + def + `}`},
		{"enabled null 仍启用", `{"enabled":null}`, nil, `{` + def + `}`},
		{"iconScale 整数原样", `{"iconScale":150}`, nil, `{"name":"前置自定义分流","icon":"misc:pin","iconScale":150,"enabled":true,"rules":[]}`},
		{"iconScale 数字串先转数", `{"iconScale":"120"}`, nil, `{"name":"前置自定义分流","icon":"misc:pin","iconScale":120,"enabled":true,"rules":[]}`},
		{"iconScale 小数用 0", `{"iconScale":1.5}`, nil, `{` + def + `}`},
		{"iconScale 数组用 0", `{"iconScale":[100]}`, nil, `{` + def + `}`},
		{"rules 非数组", `{"rules":{"0":` + r("domain", "a") + `}}`, nil, `{` + def + `}`},
		{"rules 保序、写错的丢掉、不去重", `{"rules":[` + r("domain", "b.com") + `,{"type":"x"},5,null,` + r("port", "abc") + `,` + r("domain", "a.com") + `,` + r("domain", "b.com") + `]}`, nil,
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[` + r("domain", "b.com") + `,` + r("domain", "a.com") + `,` + r("domain", "b.com") + `]}`},
		{"没有名单:geo 行都留着", `{"rules":[` + r("geosite", "zzz") + `,` + r("geoip", "zz") + `]}`, nil,
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[` + r("geosite", "zzz") + `,` + r("geoip", "zz") + `]}`},
		{"名单里有的留着", `{"rules":[` + r("geosite", "cn") + `]}`, []string{"geosite-cn"},
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[` + r("geosite", "cn") + `]}`},
		{"名单里没有的整行丢掉并按行记", `{"rules":[` + r("domain", "a.com") + `,` + r("geosite", "zzz") + `,` + r("geoip", "cn") + `,` + r("geosite", "zzz") + `,` + r("geosite", "cn") + `]}`, []string{"geosite-cn"},
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[` + r("domain", "a.com") + `,` + r("geosite", "cn") + `],"missingRulesets":["geosite-zzz","geoip-cn","geosite-zzz"]}`},
		{"ruleset 行按它的值判断", `{"rules":[` + r("ruleset", "geosite-abc") + `,` + r("ruleset", "my-set") + `,` + r("ruleset", "geoip-abc") + `]}`, []string{"geoip-abc"},
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[` + r("ruleset", "my-set") + `,` + r("ruleset", "geoip-abc") + `],"missingRulesets":["geosite-abc"]}`},
		{"ruleUrl 行与域名行不受名单影响", `{"rules":[` + r("ruleUrl", "https://example.com/a.srs") + `,` + r("domain", "geosite-a") + `,` + r("ipCidr", "10.0.0.0/8") + `,` + r("port", "80") + `]}`, []string{},
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[` + r("ruleUrl", "https://example.com/a.srs") + `,` + r("domain", "geosite-a") + `,` + r("ipCidr", "10.0.0.0/8") + `,` + r("port", "80") + `]}`},
		{"空名单:geo 行全丢", `{"rules":[` + r("geosite", "a") + `,` + r("geoip", "b") + `]}`, []string{},
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[],"missingRulesets":["geosite-a","geoip-b"]}`},
		{"停用时照样整理", `{"enabled":false,"rules":[` + r("geosite", "a") + `]}`, []string{},
			`{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":false,"rules":[],"missingRulesets":["geosite-a"]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer withGeoList(c.geo)()
			in := mustJSON(t, c.in)
			before := jsonText(t, in)
			if got := jsonText(t, normalizeCustomPolicy(in)); got != c.want {
				t.Errorf("不一致\n有: %s\n要: %s", got, c.want)
			}
			if after := jsonText(t, in); after != before {
				t.Errorf("改了入参\n前: %s\n后: %s", before, after)
			}
		})
	}
}

func TestNormalizeRoutingVectors(t *testing.T) {
	const cust = `{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[]}`
	const fb = `{"name":"其他","icon":"globe:earth-meridians","default":""}`
	empty := `{"custom":` + cust + `,"policies":[],"activePolicies":[],"fallback":` + fb + `}`
	a := pe{id: "a", name: "A", domain: `["a.com"]`}
	b := pe{id: "b", name: "B", domain: `["b.com"]`}
	bOff := b
	bOff.disabled = true
	routing := func(pols, active, fallback string) string {
		return `{"custom":` + cust + `,"policies":[` + pols + `],"activePolicies":[` + active + `],"fallback":` + fallback + `}`
	}
	cases := []struct {
		name string
		in   string
		geo  []string
		want string
	}{
		{"null", `null`, nil, empty},
		{"数组", `[1]`, nil, empty},
		{"字符串", `"x"`, nil, empty},
		{"数字", `3`, nil, empty},
		{"空对象", `{}`, nil, empty},
		{"policies 非数组", `{"policies":{"0":{"domain":["a"]}}}`, nil, empty},
		{"policies 里非对象、没有条件的都丢", `{"policies":[null,5,"x",{},{"name":"n"},{"rulesets":[1]}]}`, nil, empty},
		{"站点集保序,activePolicies 只留启用的", `{"policies":[{"id":"a","name":"A","domain":["a.com"]},{"id":"b","name":"B","enabled":false,"domain":["b.com"]}]}`, nil,
			routing(a.String()+","+bOff.String(), a.String(), fb)},
		{"下标按原数组算(被丢的也占位)", `{"policies":[{"name":"空"},{"domain":["x.com"]}]}`, nil,
			routing(pe{id: "policy-1", name: "策略-2", domain: `["x.com"]`}.String(), pe{id: "policy-1", name: "策略-2", domain: `["x.com"]`}.String(), fb)},
		{"名字叫「其他」的丢掉(trim 后比)", `{"policies":[{"name":" 其他 ","domain":["x"]},{"id":"a","name":"A","domain":["a.com"]}]}`, nil,
			routing(a.String(), a.String(), fb)},
		{"名字叫兜底名的丢掉,「其他」同样丢", `{"fallbackName":"兜底","policies":[{"name":"兜底","domain":["x"]},{"name":"其他","domain":["y"]},{"id":"a","name":"A","domain":["a.com"]}]}`, nil,
			routing(a.String(), a.String(), `{"name":"兜底","icon":"globe:earth-meridians","default":""}`)},
		{"名字只是像兜底名不算", `{"policies":[{"id":"a","name":"其他2","domain":["x"]},{"id":"b","name":"其 他","domain":["y"]}]}`, nil,
			routing(pe{id: "a", name: "其他2", domain: `["x"]`}.String()+","+pe{id: "b", name: "其 他", domain: `["y"]`}.String(),
				pe{id: "a", name: "其他2", domain: `["x"]`}.String()+","+pe{id: "b", name: "其 他", domain: `["y"]`}.String(), fb)},
		{"兜底三项 trim", "{\"fallbackName\":\"\\u3000兜底 \",\"fallbackIcon\":\" i:y \",\"fallbackDefault\":\" proxy \"}", nil,
			`{"custom":` + cust + `,"policies":[],"activePolicies":[],"fallback":{"name":"兜底","icon":"i:y","default":"proxy"}}`},
		{"兜底三项空白 / 非字符串用缺省", `{"fallbackName":"  ","fallbackIcon":5,"fallbackDefault":["x"]}`, nil, empty},
		{"前置自定义分流整理", `{"custom":{"name":"我的","iconScale":2,"rules":[{"type":"domain","value":"a","outbound":"x"},{"type":"bad"}]}}`, nil,
			`{"custom":{"name":"我的","icon":"misc:pin","iconScale":2,"enabled":true,"rules":[{"type":"domain","value":"a","outbound":"x"}]},"policies":[],"activePolicies":[],"fallback":` + fb + `}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer withGeoList(c.geo)()
			in := mustJSON(t, c.in)
			before := jsonText(t, in)
			if got := jsonText(t, normalizeRouting(in)); got != c.want {
				t.Errorf("不一致\n有: %s\n要: %s", got, c.want)
			}
			if after := jsonText(t, in); after != before {
				t.Errorf("改了入参\n前: %s\n后: %s", before, after)
			}
		})
	}
}

func TestNormalizeRoutingMigratesFirst(t *testing.T) {
	defer withGeoList(nil)()
	// 老档案先迁移再归一化:选中大陆地区 → 一个「中国大陆·直连」站点集、兜底默认走代理
	cn := pe{id: "region-cn-direct", name: "中国大陆·直连", icon: "globe:earth-meridians", def: "direct", rulesets: `["geosite-cn","geoip-cn"]`}
	want := `{"custom":{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[]},"policies":[` + cn.String() +
		`],"activePolicies":[` + cn.String() + `],"fallback":{"name":"其他","icon":"globe:earth-meridians","default":"proxy"}}`
	in := mustJSON(t, `{"regionMode":"CN","proxyTag":"p"}`)
	if got := jsonText(t, normalizeRouting(in)); got != want {
		t.Errorf("不一致\n有: %s\n要: %s", got, want)
	}
	// 迁移出来的站点集同样受兜底名过滤:兜底名改成「中国大陆·直连」时它被丢掉
	in = mustJSON(t, `{"regionMode":"CN","fallbackName":"中国大陆·直连"}`)
	got := normalizeRouting(in)
	if len(got.Policies) != 0 || len(got.ActivePolicies) != 0 || got.Fallback.Name != "中国大陆·直连" || got.Fallback.Default != "proxy" {
		t.Errorf("迁移出的站点集该被兜底名过滤:%s", jsonText(t, got))
	}
}

func TestNormalizeRoutingMissingRulesets(t *testing.T) {
	defer withGeoList([]string{"geosite-ok"})()
	in := mustJSON(t, `{
	  "custom":{"name":"自定义","rules":[
	    {"type":"geosite","value":"cx","outbound":"x"},
	    {"type":"geoip","value":"ok","outbound":"x"}]},
	  "policies":[
	    {"id":"a","name":"A","domain":["a.com"],"rulesets":["geosite-ok","geosite-a1","geoip-a2"]},
	    {"id":"b","name":"B","enabled":false,"domain":["b.com"],"rulesets":["geosite-b1"]},
	    {"id":"c","name":"C","rulesets":["geoip-c1"]},
	    {"id":"d","name":"其他","rulesets":["geosite-d1"],"domain":["d.com"]},
	    {"id":"e","name":"E","rulesets":["geosite-e1","geosite-e2"],"ipCidr":["10.0.0.0/8"]}]}`)
	got := normalizeRouting(in)
	want := `[{"tag":"geosite-cx","owner":"自定义"},{"tag":"geoip-ok","owner":"自定义"},{"tag":"geosite-a1","owner":"A"},{"tag":"geoip-a2","owner":"A"},` +
		`{"tag":"geoip-c1","owner":"C"},{"tag":"geosite-d1","owner":"其他"},{"tag":"geosite-e1","owner":"E"},{"tag":"geosite-e2","owner":"E"}]`
	if g := jsonText(t, got.MissingRulesets); g != want {
		t.Errorf("missingRulesets 不一致\n有: %s\n要: %s", g, want)
	}
	// 停用的站点集(B)不算;没有条件的 C 与叫「其他」的 D 被 policies 过滤掉,但它们启用着,缺的规则集仍报
	var ids []string
	for _, p := range got.Policies {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "e"}) {
		t.Errorf("policies 的 id %v 要 [a b e]", ids)
	}
	ids = nil
	for _, p := range got.ActivePolicies {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "e"}) {
		t.Errorf("activePolicies 的 id %v 要 [a e]", ids)
	}
	// 前置自定义分流停用时,它缺的规则集不报
	in = mustJSON(t, `{"custom":{"enabled":false,"rules":[{"type":"geosite","value":"cx","outbound":"x"}]},"policies":[{"id":"a","name":"A","domain":["a.com"],"rulesets":["geosite-a1"]}]}`)
	got = normalizeRouting(in)
	if g := jsonText(t, got.MissingRulesets); g != `[{"tag":"geosite-a1","owner":"A"}]` {
		t.Errorf("自定义停用时 missingRulesets=%s", g)
	}
	if got.Custom.MissingRulesets == nil || got.Custom.Enabled {
		t.Errorf("自定义本身仍记着缺的:%+v", got.Custom)
	}
	// 没有缺的就没有这个键
	defer withGeoList(nil)()
	out := jsonText(t, normalizeRouting(in))
	if strings.Contains(out, `"missingRulesets"`) {
		t.Errorf("没有名单时不该带 missingRulesets:%s", out)
	}
}

func TestNormalizeRoutingHandlers(t *testing.T) {
	reg := protocol.NewRegistry()
	Register(reg)
	run := func(method, args, geo string) any {
		return call(t, reg, []byte(`{"protocol":1,"method":"`+method+`","args":`+args+geo+`}`))
	}
	check := func(name string, got any, want string) {
		t.Helper()
		if w := mustJSON(t, want); !reflect.DeepEqual(got, w) {
			gb, _ := json.Marshal(got)
			t.Errorf("%s 不一致\n有: %s\n要: %s", name, gb, want)
		}
	}
	base := pe{id: "policy-0", name: "策略-1"}
	check("normalizePolicy 缺参数", run("normalizePolicy", `[]`, ``), base.String())
	check("normalizePolicy null", run("normalizePolicy", `[null]`, ``), base.String())
	check("normalizePolicy 下标", run("normalizePolicy", `[{},4]`, ``), pe{id: "policy-4", name: "策略-5"}.String())
	check("normalizePolicy 下标不是数字当 0", run("normalizePolicy", `[{},"4"]`, ``), base.String())
	check("normalizePolicy 带名单", run("normalizePolicy", `[{"rulesets":["geosite-a","geosite-b"]},0]`, `,"geoTags":["geosite-a"]`),
		pe{id: "policy-0", name: "策略-1", rulesets: `["geosite-a"]`, missing: `["geosite-b"]`}.String())
	check("名单不串到下一次", run("normalizePolicy", `[{"rulesets":["geosite-a","geosite-b"]},0]`, ``),
		pe{id: "policy-0", name: "策略-1", rulesets: `["geosite-a","geosite-b"]`}.String())
	const cust = `{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[]}`
	check("normalizeRouting 缺参数", run("normalizeRouting", `[]`, ``),
		`{"custom":`+cust+`,"policies":[],"activePolicies":[],"fallback":{"name":"其他","icon":"globe:earth-meridians","default":""}}`)
	check("normalizeRouting 带名单", run("normalizeRouting", `[{"policies":[{"id":"a","name":"A","domain":["a.com"],"rulesets":["geosite-x"]}]}]`, `,"geoTags":[]`),
		`{"custom":`+cust+`,"policies":[`+pe{id: "a", name: "A", domain: `["a.com"]`, missing: `["geosite-x"]`}.String()+`],"activePolicies":[`+
			pe{id: "a", name: "A", domain: `["a.com"]`, missing: `["geosite-x"]`}.String()+`],"fallback":{"name":"其他","icon":"globe:earth-meridians","default":""},"missingRulesets":[{"tag":"geosite-x","owner":"A"}]}`)
}
