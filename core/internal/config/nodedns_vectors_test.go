package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// 自写测试向量:nodedns.go(节点服务器域名的专用 DoH 解析器:保存前校验、存下来的样子、内核 DNS 服务器、
// 按订阅的计划、给出站挂解析器)。期望值按下面的规则人工推算,不取自别的程序的输出:
//   - DoH 地址按 WHATWG URL 规则解析:协议必须是 https(大小写不分,主机转小写、IDN 转 punycode、十进制 / 十六进制写法的
//     IPv4 折成点分写法、IPv6 取规范压缩写法);带用户名 / 密码 / 非空片段的不要;默认端口 443 不单独写;
//   - 校验的先后:不是对象 → url / bootstrap 的类型 → url 为空(bootstrap 不能单独出现)→ 长度(UTF-16 码元,上限 2048)
//     → 地址写法 → bootstrap 必须是 IP → 地址是域名时 bootstrap 必填;
//   - 内核服务器:DoH 一台(type https,server 取不带方括号的主机,端口不是 443 才带 server_port,路径不是
//     /dns-query 才带 path,路径带上查询串),主机是域名时再加一台 udp 的 bootstrap,DoH 那台用 domain_resolver 指过去;
//   - tag 取订阅 id:假值得空串,非字符串先按 JS 的 String() 转,不是 [A-Za-z0-9_-] 的 UTF-16 码元逐个换成 _,最长 40 个码元;
//   - 计划里的订阅 id 用 JS Map 的键比较(SameValueZero):"1" 与 1 不同,null 与缺失(undefined)不同。

func ndJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNodeDNSTagPrefix(t *testing.T) {
	if NodeDNSTagPrefix != "dns-sub-" {
		t.Errorf("NodeDNSTagPrefix=%q", NodeDNSTagPrefix)
	}
}

func TestUTF16Len(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{{"", 0}, {"a", 1}, {"abc", 3}, {"é", 1}, {"中文", 2}, {"😀", 2}, {"a😀", 3}, {"😀😀", 4}, {"\u00a0", 1}} {
		if got := utf16Len(c.in); got != c.want {
			t.Errorf("utf16Len(%q)=%d 要 %d", c.in, got, c.want)
		}
	}
}

