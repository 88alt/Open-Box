package routing

import (
	"reflect"
	"testing"
)

// 自写测试向量:extractHost 只在「字母协议头 + ://」时取 new URL(x).hostname,其余原样返回(含原来的类型)。
// 期望值按 WHATWG URL 的主机写法人工推算。

func TestExtractHost(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		// 带协议头:取主机(小写、去端口 / 账号 / 路径)
		{"https 加路径", "https://1.1.1.1/dns-query", "1.1.1.1"},
		{"主机转小写并去端口", "HTTPS://Example.COM:8443/x?y#z", "example.com"},
		{"账号密码", "https://user:pw@9.9.9.9/dns-query", "9.9.9.9"},
		{"IPv6 主机保留方括号", "tls://[2001:db8::1]:853", "[2001:db8::1]"},
		{"IPv6 压缩写法", "https://[2001:0db8:0000:0000:0000:0000:0000:0001]/", "[2001:db8::1]"},
		{"协议头大小写不限", "HtTpS://a.example", "a.example"},
		{"quic 协议", "quic://dns.example:784", "dns.example"},
		{"非 ASCII 主机转 punycode", "https://例え.jp/x", "xn--r8jz45g.jp"},
		{"默认端口也去掉", "https://a.example:443/", "a.example"},
		// 没有协议头:原样
		{"裸 IPv4", "1.1.1.1", "1.1.1.1"},
		{"裸域名加路径", "example.com/path", "example.com/path"},
		{"裸 IPv6", "2001:db8::1", "2001:db8::1"},
		{"带方括号的裸 IPv6", "[2001:db8::1]", "[2001:db8::1]"},
		{"空串", "", ""},
		// 协议头只认纯字母:带加号、数字、横线的、前面有空白的都不算
		{"协议名带加号", "tcp+tls://1.1.1.1", "tcp+tls://1.1.1.1"},
		{"协议名带数字", "h2://Example.com", "h2://Example.com"},
		{"协议名带横线", "my-proto://Example.com", "my-proto://Example.com"},
		{"没有协议名", "://Example.com", "://Example.com"},
		{"前导空白", " https://Example.com", " https://Example.com"},
		{"只有一个斜杠", "https:/Example.com", "https:/Example.com"},
		{"缺协议头里的冒号", "https//Example.com", "https//Example.com"},
		// 有协议头但 URL 解析失败:原样返回
		{"空主机", "https://", "https://"},
		{"主机里有空格", "https://a b/", "https://a b/"},
		{"端口越界", "https://a.example:99999/", "https://a.example:99999/"},
		// 非字符串:协议头判断拿不到匹配,原样(类型不变)
		{"nil", nil, nil},
		{"数字", 5.0, 5.0},
		{"布尔", true, true},
		// 数组转字符串后像 URL:能解析就取主机,解析失败要返回原来的数组而不是转出来的字符串
		{"单元素数组转串后能解析", []any{"https://1.1.1.1/x"}, "1.1.1.1"},
		{"单元素数组转串后解析失败:原样返回数组", []any{"https://a b/"}, []any{"https://a b/"}},
	}
	for _, c := range cases {
		if got := extractHost(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:extractHost(%#v) = %#v,想要 %#v", c.name, c.in, got, c.want)
		}
	}
}
