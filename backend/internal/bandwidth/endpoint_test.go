package bandwidth

import (
	"strings"
	"testing"
)

// 上游 v0.1.493 的测速站点预设:档案 bandwidth.endpoint 取 cloudflare / apple / cachefly / custom,
// custom 时用 endpointCustomUrl。不同站点的带宽样本彼此隔离(样本隔离在存储层,这里只管地址选择与校验)。
func TestNormalizeSettingsEndpoint(t *testing.T) {
	// 不写 endpoint:按默认 Cloudflare,且是合法的
	s, err := NormalizeSettings(map[string]any{})
	if err != nil {
		t.Fatalf("空档案不该报错: %v", err)
	}
	if s.Endpoint != EndpointCloudflare {
		t.Fatalf("默认站点应为 cloudflare,拿到 %q", s.Endpoint)
	}

	// 四个预设都能存下
	for _, e := range []string{EndpointCloudflare, EndpointApple, EndpointCachefly} {
		s, err := NormalizeSettings(map[string]any{"endpoint": e})
		if err != nil || s.Endpoint != e {
			t.Fatalf("%s 应能通过: %+v %v", e, s, err)
		}
	}

	// custom 必须带合法地址
	s, err = NormalizeSettings(map[string]any{"endpoint": "custom", "endpointCustomUrl": "https://speed.example.com/100mb.bin"})
	if err != nil {
		t.Fatalf("custom + 合法地址应通过: %v", err)
	}
	if s.Endpoint != EndpointCustom || s.EndpointCustomURL != "https://speed.example.com/100mb.bin" {
		t.Fatalf("%+v", s)
	}

	// 认不出的站点名要拒绝
	if _, err := NormalizeSettings(map[string]any{"endpoint": "fastly"}); err == nil {
		t.Error("未知站点应报错")
	}
	// endpoint 不是字符串要拒绝
	if _, err := NormalizeSettings(map[string]any{"endpoint": 1}); err == nil {
		t.Error("endpoint 非字符串应报错")
	}
}

func TestNormalizeSettingsCustomURLErrors(t *testing.T) {
	cases := []struct{ name, url string }{
		{"空地址", ""},
		{"http 不是 https", "http://speed.example.com/x.bin"},
		{"没有主机", "https:///x.bin"},
		{"localhost", "https://localhost/x.bin"},
		{"127.0.0.1", "https://127.0.0.1/x.bin"},
		{"IPv6 回环", "https://[::1]/x.bin"},
		{"内网 10/8", "https://10.1.2.3/x.bin"},
		{"内网 192.168/16", "https://192.168.1.5/x.bin"},
		{"内网 172.16/12", "https://172.16.0.9/x.bin"},
		{"链路本地", "https://169.254.1.1/x.bin"},
		{".local 后缀", "https://router.local/x.bin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NormalizeSettings(map[string]any{"endpoint": "custom", "endpointCustomUrl": c.url})
			if err == nil {
				t.Fatalf("%q 应被拒绝", c.url)
			}
		})
	}
}

// TestEndpointURL 按站点给下载地址:内置站点取预设,custom 取用户填的,认不出 / 空按默认。
func TestEndpointURL(t *testing.T) {
	cases := []struct {
		name    string
		in      Settings
		wantSub string
	}{
		{"默认(空)", Settings{}, ""},
		{"cloudflare", Settings{Endpoint: EndpointCloudflare}, "speed.cloudflare.com"},
		{"apple", Settings{Endpoint: EndpointApple}, "speed.apple.com"},
		{"cachefly", Settings{Endpoint: EndpointCachefly}, "cachefly"},
		{"custom", Settings{Endpoint: EndpointCustom, EndpointCustomURL: "https://speed.example.com/a.bin"}, "speed.example.com"},
		{"custom 但地址空", Settings{Endpoint: EndpointCustom}, "speed.cloudflare.com"},
		{"认不出的站点", Settings{Endpoint: "fastly"}, "speed.cloudflare.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.TestEndpointURL()
			if got == "" {
				t.Fatal("不该返回空地址")
			}
			if c.wantSub != "" && !strings.Contains(got, c.wantSub) {
				t.Fatalf("期望包含 %q,拿到 %q", c.wantSub, got)
			}
		})
	}
}