func TestSameValueZeroVectors(t *testing.T) {
	for _, c := range []struct {
		name string
		a, b any
		want bool
	}{
		{"字符串相同", "a", "a", true},
		{"字符串不同", "a", "b", false},
		{"空串相同", "", "", true},
		{"大小写不同", "a", "A", false},
		{"字符串与数字", "1", 1.0, false},
		{"数字与字符串", 1.0, "1", false},
		{"数字相同", 1.0, 1.0, true},
		{"数字不同", 1.0, 2.0, false},
		{"零相同", 0.0, 0.0, true},
		{"零与 null", 0.0, nil, false},
		{"布尔相同", true, true, true},
		{"布尔不同", true, false, false},
		{"布尔与数字", true, 1.0, false},
		{"布尔与字符串", false, "", false},
		{"null 与 null", nil, nil, true},
		{"null 与 undefined", nil, jsx.Undefined, false},
		{"undefined 与 null", jsx.Undefined, nil, false},
		{"undefined 与 undefined", jsx.Undefined, jsx.Undefined, true},
		{"undefined 与空串", jsx.Undefined, "", false},
		{"null 与空串", nil, "", false},
		{"null 与 false", nil, false, false},
		{"字符串与 null", "a", nil, false},
		{"空串与 null", "", nil, false},
		{"空串与 undefined", "", jsx.Undefined, false},
		{"空串与 false", "", false, false},
		{"空串与 0", "", 0.0, false},
		{"0 与空串", 0.0, "", false},
		{"0 与 false", 0.0, false, false},
		{"false 与 0", false, 0.0, false},
		{"false 与 null", false, nil, false},
		{"false 与 undefined", false, jsx.Undefined, false},
		{"对象之间不相等", map[string]any{}, map[string]any{}, false},
	} {
		if got := sameValueZero(c.a, c.b); got != c.want {
			t.Errorf("%s:sameValueZero(%#v,%#v)=%v 要 %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestParseDohURLVectors(t *testing.T) {
	for _, c := range []struct {
		in   string
		want dohURL
		ok   bool
	}{
		{"https://dns.example/dns-query", dohURL{"dns.example", 443, "/dns-query"}, true},
		{"https://dns.example", dohURL{"dns.example", 443, "/"}, true},
		{"https://dns.example/", dohURL{"dns.example", 443, "/"}, true},
		{"https://dns.example:443/dns-query", dohURL{"dns.example", 443, "/dns-query"}, true},
		{"https://dns.example:8443/dns-query", dohURL{"dns.example", 8443, "/dns-query"}, true},
		{"https://dns.example/a/b", dohURL{"dns.example", 443, "/a/b"}, true},
		{"https://dns.example/p?a=b", dohURL{"dns.example", 443, "/p?a=b"}, true},
		{"https://dns.example/p?a=b&c=d", dohURL{"dns.example", 443, "/p?a=b&c=d"}, true},
		{"https://dns.example/p?", dohURL{"dns.example", 443, "/p"}, true},
		{"https://dns.example?x=1", dohURL{"dns.example", 443, "/?x=1"}, true},
		{"https://dns.example/a b", dohURL{"dns.example", 443, "/a%20b"}, true},
		{"HTTPS://DNS.Example/Dns-Query", dohURL{"dns.example", 443, "/Dns-Query"}, true},
		{"  https://dns.example/q  ", dohURL{"dns.example", 443, "/q"}, true},
		{"\ufeffhttps://dns.example/q\u3000", dohURL{"dns.example", 443, "/q"}, true},
		{"https://1.1.1.1/dns-query", dohURL{"1.1.1.1", 443, "/dns-query"}, true},
		{"https://[2606:4700:4700::1111]/dns-query", dohURL{"2606:4700:4700::1111", 443, "/dns-query"}, true},
		{"https://[2606:4700:4700:0:0:0:0:1111]:8443/q", dohURL{"2606:4700:4700::1111", 8443, "/q"}, true},
		{"https://[::1]/", dohURL{"::1", 443, "/"}, true},
		{"https://16843009/dns-query", dohURL{"1.1.1.1", 443, "/dns-query"}, true},
		{"https://0x01010101/dns-query", dohURL{"1.1.1.1", 443, "/dns-query"}, true},
		{"https://例え.jp/dns-query", dohURL{"xn--r8jz45g.jp", 443, "/dns-query"}, true},
		{"https://dns.example/#", dohURL{"dns.example", 443, "/"}, true},
		// 不合格
		{"", dohURL{}, false},
		{"   ", dohURL{}, false},
		{"dns.example/dns-query", dohURL{}, false},
		{"http://dns.example/dns-query", dohURL{}, false},
		{"tls://dns.example", dohURL{}, false},
		{"quic://dns.example", dohURL{}, false},
		{"wss://dns.example/dns-query", dohURL{}, false},
		{"https://", dohURL{}, false},
		{"https://user@dns.example/", dohURL{}, false},
		{"https://user:pw@dns.example/", dohURL{}, false},
		{"https://:pw@dns.example/", dohURL{}, false},
		{"https://dns.example/#frag", dohURL{}, false},
		{"https://dns.example/dns-query#x", dohURL{}, false},
		{"https://dns.example:65536/", dohURL{}, false},
		{"https://dns.example:abc/", dohURL{}, false},
		{"https://[::1/", dohURL{}, false},
		{"https://a b/", dohURL{}, false},
	} {
		got, ok := parseDohURL(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseDohURL(%q)=(%+v,%v) 要 (%+v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestValidateNodeDNSVectors(t *testing.T) {
	const (
		notObj    = "nodeDns must be an object"
		urlStr    = "nodeDns.url must be a string"
		bsStr     = "nodeDns.bootstrap must be a string"
		needURL   = "nodeDns.url is required when bootstrap is set"
		tooLong   = "nodeDns.url is too long"
		badURL    = "nodeDns.url must be an https:// DoH address"
		badBs     = "nodeDns.bootstrap must be an IP address"
		needBs    = "nodeDns.bootstrap is required when the DoH address is a domain"
		domainURL = "https://dns.example/dns-query"
		ipURL     = "https://1.1.1.1/dns-query"
	)
	for _, c := range []struct{ in, want string }{
		// 不用专用解析器 / 形状不对
		{`null`, ""},
		{`{}`, ""},
		{`{"other":1}`, ""},
		{`"x"`, notObj},
		{`""`, notObj},
		{`5`, notObj},
		{`0`, notObj},
		{`true`, notObj},
		{`false`, notObj},
		{`[]`, notObj},
		{`["https://1.1.1.1/"]`, notObj},
		// url / bootstrap 的类型:键在就要是字符串,null 也不行
		{`{"url":null}`, urlStr},
		{`{"url":5}`, urlStr},
		{`{"url":true}`, urlStr},
		{`{"url":[]}`, urlStr},
		{`{"url":{}}`, urlStr},
		{`{"bootstrap":null}`, bsStr},
		{`{"bootstrap":5}`, bsStr},
		{`{"bootstrap":false}`, bsStr},
		{`{"bootstrap":["1.1.1.1"]}`, bsStr},
		{`{"url":5,"bootstrap":5}`, urlStr},
		{`{"url":"","bootstrap":null}`, bsStr},
		{`{"url":"` + ipURL + `","bootstrap":1}`, bsStr},
		// url 为空(空串 / 全是空白)
		{`{"url":""}`, ""},
		{`{"url":"   "}`, ""},
		{`{"url":"\u3000\ufeff\u00a0"}`, ""},
		{`{"url":"","bootstrap":""}`, ""},
		{`{"bootstrap":""}`, ""},
		{`{"bootstrap":"   "}`, ""},
		{`{"url":" ","bootstrap":" "}`, ""},
		// bootstrap 不能单独出现
		{`{"bootstrap":"1.1.1.1"}`, needURL},
		{`{"url":"","bootstrap":"1.1.1.1"}`, needURL},
		{`{"url":"  ","bootstrap":" 1.1.1.1 "}`, needURL},
		{`{"bootstrap":"not an ip"}`, needURL},
		// 地址写法
		{`{"url":"` + ipURL + `"}`, ""},
		{`{"url":"https://1.1.1.1"}`, ""},
		{`{"url":"https://1.1.1.1:8443/dns-query"}`, ""},
		{`{"url":"https://1.1.1.1:443/dns-query"}`, ""},
		{`{"url":"HTTPS://1.1.1.1/dns-query"}`, ""},
		{`{"url":"  https://1.1.1.1/dns-query  "}`, ""},
		{`{"url":"https://1.1.1.1/dns-query?x=1"}`, ""},
		{`{"url":"https://1.1.1.1/#"}`, ""},
		{`{"url":"https://[2606:4700:4700::1111]/dns-query"}`, ""},
		{`{"url":"https://[::1]"}`, ""},
		{`{"url":"https://16843009/dns-query"}`, ""},
		{`{"url":"https://0x01010101/"}`, ""},
		{`{"url":"http://1.1.1.1/dns-query"}`, badURL},
		{`{"url":"tls://1.1.1.1"}`, badURL},
		{`{"url":"1.1.1.1"}`, badURL},
		{`{"url":"1.1.1.1/dns-query"}`, badURL},
		{`{"url":"dns.example"}`, badURL},
		{`{"url":"https://"}`, badURL},
		{`{"url":"https://user@1.1.1.1/"}`, badURL},
		{`{"url":"https://user:pw@1.1.1.1/"}`, badURL},
		{`{"url":"https://1.1.1.1/#frag"}`, badURL},
		{`{"url":"https://1.1.1.1:65536/"}`, badURL},
		{`{"url":"https://1.1.1.1:abc/"}`, badURL},
		{`{"url":"https://[::1/"}`, badURL},
		{`{"url":"https://256.1.1.1.1/"}`, badURL},
		{`{"url":"https://a b/"}`, badURL},
		// 地址写法错优先于 bootstrap 的问题
		{`{"url":"http://1.1.1.1/","bootstrap":"bad"}`, badURL},
		// bootstrap 要是 IP:有 bootstrap 就查,地址是 IP 也查
		{`{"url":"` + domainURL + `","bootstrap":"1.1.1.1"}`, ""},
		{`{"url":"` + domainURL + `","bootstrap":" 1.1.1.1 "}`, ""},
		{`{"url":"` + domainURL + `","bootstrap":"2606:4700:4700::1111"}`, ""},
		{`{"url":"` + ipURL + `","bootstrap":"8.8.8.8"}`, ""},
		{`{"url":"` + domainURL + `","bootstrap":"dns.example"}`, badBs},
		{`{"url":"` + domainURL + `","bootstrap":"1.1.1.1:53"}`, badBs},
		{`{"url":"` + domainURL + `","bootstrap":"[::1]"}`, badBs},
		{`{"url":"` + domainURL + `","bootstrap":"256.1.1.1"}`, badBs},
		{`{"url":"` + domainURL + `","bootstrap":"1.1.1"}`, badBs},
		{`{"url":"` + domainURL + `","bootstrap":"1.1.1.1/32"}`, badBs},
		{`{"url":"` + ipURL + `","bootstrap":"bad"}`, badBs},
		// 地址是域名:bootstrap 必填
		{`{"url":"` + domainURL + `"}`, needBs},
		{`{"url":"` + domainURL + `","bootstrap":""}`, needBs},
		{`{"url":"` + domainURL + `","bootstrap":"  "}`, needBs},
		{`{"url":"https://dns.example"}`, needBs},
		{`{"url":"https://1.1.1.1.example/"}`, needBs},
		{`{"url":"https://例え.jp/"}`, needBs},
		{`{"url":"https://xn--r8jz45g.jp/"}`, needBs},
		// 多余的键不管
		{`{"url":"` + ipURL + `","extra":[1,2],"x":null}`, ""},
	} {
		if got := ValidateNodeDNS(vecJSON(t, c.in)); got != c.want {
			t.Errorf("ValidateNodeDNS(%s)=%q 要 %q", c.in, got, c.want)
		}
	}
	if got := ValidateNodeDNS(jsx.Undefined); got != "" {
		t.Errorf("undefined 应合法,得到 %q", got)
	}
}

func TestValidateNodeDNSLength(t *testing.T) {
	const tooLong = "nodeDns.url is too long"
	ip := "https://1.1.1.1/" // 16 个码元
	mk := func(url string) any { return map[string]any{"url": url} }
	for _, c := range []struct {
		name string
		url  string
		want string
	}{
		{"恰好 2048", ip + strings.Repeat("a", 2048-16), ""},
		{"2049", ip + strings.Repeat("a", 2049-16), tooLong},
		{"2047", ip + strings.Repeat("a", 2047-16), ""},
		{"表情占两个码元,2048 个码元", ip + strings.Repeat("😀", 1016), ""},
		{"表情,2050 个码元", ip + strings.Repeat("😀", 1017), tooLong},
		{"表情加一个字母,2049 个码元", ip + strings.Repeat("😀", 1016) + "a", tooLong},
		{"汉字一个码元,2048 个码元", ip + strings.Repeat("中", 2048-16), ""},
		{"汉字,2049 个码元", ip + strings.Repeat("中", 2049-16), tooLong},
		{"首尾空白不算长度", strings.Repeat(" ", 100) + ip + strings.Repeat("a", 2048-16) + strings.Repeat("\u3000", 100), ""},
		{"长度检查先于写法检查", strings.Repeat("x", 3000), tooLong},
		{"长度检查先于 bootstrap 检查", "https://dns.example/" + strings.Repeat("a", 2100), tooLong},
	} {
		if got := ValidateNodeDNS(mk(c.url)); got != c.want {
			t.Errorf("%s:ValidateNodeDNS=%q 要 %q", c.name, got, c.want)
		}
	}
}

func TestNormalizeNodeDNSVectors(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string // JSON;nil 写 null
	}{
		// 没配 / 形状不对 / 不合法 → nil
		{`null`, `null`},
		{`{}`, `null`},
		{`"https://1.1.1.1/"`, `null`},
		{`5`, `null`},
		{`true`, `null`},
		{`[]`, `null`},
		{`{"url":""}`, `null`},
		{`{"url":"  "}`, `null`},
		{`{"url":null}`, `null`},
		{`{"url":5}`, `null`},
		{`{"bootstrap":"1.1.1.1"}`, `null`},
		{`{"url":"","bootstrap":"1.1.1.1"}`, `null`},
		{`{"url":"http://1.1.1.1/"}`, `null`},
		{`{"url":"https://dns.example/dns-query"}`, `null`},
		{`{"url":"https://dns.example/dns-query","bootstrap":"bad"}`, `null`},
		{`{"url":"https://1.1.1.1/","bootstrap":5}`, `null`},
		{`{"url":"https://1.1.1.1/#x"}`, `null`},
		// 合法:地址原样保留(只去首尾空白),不做规范化
		{`{"url":"https://1.1.1.1/dns-query"}`, `{"url":"https://1.1.1.1/dns-query"}`},
		{`{"url":"  https://1.1.1.1/dns-query\u3000"}`, `{"url":"https://1.1.1.1/dns-query"}`},
		{`{"url":"HTTPS://1.1.1.1/dns-query"}`, `{"url":"HTTPS://1.1.1.1/dns-query"}`},
		{`{"url":"https://16843009/"}`, `{"url":"https://16843009/"}`},
		{`{"url":"https://dns.example/dns-query","bootstrap":"1.1.1.1"}`, `{"url":"https://dns.example/dns-query","bootstrap":"1.1.1.1"}`},
		{`{"url":" https://dns.example/q ","bootstrap":" 1.1.1.1 "}`, `{"url":"https://dns.example/q","bootstrap":"1.1.1.1"}`},
		{`{"url":"https://1.1.1.1/","bootstrap":"8.8.8.8"}`, `{"url":"https://1.1.1.1/","bootstrap":"8.8.8.8"}`},
		{`{"url":"https://1.1.1.1/","bootstrap":""}`, `{"url":"https://1.1.1.1/"}`},
		{`{"url":"https://1.1.1.1/","bootstrap":"  "}`, `{"url":"https://1.1.1.1/"}`},
		// 多余的键丢掉
		{`{"url":"https://1.1.1.1/","extra":1,"name":"x"}`, `{"url":"https://1.1.1.1/"}`},
	} {
		conf := NormalizeNodeDNS(vecJSON(t, c.in))
		var got string
		if conf == nil {
			got = "null"
		} else {
			m := map[string]any{"url": conf.URL}
			if conf.Bootstrap != "" {
				m["bootstrap"] = conf.Bootstrap
			}
			// 键序固定:url 在前
			got = `{"url":` + ndJSON(t, conf.URL)
			if conf.Bootstrap != "" {
				got += `,"bootstrap":` + ndJSON(t, conf.Bootstrap)
			}
			got += `}`
			_ = m
		}
		if got != c.want {
			t.Errorf("NormalizeNodeDNS(%s)=%s 要 %s", c.in, got, c.want)
		}
	}
	if NormalizeNodeDNS(jsx.Undefined) != nil {
		t.Error("undefined 应得 nil")
	}
}

func TestSafeIDVectors(t *testing.T) {
	a39 := strings.Repeat("a", 39)
	a40 := strings.Repeat("a", 40)
	for _, c := range []struct {
		name string
		in   any
		want string
	}{
		{"字母数字", "abc123", "abc123"},
		{"大小写保留", "AbC", "AbC"},
		{"横线与下划线保留", "a-b_c", "a-b_c"},
		{"点、空格、斜杠", "a.b c/d", "a_b_c_d"},
		{"冒号与加号", "a:b+c", "a_b_c"},
		{"全是特殊字符", "...", "___"},
		{"中文逐个码元", "订阅1", "__1"},
		{"表情是两个码元", "😀", "__"},
		{"带重音的字母", "é", "_"},
		{"全角字母", "Ａ", "_"},
		{"制表符与换行", "a\tb\nc", "a_b_c"},
		{"40 个原样", a40, a40},
		{"41 个截成 40", a40 + "b", a40},
		{"100 个截成 40", strings.Repeat("x", 100), strings.Repeat("x", 40)},
		{"39 个加表情:在码元 40 处截断", a39 + "😀", a39 + "_"},
		{"40 个加表情", a40 + "😀", a40},
		{"特殊字符也占长度", strings.Repeat(".", 50), strings.Repeat("_", 40)},
		// 假值得空串
		{"nil", nil, ""},
		{"undefined", jsx.Undefined, ""},
		{"空串", "", ""},
		{"false", false, ""},
		{"0", 0.0, ""},
		// 非字符串先转字符串
		{"数字 12", 12.0, "12"},
		{"小数", 1.5, "1_5"},
		{"负数", -3.0, "-3"},
		{"大数", 100000000.0, "100000000"},
		{"指数写法", 1e21, "1e_21"},
		{"true", true, "true"},
		{"数组", []any{"x", "y"}, "x_y"},
		{"数组含 null", []any{"x", nil, "y"}, "x__y"},
		{"空数组是真值,转出空串", []any{}, ""},
		{"对象", map[string]any{}, "_object_Object_"},
		{"空白串是真值", " ", "_"},
	} {
		if got := safeID(c.in); got != c.want {
			t.Errorf("%s:safeID(%#v)=%q 要 %q", c.name, c.in, got, c.want)
		}
	}
}

func TestNodeDNSServersVectors(t *testing.T) {
	for _, c := range []struct {
		name    string
		conf    string
		id      any
		wantTag string
		want    string // 服务器列表的 JSON(Go 序列化按键名字母序);"" 表示 ok=false
	}{
		{"没配", `null`, "s1", "", ""},
		{"不合法", `{"url":"http://1.1.1.1/"}`, "s1", "", ""},
		{"域名缺 bootstrap", `{"url":"https://dns.example/dns-query"}`, "s1", "", ""},
		{"IP 默认端口默认路径", `{"url":"https://1.1.1.1/dns-query"}`, "s1", "dns-sub-s1",
			`[{"server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"IP 不带路径:路径是 / 要写", `{"url":"https://1.1.1.1"}`, "s1", "dns-sub-s1",
			`[{"path":"/","server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"IP 非 443 端口", `{"url":"https://1.1.1.1:8443/dns-query"}`, "s1", "dns-sub-s1",
			`[{"server":"1.1.1.1","server_port":8443,"tag":"dns-sub-s1","type":"https"}]`},
		{"显式 443 不写端口", `{"url":"https://1.1.1.1:443/dns-query"}`, "s1", "dns-sub-s1",
			`[{"server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"自定义路径", `{"url":"https://1.1.1.1/abc"}`, "s1", "dns-sub-s1",
			`[{"path":"/abc","server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"路径带查询串", `{"url":"https://1.1.1.1/dns-query?k=v"}`, "s1", "dns-sub-s1",
			`[{"path":"/dns-query?k=v","server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"路径大小写敏感", `{"url":"https://1.1.1.1/DNS-QUERY"}`, "s1", "dns-sub-s1",
			`[{"path":"/DNS-QUERY","server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"路径带尾斜杠也要写", `{"url":"https://1.1.1.1/dns-query/"}`, "s1", "dns-sub-s1",
			`[{"path":"/dns-query/","server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"空查询串不影响路径", `{"url":"https://1.1.1.1/dns-query?"}`, "s1", "dns-sub-s1",
			`[{"server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"路径含空格被编码", `{"url":"https://1.1.1.1/a b"}`, "s1", "dns-sub-s1",
			`[{"path":"/a%20b","server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"端口路径一起", `{"url":"https://1.1.1.1:8443/q?a=1"}`, "s1", "dns-sub-s1",
			`[{"path":"/q?a=1","server":"1.1.1.1","server_port":8443,"tag":"dns-sub-s1","type":"https"}]`},
		{"IP 时 bootstrap 不生成服务器", `{"url":"https://1.1.1.1/dns-query","bootstrap":"8.8.8.8"}`, "s1", "dns-sub-s1",
			`[{"server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"IPv6 去方括号", `{"url":"https://[2606:4700:4700::1111]/dns-query"}`, "s1", "dns-sub-s1",
			`[{"server":"2606:4700:4700::1111","tag":"dns-sub-s1","type":"https"}]`},
		{"IPv6 写全了折成压缩写法", `{"url":"https://[2606:4700:4700:0:0:0:0:1111]:853/dns-query"}`, "s1", "dns-sub-s1",
			`[{"server":"2606:4700:4700::1111","server_port":853,"tag":"dns-sub-s1","type":"https"}]`},
		{"十进制 IPv4 折成点分", `{"url":"https://16843009/dns-query"}`, "s1", "dns-sub-s1",
			`[{"server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}]`},
		{"域名:两台,DoH 在前", `{"url":"https://dns.example/dns-query","bootstrap":"1.1.1.1"}`, "s1", "dns-sub-s1",
			`[{"domain_resolver":"dns-sub-s1-bootstrap","server":"dns.example","tag":"dns-sub-s1","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-s1-bootstrap","type":"udp"}]`},
		{"域名转小写", `{"url":"https://DNS.Example/dns-query","bootstrap":"1.1.1.1"}`, "s1", "dns-sub-s1",
			`[{"domain_resolver":"dns-sub-s1-bootstrap","server":"dns.example","tag":"dns-sub-s1","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-s1-bootstrap","type":"udp"}]`},
		{"域名加端口路径", `{"url":"https://dns.example:8443/q","bootstrap":" 8.8.8.8 "}`, "s1", "dns-sub-s1",
			`[{"domain_resolver":"dns-sub-s1-bootstrap","path":"/q","server":"dns.example","server_port":8443,"tag":"dns-sub-s1","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-s1-bootstrap","type":"udp"}]`},
		{"IDN 域名用 punycode", `{"url":"https://例え.jp/dns-query","bootstrap":"1.1.1.1"}`, "s1", "dns-sub-s1",
			`[{"domain_resolver":"dns-sub-s1-bootstrap","server":"xn--r8jz45g.jp","tag":"dns-sub-s1","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-s1-bootstrap","type":"udp"}]`},
		{"bootstrap 是 IPv6", `{"url":"https://dns.example/dns-query","bootstrap":"2606:4700:4700::1111"}`, "s1", "dns-sub-s1",
			`[{"domain_resolver":"dns-sub-s1-bootstrap","server":"dns.example","tag":"dns-sub-s1","type":"https"},{"server":"2606:4700:4700::1111","tag":"dns-sub-s1-bootstrap","type":"udp"}]`},
		// tag 取订阅 id
		{"id 含特殊字符", `{"url":"https://1.1.1.1/dns-query"}`, "a.b c/d", "dns-sub-a_b_c_d",
			`[{"server":"1.1.1.1","tag":"dns-sub-a_b_c_d","type":"https"}]`},
		{"id 是数字", `{"url":"https://1.1.1.1/dns-query"}`, 12.0, "dns-sub-12",
			`[{"server":"1.1.1.1","tag":"dns-sub-12","type":"https"}]`},
		{"id 缺失得空后缀", `{"url":"https://1.1.1.1/dns-query"}`, jsx.Undefined, "dns-sub-",
			`[{"server":"1.1.1.1","tag":"dns-sub-","type":"https"}]`},
		{"id 是 null", `{"url":"https://1.1.1.1/dns-query"}`, nil, "dns-sub-",
			`[{"server":"1.1.1.1","tag":"dns-sub-","type":"https"}]`},
		{"id 太长截到 40", `{"url":"https://dns.example/dns-query","bootstrap":"1.1.1.1"}`, strings.Repeat("k", 60), "dns-sub-" + strings.Repeat("k", 40),
			`[{"domain_resolver":"dns-sub-` + strings.Repeat("k", 40) + `-bootstrap","server":"dns.example","tag":"dns-sub-` + strings.Repeat("k", 40) + `","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-` + strings.Repeat("k", 40) + `-bootstrap","type":"udp"}]`},
	} {
		tag, servers, ok := NodeDNSServers(vecJSON(t, c.conf), c.id)
		if c.want == "" {
			if ok || tag != "" || servers != nil {
				t.Errorf("%s:应 ok=false,得到 (%q,%v,%v)", c.name, tag, servers, ok)
			}
			continue
		}
		if !ok {
			t.Errorf("%s:不应 ok=false", c.name)
			continue
		}
		if tag != c.wantTag {
			t.Errorf("%s:tag=%q 要 %q", c.name, tag, c.wantTag)
		}
		if got := ndJSON(t, servers); got != c.want {
			t.Errorf("%s:\n  得到 %s\n  要   %s", c.name, got, c.want)
		}
	}
}

func TestNodeDNSServersDoesNotMutate(t *testing.T) {
	src := `{"url":"https://dns.example/dns-query","bootstrap":"1.1.1.1","extra":[1]}`
	in := vecJSON(t, src)
	NodeDNSServers(in, "s1")
	if !reflect.DeepEqual(in, vecJSON(t, src)) {
		t.Errorf("入参被改了:%v", in)
	}
}

func TestPlanNodeDNSVectors(t *testing.T) {
	ipSub := func(id string, url string) string {
		return `{"id":"` + id + `","nodeDns":{"url":"` + url + `"}}`
	}
	for _, c := range []struct {
		name        string
		subs        string
		wantServers string
		wantEntries string
	}{
		{"null", `null`, `[]`, `[]`},
		{"对象不是数组", `{"id":"a","nodeDns":{"url":"https://1.1.1.1/"}}`, `[]`, `[]`},
		{"字符串", `"x"`, `[]`, `[]`},
		{"空数组", `[]`, `[]`, `[]`},
		{"假值条目与非对象条目跳过", `[null,false,0,"",5,"x",[],true]`, `[]`, `[]`},
		{"没有 nodeDns", `[{"id":"a"},{"id":"b","nodeDns":null},{"id":"c","nodeDns":{}},{"id":"d","nodeDns":{"url":""}}]`, `[]`, `[]`},
		{"nodeDns 不合法跳过", `[{"id":"a","nodeDns":{"url":"http://1.1.1.1/"}},{"id":"b","nodeDns":{"url":"https://dns.example/"}}]`, `[]`, `[]`},
		{"单个", `[` + ipSub("a", "https://1.1.1.1/dns-query") + `]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-a","type":"https"}]`, `[["a","dns-sub-a"]]`},
		{"按顺序", `[` + ipSub("b", "https://1.1.1.1/dns-query") + `,` + ipSub("a", "https://8.8.8.8/dns-query") + `]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-b","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-a","type":"https"}]`,
			`[["b","dns-sub-b"],["a","dns-sub-a"]]`},
		{"域名订阅带出两台", `[{"id":"x","nodeDns":{"url":"https://dns.example/q","bootstrap":"1.1.1.1"}},` + ipSub("y", "https://9.9.9.9/dns-query") + `]`,
			`[{"domain_resolver":"dns-sub-x-bootstrap","path":"/q","server":"dns.example","tag":"dns-sub-x","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-x-bootstrap","type":"udp"},{"server":"9.9.9.9","tag":"dns-sub-y","type":"https"}]`,
			`[["x","dns-sub-x"],["y","dns-sub-y"]]`},
		// enabled:只有布尔 false 才停用
		{"enabled false 跳过", `[{"id":"a","enabled":false,"nodeDns":{"url":"https://1.1.1.1/dns-query"}}]`, `[]`, `[]`},
		{"enabled true 保留", `[{"id":"a","enabled":true,"nodeDns":{"url":"https://1.1.1.1/dns-query"}}]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-a","type":"https"}]`, `[["a","dns-sub-a"]]`},
		{"enabled 为 null / 0 / 空串 / 字符串 false 都保留", `[{"id":"a","enabled":null,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},{"id":"b","enabled":0,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},{"id":"c","enabled":"","nodeDns":{"url":"https://1.1.1.1/dns-query"}},{"id":"d","enabled":"false","nodeDns":{"url":"https://1.1.1.1/dns-query"}}]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-a","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-b","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-c","type":"https"},{"server":"1.1.1.1","tag":"dns-sub-d","type":"https"}]`,
			`[["a","dns-sub-a"],["b","dns-sub-b"],["c","dns-sub-c"],["d","dns-sub-d"]]`},
		{"停用的夹在中间", `[` + ipSub("a", "https://1.1.1.1/dns-query") + `,{"id":"b","enabled":false,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},` + ipSub("c", "https://8.8.8.8/dns-query") + `]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-a","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-c","type":"https"}]`,
			`[["a","dns-sub-a"],["c","dns-sub-c"]]`},
		// 订阅 id:Map 的键比较
		{"同 id 出现两次:服务器各加一遍,表里只占一个位置", `[` + ipSub("a", "https://1.1.1.1/dns-query") + `,` + ipSub("b", "https://8.8.8.8/dns-query") + `,` + ipSub("a", "https://9.9.9.9/dns-query") + `]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-a","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-b","type":"https"},{"server":"9.9.9.9","tag":"dns-sub-a","type":"https"}]`,
			`[["a","dns-sub-a"],["b","dns-sub-b"]]`},
		{"数字 1 与字符串 1 是两个键", `[{"id":1,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},{"id":"1","nodeDns":{"url":"https://8.8.8.8/dns-query"}}]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-1","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-1","type":"https"}]`,
			`[[1,"dns-sub-1"],["1","dns-sub-1"]]`},
		{"id 不同但 tag 相同:各占一位", `[` + ipSub("a.b", "https://1.1.1.1/dns-query") + `,` + ipSub("a_b", "https://8.8.8.8/dns-query") + `]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-a_b","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-a_b","type":"https"}]`,
			`[["a.b","dns-sub-a_b"],["a_b","dns-sub-a_b"]]`},
		{"id 为 null 与缺失是两个键,序列化都是 null", `[{"id":null,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},{"nodeDns":{"url":"https://8.8.8.8/dns-query"}}]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-","type":"https"}]`,
			`[[null,"dns-sub-"],[null,"dns-sub-"]]`},
		{"两个缺失 id 合成一个键", `[{"nodeDns":{"url":"https://1.1.1.1/dns-query"}},{"nodeDns":{"url":"https://8.8.8.8/dns-query"}}]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-","type":"https"}]`,
			`[[null,"dns-sub-"]]`},
		{"布尔 id", `[{"id":true,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},{"id":false,"nodeDns":{"url":"https://8.8.8.8/dns-query"}}]`,
			`[{"server":"1.1.1.1","tag":"dns-sub-true","type":"https"},{"server":"8.8.8.8","tag":"dns-sub-","type":"https"}]`,
			`[[true,"dns-sub-true"],[false,"dns-sub-"]]`},
	} {
		p := PlanNodeDNS(vecJSON(t, c.subs))
		if p.Servers == nil {
			t.Errorf("%s:Servers 不应是 nil", c.name)
		}
		if got := ndJSON(t, p.Servers); got != c.wantServers {
			t.Errorf("%s:Servers\n  得到 %s\n  要   %s", c.name, got, c.wantServers)
		}
		if got := ndJSON(t, p.Entries()); got != c.wantEntries {
			t.Errorf("%s:Entries\n  得到 %s\n  要   %s", c.name, got, c.wantEntries)
		}
	}
}

func TestNodeDNSPlanLookup(t *testing.T) {
	p := PlanNodeDNS(vecJSON(t, `[
		{"id":"a","nodeDns":{"url":"https://1.1.1.1/dns-query"}},
		{"id":1,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},
		{"id":null,"nodeDns":{"url":"https://1.1.1.1/dns-query"}},
		{"nodeDns":{"url":"https://1.1.1.1/dns-query"}},
		{"id":"b","enabled":false,"nodeDns":{"url":"https://1.1.1.1/dns-query"}}
	]`))
	for _, c := range []struct {
		name string
		id   any
		want string
	}{
		{"字符串 id", "a", "dns-sub-a"},
		{"数字 id", 1.0, "dns-sub-1"},
		{"字符串 1 查不到数字 1 的位置之外的", "1", ""},
		{"null id", nil, "dns-sub-"},
		{"缺失 id", jsx.Undefined, "dns-sub-"},
		{"停用订阅的 id 查不到", "b", ""},
		{"没有的 id", "zzz", ""},
		{"大小写敏感", "A", ""},
		{"空串", "", ""},
		{"布尔", true, ""},
	} {
		if got := p.Lookup(c.id); got != c.want {
			t.Errorf("%s:Lookup(%#v)=%q 要 %q", c.name, c.id, got, c.want)
		}
	}
	empty := PlanNodeDNS(nil)
	if got := empty.Lookup("a"); got != "" {
		t.Errorf("空计划 Lookup=%q", got)
	}
	if es := empty.Entries(); es == nil || len(es) != 0 || ndJSON(t, es) != "[]" {
		t.Errorf("空计划 Entries=%v", es)
	}
}

func TestPlanNodeDNSDoesNotMutate(t *testing.T) {
	src := `[{"id":"a","nodeDns":{"url":"https://dns.example/q","bootstrap":"1.1.1.1"}},{"id":"b","enabled":false}]`
	in := vecJSON(t, src)
	PlanNodeDNS(in)
	if !reflect.DeepEqual(in, vecJSON(t, src)) {
		t.Errorf("入参被改了:%v", in)
	}
}

func TestWithNodeResolverVectors(t *testing.T) {
	// 不需要加解析器:原样返回同一个对象
	for _, c := range []struct {
		name string
		ob   string
		tag  string
	}{
		{"tag 为空", `{"server":"node.example"}`, ""},
		{"没有 server", `{"type":"direct"}`, "dns-sub-a"},
		{"server 为空串", `{"server":""}`, "dns-sub-a"},
		{"server 不是字符串", `{"server":5}`, "dns-sub-a"},
		{"server 是 null", `{"server":null}`, "dns-sub-a"},
		{"server 是数组", `{"server":["node.example"]}`, "dns-sub-a"},
		{"IPv4", `{"server":"1.2.3.4"}`, "dns-sub-a"},
		{"IPv6", `{"server":"2001:db8::1"}`, "dns-sub-a"},
		{"IPv6 回环", `{"server":"::1"}`, "dns-sub-a"},
	} {
		ob := vecJSON(t, c.ob).(map[string]any)
		got := WithNodeResolver(ob, c.tag)
		if reflect.ValueOf(got).Pointer() != reflect.ValueOf(ob).Pointer() {
			t.Errorf("%s:应原样返回同一个对象,得到 %v", c.name, got)
		}
		if _, has := got["domain_resolver"]; has {
			t.Errorf("%s:不应带 domain_resolver", c.name)
		}
	}
	if got := WithNodeResolver(nil, "dns-sub-a"); got != nil {
		t.Errorf("nil 出站应得 nil,得到 %v", got)
	}

	// 需要加:返回拷贝,原对象不动,其余字段原样带上
	for _, c := range []struct {
		name string
		ob   string
		tag  string
		want string
	}{
		{"域名", `{"type":"vless","server":"node.example","server_port":443}`, "dns-sub-a",
			`{"domain_resolver":"dns-sub-a","server":"node.example","server_port":443,"type":"vless"}`},
		{"覆盖已有的解析器", `{"server":"node.example","domain_resolver":"old"}`, "dns-sub-a",
			`{"domain_resolver":"dns-sub-a","server":"node.example"}`},
		{"嵌套字段原样带上", `{"server":"node.example","tls":{"enabled":true,"alpn":["h2"]},"tag":"n1"}`, "t",
			`{"domain_resolver":"t","server":"node.example","tag":"n1","tls":{"alpn":["h2"],"enabled":true}}`},
		{"形似 IP 的域名", `{"server":"1.2.3.4.example"}`, "t", `{"domain_resolver":"t","server":"1.2.3.4.example"}`},
		{"带空白的 IP 不算 IP", `{"server":" 1.2.3.4"}`, "t", `{"domain_resolver":"t","server":" 1.2.3.4"}`},
		{"带方括号的 IPv6 不算 IP", `{"server":"[::1]"}`, "t", `{"domain_resolver":"t","server":"[::1]"}`},
		{"超出范围的点分数字不算 IP", `{"server":"256.1.1.1"}`, "t", `{"domain_resolver":"t","server":"256.1.1.1"}`},
		{"带 IDN 的域名", `{"server":"例え.jp"}`, "t", `{"domain_resolver":"t","server":"例え.jp"}`},
	} {
		ob := vecJSON(t, c.ob).(map[string]any)
		before := ndJSON(t, ob)
		got := WithNodeResolver(ob, c.tag)
		if g := ndJSON(t, got); g != c.want {
			t.Errorf("%s:\n  得到 %s\n  要   %s", c.name, g, c.want)
		}
		if reflect.ValueOf(got).Pointer() == reflect.ValueOf(ob).Pointer() {
			t.Errorf("%s:应返回拷贝", c.name)
		}
		if after := ndJSON(t, ob); after != before {
			t.Errorf("%s:原对象被改了:%s → %s", c.name, before, after)
		}
	}
}
