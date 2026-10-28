package clientapp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// anyOf 把 JSON 文本解成 any(数字是 float64,和 HTTP 请求体解出来的一样)。
func anyOf(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("用例 JSON 无效: %v\n%s", err, s)
	}
	return v
}

// jsonRoundTrip 把任意值编成 JSON 再解回 any,用来和规整结果比较。
func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return anyOf(t, string(b))
}

// 最小的合法一组;各用例在它上面改。
const okGroup = `{"id":"g1","name":"组一","regions":["JP"],"rules":[],"catchAll":"direct"}`

func groupWith(t *testing.T, patch string) []any {
	t.Helper()
	g := anyOf(t, okGroup).(map[string]any)
	for k, v := range anyOf(t, patch).(map[string]any) {
		if v == nil {
			delete(g, k)
		} else {
			g[k] = v
		}
	}
	return []any{g}
}

// 默认三组原样送进来,规整后必须不变(否则「前端读到默认值再原样保存」会悄悄改掉数据)。
func TestDefaultsAreFixedPointOfNormalize(t *testing.T) {
	in := jsonRoundTrip(t, DefaultShareRegions())
	out, err := NormalizeShareRegions(in)
	if err != nil {
		t.Fatalf("默认三组应通过校验: %v", err)
	}
	if !reflect.DeepEqual(jsonRoundTrip(t, out), in) {
		t.Fatalf("规整改动了默认三组:\n得到 %v\n原样 %v", out, in)
	}
	// 规整是幂等的
	again, err := NormalizeShareRegions(out)
	if err != nil || !reflect.DeepEqual(again, out) {
		t.Fatalf("规整不幂等: %v %v", again, err)
	}
	// 存下来再读出来,和默认值一致,并且算「改过」
	groups, customized := regionsOf(out)
	if !customized || !reflect.DeepEqual(jsonRoundTrip(t, groups), in) {
		t.Fatalf("存下再读不一致: customized=%v", customized)
	}
}

func TestNormalizeShareRegionsEmptyAndShape(t *testing.T) {
	out, err := NormalizeShareRegions(anyOf(t, `[]`))
	if err != nil || out == nil || len(out) != 0 {
		t.Fatalf("空数组 = 恢复默认,应合法且是空切片: %#v %v", out, err)
	}
	for _, bad := range []string{`{}`, `"x"`, `5`, `null`} {
		if _, err := NormalizeShareRegions(anyOf(t, bad)); err == nil || !strings.Contains(err.Error(), "must be an array") {
			t.Errorf("%s 应报「必须是数组」: %v", bad, err)
		}
	}
	if _, err := NormalizeShareRegions(anyOf(t, `[1]`)); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Errorf("元素不是对象: %v", err)
	}
}

