package penetration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/routedecide"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// 与 core 生成的顺序一致:bypass → sniff → DNS 劫持 → tun 防回环 → 前置自定义分流 → 私网直连 → ……
const customCfgJSON = `{
 "route": {"final": "其他", "rules": [
  {"type": "logical", "mode": "and", "rules": [{"source_ip_cidr": ["192.168.1.9"]}], "action": "bypass"},
  {"action": "sniff"},
  {"protocol": "dns", "action": "hijack-dns"},
  {"inbound": ["dns-in"], "action": "hijack-dns"},
  {"ip_cidr": ["198.18.0.0/16"], "action": "reject"},
  {"domain_suffix": ["work.example"], "outbound": "jp-01"},
  {"ip_cidr": ["203.0.113.0/24"], "outbound": "block"},
  {"type": "logical", "mode": "and", "rules": [{"ip_cidr": ["203.0.113.0/24"]}, {"ip_version": 6}], "action": "reject"},
  {"ip_is_private": true, "outbound": "direct"},
  {"rule_set": ["geosite-openai"], "outbound": "AI"}
 ]},
 "outbounds": [{"tag":"direct","type":"direct"},{"tag":"block","type":"block"},{"tag":"AI","type":"selector"},{"tag":"其他","type":"selector"},{"tag":"jp-01","type":"shadowsocks"}]
}`

func customSvc(t *testing.T, name func(context.Context) string) *Service {
	t.Helper()
	s := svc(t, nil, nil)
	s.Config = func() (map[string]any, error) {
		var m map[string]any
		return m, json.Unmarshal([]byte(customCfgJSON), &m)
	}
	s.Proxies = func(context.Context) ([]singbox.KernelProxy, error) { return proxies, nil }
	s.CustomName = name
	return s
}

func TestOwnerCustomPolicy(t *testing.T) {
	calls := 0
	s := customSvc(t, func(context.Context) string { calls++; return "我的前置分流" })
	r, err := s.Query(context.Background(), "a.work.example")
	if err != nil {
		t.Fatal(err)
	}
	if r.Matched == nil || r.Matched.Index != 5 || r.Matched.Outbound != "jp-01" {
		t.Fatalf("%+v", r.Matched)
	}
	if r.Owner == nil || r.Owner.Kind != "custom" || r.Owner.Name != "我的前置分流" {
		t.Fatalf("%+v", r.Owner)
	}
	if r.Matched.OwnerName != "我的前置分流" {
		t.Fatalf("ownerName: %q", r.Matched.OwnerName)
	}
	if calls != 1 {
		t.Fatalf("名字只在命中自定义分流时取一次: %d", calls)
	}
	b, _ := json.Marshal(r.Matched)
	if !json.Valid(b) || !contains(string(b), `"ownerName":"我的前置分流"`) {
		t.Fatalf("%s", b)
	}
}

// 命中别的规则时不取名字,matched 里也没有 ownerName
func TestOwnerCustomNotCalledForOtherRules(t *testing.T) {
	calls := 0
	s := customSvc(t, func(context.Context) string { calls++; return "x" })
	r, err := s.Query(context.Background(), "192.168.1.9")
	if err != nil {
		t.Fatal(err)
	}
	if r.Owner == nil || r.Owner.Name != "private" || r.Matched.OwnerName != "" || calls != 0 {
		t.Fatalf("%+v %+v calls=%d", r.Owner, r.Matched, calls)
	}
	r, _ = s.Query(context.Background(), "chat.openai.com")
	_ = r
	if calls != 0 {
		t.Fatalf("calls=%d", calls)
	}
}

// 不知道名字(没有钩子或返回空):不给 owner,也不退回去按形状猜
func TestOwnerCustomUnknownName(t *testing.T) {
	for _, name := range []func(context.Context) string{nil, func(context.Context) string { return "" }} {
		r, err := customSvc(t, name).Query(context.Background(), "a.work.example")
		if err != nil {
			t.Fatal(err)
		}
		if r.Owner != nil || r.Matched.OwnerName != "" {
			t.Fatalf("%+v %+v", r.Owner, r.Matched)
		}
	}
}

// 自定义分流的 IP 段规则形状和内核占位段(reject + ip_cidr)撞得上:位置优先,不能认成 kernel
func TestOwnerCustomBeatsKernelShape(t *testing.T) {
	s := customSvc(t, func(context.Context) string { return "前置" })
	r, err := s.Query(context.Background(), "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if r.Owner == nil || r.Owner.Kind != "custom" {
		t.Fatalf("%+v %+v", r.Owner, r.Matched)
	}
}

// 没有启用前置自定义分流:基础规则后面直接是私网直连,段是空的,原来的认法不变
func TestInCustomSpanEmptyWhenNoCustom(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(cfgJSON), &m); err != nil {
		t.Fatal(err)
	}
	v, err := routedecide.FromConfig(m)
	if err != nil {
		t.Fatal(err)
	}
	for i := range v.Rules {
		if inCustomSpan(v, i) {
			t.Fatalf("第 %d 条不该算自定义分流", i)
		}
	}
	// 配置里没有私网直连(不是 core 生成的):不认
	m2 := map[string]any{"route": map[string]any{"final": "x", "rules": []any{map[string]any{"domain": []any{"a"}, "outbound": "y"}}}}
	v2, _ := routedecide.FromConfig(m2)
	if inCustomSpan(v2, 0) {
		t.Fatal("没有私网直连不该认")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
