package terminaltest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsforward"
)

// 期望值人工推算。配置用 JSON 文本写,和真实 config.json 同形。
func cfgOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNativeBypassFromConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		on   bool
		sets []string
	}{
		{"整份集合原样", `{"inbounds":[{"type":"tun","route_exclude_address_set":["geoip-cn","geoip-private"]}]}`, true, []string{"geoip-cn", "geoip-private"}},
		{"热切换裁剪名还原成原名", `{"inbounds":[{"type":"tun","route_exclude_address_set":["obflip-byp-geoip-us"]}]}`, true, []string{"geoip-us"}},
		{"两种混着、保序", `{"inbounds":[{"type":"tun","route_exclude_address_set":["geoip-cn","obflip-byp-geoip-us"]}]}`, true, []string{"geoip-cn", "geoip-us"}},
		{"没有这个键", `{"inbounds":[{"type":"tun"}]}`, false, []string{}},
		{"空名单", `{"inbounds":[{"type":"tun","route_exclude_address_set":[]}]}`, false, []string{}},
		{"没有 tun 入站", `{"inbounds":[{"type":"mixed","route_exclude_address_set":["geoip-cn"]}]}`, false, []string{}},
		{"没有 inbounds", `{}`, false, []string{}},
		{"非字符串与空串跳过", `{"inbounds":[{"type":"tun","route_exclude_address_set":["geoip-cn",5,null,"",{"a":1}]}]}`, true, []string{"geoip-cn"}},
		{"只有前缀不还原", `{"inbounds":[{"type":"tun","route_exclude_address_set":["obflip-byp-"]}]}`, true, []string{"obflip-byp-"}},
		{"前缀不在开头不还原", `{"inbounds":[{"type":"tun","route_exclude_address_set":["x-obflip-byp-geoip-cn"]}]}`, true, []string{"x-obflip-byp-geoip-cn"}},
		{"只看第一个 tun 入站", `{"inbounds":[{"type":"mixed"},{"type":"tun","route_exclude_address_set":["a"]},{"type":"tun","route_exclude_address_set":["b"]}]}`, true, []string{"a"}},
		{"名单不是数组", `{"inbounds":[{"type":"tun","route_exclude_address_set":"geoip-cn"}]}`, false, []string{}},
	}
	for _, c := range cases {
		on, sets := NativeBypassFromConfig(cfgOf(t, c.cfg))
		if on != c.on || !reflect.DeepEqual(sets, c.sets) {
			t.Errorf("%s: got (%v, %#v),要 (%v, %#v)", c.name, on, sets, c.on, c.sets)
		}
	}
}

func fl(cfg string, cfgErr error, mode string, modeErr error, fwd *dnsforward.Result) *FirstLayer {
	return NewFirstLayer(
		func() (map[string]any, error) {
			if cfgErr != nil {
				return nil, cfgErr
			}
			var m map[string]any
			_ = json.Unmarshal([]byte(cfg), &m)
			return m, nil
		},
		func(context.Context) (string, error) { return mode, modeErr },
		func(context.Context) *dnsforward.Result { return fwd },
	)(context.Background())
}

const tunCN = `{"inbounds":[{"type":"tun","route_exclude_address_set":["geoip-cn"]}]}`

func TestNewFirstLayer(t *testing.T) {
	all := &dnsforward.Result{Forward: "kernel", Plan: "all", To: "127.0.0.1:7853"}
	// dnsmasq 且接管生效:转发计划取接管给的 plan
	got := fl(tunCN, nil, "dnsmasq", nil, all)
	want := &FirstLayer{BypassEnabled: true, BypassSets: []string{"geoip-cn"}, DNSMode: "dnsmasq", DNSForward: "all"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dnsmasq: %+v", got)
	}
	// 不是 dnsmasq:dnsmasq 不参与,none(即使 forward 给了东西也不看)
	for _, m := range []string{"hijack", "off", "whatever"} {
		got = fl(tunCN, nil, m, nil, all)
		if got == nil || got.DNSForward != "none" || got.DNSMode != m {
			t.Errorf("%s: %+v", m, got)
		}
	}
	// dnsmasq 但接管没生效(forward 为 nil):不知道,dnsForward 不带
	got = fl(tunCN, nil, "dnsmasq", nil, nil)
	if got == nil || got.DNSForward != "" || !got.BypassEnabled {
		t.Errorf("接管没生效: %+v", got)
	}
	b, _ := json.Marshal(got)
	if string(b) != `{"bypassEnabled":true,"bypassSets":["geoip-cn"],"dnsMode":"dnsmasq"}` {
		t.Errorf("序列化: %s", b)
	}
	// 没开旁路:bypassSets 是 [] 不是 null
	got = fl(`{"inbounds":[{"type":"tun"}]}`, nil, "hijack", nil, nil)
	b, _ = json.Marshal(got)
	if string(b) != `{"bypassEnabled":false,"bypassSets":[],"dnsMode":"hijack","dnsForward":"none"}` {
		t.Errorf("未开旁路序列化: %s", b)
	}
	// 读不到配置 / 档案模式:整个不带
	if got = fl("", errors.New("x"), "hijack", nil, nil); got != nil {
		t.Errorf("配置读不到应为 nil: %+v", got)
	}
	if got = fl(tunCN, nil, "", errors.New("x"), nil); got != nil {
		t.Errorf("模式读不到应为 nil: %+v", got)
	}
	if got = fl(tunCN, nil, "", nil, nil); got != nil {
		t.Errorf("模式为空应为 nil: %+v", got)
	}
}

func TestRunFillsFirstLayer(t *testing.T) {
	d := capableDeps(newFakeTerm())
	d.FirstLayer = func(context.Context) *FirstLayer {
		return &FirstLayer{BypassEnabled: true, BypassSets: []string{"geoip-cn"}, DNSMode: "hijack", DNSForward: "none"}
	}
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	f, _ := got["firstLayer"].(map[string]any)
	if f["bypassEnabled"] != true || f["dnsMode"] != "hijack" || f["dnsForward"] != "none" || len(f) != 4 {
		t.Fatalf("firstLayer: %v", got["firstLayer"])
	}
	if s, _ := f["bypassSets"].([]any); len(s) != 1 || s[0] != "geoip-cn" {
		t.Errorf("bypassSets: %v", f["bypassSets"])
	}
	// 没接线 / 返回 nil:不带
	d.FirstLayer = func(context.Context) *FirstLayer { return nil }
	if _, has := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["firstLayer"]; has {
		t.Errorf("返回 nil 时不该带 firstLayer")
	}
	// 建终端失败时没有 source,firstLayer 也不带(和 dnsForward 等一样只在测试真的做了才出现)
	f2 := newFakeTerm()
	f2.ensureErr = errors.New("boom")
	d2 := capableDeps(f2)
	d2.FirstLayer = func(context.Context) *FirstLayer { return &FirstLayer{DNSMode: "hijack"} }
	if _, has := decode(t, serve(t, d2, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["firstLayer"]; has {
		t.Errorf("setupError 时不该带 firstLayer")
	}
}

// 读配置报错时,即使同时返回了半份配置也不能拿来用。
func TestNewFirstLayerConfigErrorIgnoresPartialConfig(t *testing.T) {
	got := NewFirstLayer(
		func() (map[string]any, error) {
			return cfgOf(t, tunCN), errors.New("读不全当前配置")
		},
		func(context.Context) (string, error) { return "hijack", nil }, nil,
	)(context.Background())
	if got != nil {
		t.Errorf("应为 nil: %+v", got)
	}
}