func TestNormalizeGroupTrimsFillsAndDropsUnknown(t *testing.T) {
	in := anyOf(t, `[{"id":"g1","name":"  组一 ","description":" 说明 ","regions":["JP","JP","KR"],
		"rules":[{"type":"domainSuffix","value":" example.com ","action":"proxy"}],
		"catchAll":"direct","junk":{"a":1}}]`)
	out, err := NormalizeShareRegions(in)
	if err != nil {
		t.Fatal(err)
	}
	g := out[0].(map[string]any)
	if g["name"] != "组一" || g["description"] != "说明" {
		t.Errorf("名称 / 说明应去首尾空白: %v %v", g["name"], g["description"])
	}
	if !reflect.DeepEqual(g["regions"], []any{"JP", "KR"}) {
		t.Errorf("组内重复的地区应去重: %v", g["regions"])
	}
	if _, has := g["junk"]; has {
		t.Error("不认识的键应丢掉")
	}
	if r := g["rules"].([]any)[0].(map[string]any); r["value"] != "example.com" {
		t.Errorf("规则值应去空白: %v", r)
	}
	// 没给 dns:按地区补默认(不含大陆 → 两侧都是系统 DNS)
	want := jsonRoundTrip(t, regionDNS(false))
	if !reflect.DeepEqual(jsonRoundTrip(t, g["dns"]), want) {
		t.Errorf("缺 dns 时应补默认值:\n%v\n%v", g["dns"], want)
	}
	// 含大陆 → 代理侧默认 TCP 1.1.1.1;「在这些地区之外」且列了 CN = 不含大陆
	cn, err := NormalizeShareRegions(groupWith(t, `{"regions":["CN"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := cn[0].(map[string]any)["dns"].(map[string]any); d["proxy"] != "1.1.1.1" || d["proxyProtocol"] != "tcp" {
		t.Errorf("含大陆的默认 dns: %v", d)
	}
	outside, err := NormalizeShareRegions(groupWith(t, `{"regions":["CN"],"regionMatch":"outside"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := outside[0].(map[string]any)["dns"].(map[string]any); d["proxy"] != "wan" {
		t.Errorf("「CN 之外」不含大陆,默认 dns 应是系统 DNS: %v", d)
	}
	// regionMatch: inside 与不写等价,规整后不带该键;default: false 也不带
	inside, err := NormalizeShareRegions(groupWith(t, `{"regionMatch":"inside","default":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if g := inside[0].(map[string]any); g["regionMatch"] != nil || g["default"] != nil {
		t.Errorf("inside / false 不该留在结果里: %v", g)
	}
	// 入参不被改动
	if !reflect.DeepEqual(in, anyOf(t, `[{"id":"g1","name":"  组一 ","description":" 说明 ","regions":["JP","JP","KR"],
		"rules":[{"type":"domainSuffix","value":" example.com ","action":"proxy"}],
		"catchAll":"direct","junk":{"a":1}}]`)) {
		t.Error("规整不应改动入参")
	}
}

func TestShareRegionsErrors(t *testing.T) {
	big := func(n int, f func(i int) string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = f(i)
		}
		return strings.Join(parts, ",")
	}
	cases := []struct {
		name  string
		input string // 整个 shareRegions 数组;以 `+` 开头表示在 okGroup 上改字段
		want  string
	}{
		{"id 为空", `+{"id":""}`, "id must match"},
		{"id 含大写", `+{"id":"Abc"}`, "id must match"},
		{"id 太长", `+{"id":"` + strings.Repeat("a", 33) + `"}`, "id must match"},
		{"id 不是字符串", `+{"id":5}`, "id must be a string"},
		{"没有名称", `+{"name":null}`, "name is required"},
		{"名称全是空白", `+{"name":"   "}`, "name is required"},
		{"名称 31 字", `+{"name":"` + strings.Repeat("字", 31) + `"}`, "name is longer than 30"},
		{"说明 121 字", `+{"description":"` + strings.Repeat("字", 121) + `"}`, "description is longer than 120"},
		{"地区小写", `+{"regions":["jp"]}`, "two-letter uppercase"},
		{"地区三个字母", `+{"regions":["JPN"]}`, "two-letter uppercase"},
		{"地区不是字符串", `+{"regions":[5]}`, "two-letter uppercase"},
		{"regions 不是数组", `+{"regions":"JP"}`, "regions must be an array"},
		{"regionMatch 非法", `+{"regionMatch":"around"}`, "regionMatch must be inside or outside"},
		{"outside 没有地区", `+{"regionMatch":"outside","regions":[]}`, "outside group needs at least one region"},
		{"default 不是布尔", `+{"default":"yes"}`, "default must be true or false"},
		{"catchAll 缺失", `+{"catchAll":null}`, "catchAll must be direct or proxy"},
		{"catchAll 非法", `+{"catchAll":"block"}`, "catchAll must be direct or proxy"},
		{"规则类型非法", `+{"rules":[{"type":"regex","value":"a","action":"direct"}]}`, "type must be one of"},
		{"规则走向非法", `+{"rules":[{"type":"domain","value":"a.com","action":"block"}]}`, "action must be direct or proxy"},
		{"规则值为空", `+{"rules":[{"type":"domain","value":"  ","action":"direct"}]}`, "is not a valid domain value"},
		{"规则不是对象", `+{"rules":["a.com"]}`, "must be an object"},
		{"规则列表不是数组", `+{"rules":{}}`, "rules must be an array"},
		{"规则 201 条", `+{"rules":[` + big(201, func(i int) string { return fmt.Sprintf(`{"type":"domain","value":"a%d.com","action":"direct"}`, i) }) + `]}`, "at most 200 rules"},
		{"geosite 大写", `+{"rules":[{"type":"geosite","value":"CN","action":"direct"}]}`, "is not a valid geosite value"},
		{"geoip 含空格", `+{"rules":[{"type":"geoip","value":"c n","action":"direct"}]}`, "is not a valid geoip value"},
		{"域名含斜杠", `+{"rules":[{"type":"domainSuffix","value":"a.com/x","action":"direct"}]}`, "is not a valid domainSuffix value"},
		{"域名含空格", `+{"rules":[{"type":"domainKeyword","value":"a b","action":"direct"}]}`, "is not a valid domainKeyword value"},
		{"ipcidr 不是 IP", `+{"rules":[{"type":"ipcidr","value":"example.com","action":"direct"}]}`, "is not a valid ipcidr value"},
		{"ipcidr 前缀越界", `+{"rules":[{"type":"ipcidr","value":"10.0.0.0/33","action":"direct"}]}`, "is not a valid ipcidr value"},
		{"ipcidr v6 前缀越界", `+{"rules":[{"type":"ipcidr","value":"fe80::/129","action":"direct"}]}`, "is not a valid ipcidr value"},
		{"ipcidr 前缀不是数字", `+{"rules":[{"type":"ipcidr","value":"10.0.0.0/a","action":"direct"}]}`, "is not a valid ipcidr value"},
		{"ipcidr 多个斜杠", `+{"rules":[{"type":"ipcidr","value":"10.0.0.0/8/8","action":"direct"}]}`, "is not a valid ipcidr value"},
		{"ipcidr 带区域", `+{"rules":[{"type":"ipcidr","value":"fe80::1%eth0","action":"direct"}]}`, "is not a valid ipcidr value"},
		{"链接不是 http", `+{"rules":[{"type":"ruleUrl","value":"ftp://a.com/x","action":"direct"}]}`, "is not a valid ruleUrl value"},
		{"链接含空格", `+{"rules":[{"type":"ruleUrl","value":"https://a.com/a b","action":"direct"}]}`, "is not a valid ruleUrl value"},
		{"链接太长", `+{"rules":[{"type":"ruleUrl","value":"https://a.com/` + strings.Repeat("a", 2048) + `","action":"direct"}]}`, "is not a valid ruleUrl value"},
		{"dns 不是对象", `+{"dns":"wan"}`, "dns must be an object"},
		{"直连上游填域名", `+{"dns":{"direct":"dns.example.com"}}`, "server must be an IP address or wan"},
		{"上游带区域", `+{"dns":{"proxy":"fe80::1%eth0"}}`, "server must be an IP address or wan"},
		{"协议非法", `+{"dns":{"directProtocol":"tls","direct":"1.1.1.1"}}`, "protocol must be udp or tcp"},
		{"端口 0", `+{"dns":{"directPort":0,"direct":"1.1.1.1"}}`, "port must be an integer between 1 and 65535"},
		{"端口越界", `+{"dns":{"proxyPort":70000,"proxy":"1.1.1.1"}}`, "port must be an integer between 1 and 65535"},
		{"端口是小数", `+{"dns":{"proxyPort":53.5,"proxy":"1.1.1.1"}}`, "port must be an integer between 1 and 65535"},
		{"端口是字符串", `+{"dns":{"proxyPort":"53","proxy":"1.1.1.1"}}`, "port must be an integer between 1 and 65535"},
		{"备用 4 个", `+{"dns":{"directExtras":[{"server":"1.1.1.1"},{"server":"1.0.0.1"},{"server":"8.8.8.8"},{"server":"8.8.4.4"}]}}`, "at most 3 extra upstreams"},
		{"备用缺 server", `+{"dns":{"proxyExtras":[{"port":53}]}}`, "server is required"},
		{"备用 server 为空", `+{"dns":{"proxyExtras":[{"server":" "}]}}`, "server is required"},
		{"备用填域名", `+{"dns":{"proxyExtras":[{"server":"dns.example.com"}]}}`, "server must be an IP address or wan"},
		{"备用不是数组", `+{"dns":{"directExtras":{}}}`, "directExtras must be an array"},
		{"备用元素不是对象", `+{"dns":{"directExtras":["1.1.1.1"]}}`, "must be an object"},
		{"组 id 重复", `[` + okGroup + `,` + okGroup + `]`, "duplicate group id g1"},
		{"13 组", `[` + big(13, func(i int) string {
			return fmt.Sprintf(`{"id":"g%d","name":"n","regions":[],"rules":[],"catchAll":"direct"}`, i)
		}) + `]`, "at most 12 groups"},
		{"两个默认组", `[{"id":"a","name":"a","regions":[],"rules":[],"catchAll":"direct","default":true},{"id":"b","name":"b","regions":[],"rules":[],"catchAll":"direct","default":true}]`, "only one default group is allowed (a and b)"},
		{"地区被两组占用", `[{"id":"cn","name":"a","regions":["CN"],"rules":[],"catchAll":"direct"},{"id":"x","name":"b","regions":["US","CN"],"rules":[],"catchAll":"direct"}]`, "region CN is in both cn and x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var in any
			if strings.HasPrefix(c.input, "+") {
				in = groupWith(t, c.input[1:])
			} else {
				in = anyOf(t, c.input)
			}
			_, err := NormalizeShareRegions(in)
			if err == nil {
				t.Fatalf("应报错(含 %q),却通过了", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误 %q 不含 %q", err.Error(), c.want)
			}
			if len(err.Error()) > 400 {
				t.Fatalf("错误信息太长,可能把输入整段回显了: %d 字节", len(err.Error()))
			}
		})
	}
}

func TestShareRegionsAccepts(t *testing.T) {
	cases := map[string]string{
		"恰好 12 组": `[` + strings.Join(func() []string {
			var l []string
			for i := 0; i < 12; i++ {
				l = append(l, fmt.Sprintf(`{"id":"g%d","name":"n","regions":[],"rules":[],"catchAll":"proxy"}`, i))
			}
			return l
		}(), ",") + `]`,
		"名称恰好 30 字":       `+{"name":"` + strings.Repeat("字", 30) + `"}`,
		"说明恰好 120 字":      `+{"description":"` + strings.Repeat("字", 120) + `"}`,
		"id 恰好 32 位":      `+{"id":"` + strings.Repeat("a", 32) + `"}`,
		"id 含下划线连字符":      `+{"id":"a_b-c1"}`,
		"没有地区的非默认组":       `+{"regions":[]}`,
		"「之外」组可以和别的组共用地区": `[{"id":"cn","name":"a","regions":["CN"],"rules":[],"catchAll":"direct"},{"id":"o","name":"b","regions":["CN"],"regionMatch":"outside","rules":[],"catchAll":"direct"}]`,
		"「之外」组之间可以共用地区":   `[{"id":"a","name":"a","regions":["CN"],"regionMatch":"outside","rules":[],"catchAll":"direct"},{"id":"b","name":"b","regions":["CN"],"regionMatch":"outside","rules":[],"catchAll":"direct"}]`,
		"恰好 200 条规则": `+{"rules":[` + strings.Join(func() []string {
			var l []string
			for i := 0; i < 200; i++ {
				l = append(l, fmt.Sprintf(`{"type":"domainSuffix","value":"a%d.com","action":"proxy"}`, i))
			}
			return l
		}(), ",") + `]}`,
		"各类规则": `+{"rules":[
			{"type":"geosite","value":"geolocation-!cn@x","action":"direct"},
			{"type":"geoip","value":"cn","action":"proxy"},
			{"type":"domain","value":"a.example.com","action":"direct"},
			{"type":"domainSuffix","value":".example.com","action":"direct"},
			{"type":"domainKeyword","value":"ads","action":"proxy"},
			{"type":"domainSuffix","value":"*.example.com","action":"proxy"},
			{"type":"ipcidr","value":"10.0.0.0/8","action":"direct"},
			{"type":"ipcidr","value":"192.168.1.1","action":"direct"},
			{"type":"ipcidr","value":"fd00::/8","action":"direct"},
			{"type":"ipcidr","value":"2001:db8::1","action":"proxy"},
			{"type":"ruleUrl","value":"https://example.com/a.list?token=x","action":"proxy"},
			{"type":"ruleUrl","value":"HTTP://example.com/b","action":"direct"}]}`,
		"IPv6 上游和备用": `+{"dns":{"direct":"2606:4700:4700::1111","directProtocol":"tcp","directPort":5353,
			"proxy":"8.8.8.8","proxyExtras":[{"server":"8.8.4.4","protocol":"tcp","port":53},{"server":"wan"},{"server":"1.1.1.1"}]}}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var in any
			if strings.HasPrefix(input, "+") {
				in = groupWith(t, input[1:])
			} else {
				in = anyOf(t, input)
			}
			if _, err := NormalizeShareRegions(in); err != nil {
				t.Fatalf("应通过,却报错: %v", err)
			}
		})
	}
}

func TestWanUpstreamIsFixedUDP53(t *testing.T) {
	out, err := NormalizeShareRegions(groupWith(t, `{"dns":{"direct":"wan","directProtocol":"tcp","directPort":8053,
		"proxy":"1.1.1.1","proxyExtras":[{"server":"wan","protocol":"tcp","port":99}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	d := out[0].(map[string]any)["dns"].(map[string]any)
	if d["directProtocol"] != "udp" || d["directPort"] != 53.0 {
		t.Errorf("wan 固定 UDP 53: %v", d)
	}
	if e := d["proxyExtras"].([]any)[0].(map[string]any); e["protocol"] != "udp" || e["port"] != 53.0 {
		t.Errorf("备用里的 wan 也固定 UDP 53: %v", e)
	}
	if d["proxy"] != "1.1.1.1" || d["proxyProtocol"] != "udp" || d["proxyPort"] != 53.0 {
		t.Errorf("普通 IP 上游:协议端口没给就是 udp 53: %v", d)
	}
	// 备用的协议和端口没给 → udp 53;给了就保留
	ex, err := NormalizeShareRegions(groupWith(t, `{"dns":{"directExtras":[{"server":"8.8.8.8"},{"server":"8.8.4.4","protocol":"tcp","port":5353}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	list := ex[0].(map[string]any)["dns"].(map[string]any)["directExtras"].([]any)
	if !reflect.DeepEqual(jsonRoundTrip(t, list), anyOf(t, `[{"server":"8.8.8.8","protocol":"udp","port":53},{"server":"8.8.4.4","protocol":"tcp","port":5353}]`)) {
		t.Errorf("备用上游: %v", list)
	}
}

func TestShareRegionsErrorDoesNotEchoHugeInput(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	_, err := NormalizeShareRegions(groupWith(t, `{"rules":[{"type":"domain","value":"`+huge+`","action":"direct"}]}`))
	if err == nil || len(err.Error()) > 300 {
		t.Fatalf("错误应截断过长的值: %v", err)
	}
}

func TestNormalizeServerInfo(t *testing.T) {
	ok := map[string]string{
		"空对象 = 全用默认":   `{}`,
		"四个字段都空":       `{"name":"","icon":"","iconSvg":"","region":""}`,
		"只有名称":         `{"name":"家里"}`,
		"名称恰好 40 字":    `{"name":"` + strings.Repeat("字", 40) + `"}`,
		"国家图标":         `{"icon":"US"}`,
		"品牌图标":         `{"icon":"brand:openai"}`,
		"svg 开头有空白":    `{"iconSvg":"  <svg xmlns='http://www.w3.org/2000/svg'/>"}`,
		"svg 标签大写":     `{"iconSvg":"<SVG/>"}`,
		"地区":           `{"region":"JP"}`,
		"不认识的键原样保留":    `{"name":"x","future":{"a":1}}`,
		"svg 恰好 64 KB": `{"iconSvg":"<svg>` + strings.Repeat("a", 64<<10-5-6) + `</svg>"}`,
	}
	for name, in := range ok {
		t.Run("通过/"+name, func(t *testing.T) {
			if _, err := NormalizeServerInfo(anyOf(t, in)); err != nil {
				t.Fatalf("应通过: %v", err)
			}
		})
	}
	bad := []struct{ name, in, want string }{
		{"不是对象", `[]`, "must be an object"},
		{"null", `null`, "must be an object"},
		{"名称 41 字", `{"name":"` + strings.Repeat("字", 41) + `"}`, "name is longer than 40"},
		{"名称不是字符串", `{"name":5}`, "name must be a string"},
		{"图标 65 字", `{"icon":"` + strings.Repeat("a", 65) + `"}`, "icon is not a valid icon code"},
		{"图标含控制字符", `{"icon":"a\u0000b"}`, "icon is not a valid icon code"},
		{"svg 不以 <svg 开头", `{"iconSvg":"<div/>"}`, "iconSvg must start with <svg"},
		{"svg 太短", `{"iconSvg":"<sv"}`, "iconSvg must start with <svg"},
		{"svg 超过 64 KB", `{"iconSvg":"<svg>` + strings.Repeat("a", 64<<10) + `"}`, "iconSvg is larger than 64 KB"},
		{"地区小写", `{"region":"jp"}`, "region must be a two-letter uppercase country code"},
		{"地区三位", `{"region":"CHN"}`, "region must be a two-letter uppercase country code"},
		{"地区不是字符串", `{"region":86}`, "region must be a string"},
	}
	for _, c := range bad {
		t.Run("拒绝/"+c.name, func(t *testing.T) {
			_, err := NormalizeServerInfo(anyOf(t, c.in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("应报错含 %q: %v", c.want, err)
			}
		})
	}
	// 规整:去空白、去掉空字符串、保留不认识的键、不改入参
	in := anyOf(t, `{"name":"  家里 ","icon":"","iconSvg":"  <svg/>","region":"JP","future":1}`)
	out, err := NormalizeServerInfo(in)
	if err != nil {
		t.Fatal(err)
	}
	want := anyOf(t, `{"name":"家里","iconSvg":"<svg/>","region":"JP","future":1}`)
	if !reflect.DeepEqual(jsonRoundTrip(t, out), want) {
		t.Errorf("规整结果不对: %v", out)
	}
	if !reflect.DeepEqual(in, anyOf(t, `{"name":"  家里 ","icon":"","iconSvg":"  <svg/>","region":"JP","future":1}`)) {
		t.Error("规整不应改动入参")
	}
}

func TestIsIPOrCIDR(t *testing.T) {
	for _, v := range []string{"1.2.3.4", "1.2.3.4/32", "0.0.0.0/0", "::1", "fd00::/8", "2001:db8::/128", "::ffff:1.2.3.4"} {
		if !isIPOrCIDR(v) {
			t.Errorf("%q 应合法", v)
		}
	}
	for _, v := range []string{"", "/8", "1.2.3/24", "1.2.3.4/", "1.2.3.4/33", "::1/129", "1.2.3.4/-1", "1.2.3.4/ 8", "a.b.c.d", "1.2.3.4%eth0", "1.2.3.4/0008"} {
		if isIPOrCIDR(v) {
			t.Errorf("%q 应不合法", v)
		}
	}
}
