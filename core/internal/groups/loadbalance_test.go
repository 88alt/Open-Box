package groups

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// 负载均衡(loadbalance)的期望值:对着上游 v0.1.387 的行为核对过,下面的用例只记结论。

func lbNodes(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = map[string]any{"tag": "N" + strconv.Itoa(i)}
	}
	return out
}

func lbOutbound(t *testing.T, res *EmitResult, tag string) Outbound {
	t.Helper()
	for _, o := range res.Outbounds {
		if o.Tag == tag {
			return o
		}
	}
	t.Fatalf("没有出站 %s", tag)
	return Outbound{}
}

func lbGroup(extra map[string]any) []any {
	g := map[string]any{"id": "g1", "name": "LB", "type": "loadbalance", "mode": "static", "members": []any{"N0", "N1"}}
	for k, v := range extra {
		g[k] = v
	}
	return []any{g}
}

func TestLoadBalanceNormalizeInterval(t *testing.T) {
	cases := map[any]string{
		nil: "120s", "": "120s", "abc": "120s", "1s": "120s", "4s": "120s", "25h": "120s", 30: "120s",
		"5s": "5s", "90s": "90s", "5m": "5m", " 10m ": "10m", "24h": "24h", "1h30m": "1h30m", "5000ms": "5000ms",
	}
	for in, want := range cases {
		g := NormalizeGroups(lbGroup(map[string]any{"interval": in}))
		var got string
		for _, x := range g {
			if x.ID == "g1" {
				got = x.Interval
			}
		}
		if got != want {
			t.Errorf("interval %#v → %q,要 %q", in, got, want)
		}
	}
}

func TestLoadBalanceNormalizeDropsUrltestFields(t *testing.T) {
	g := NormalizeGroups(lbGroup(map[string]any{"tolerance": 50, "idleTimeout": "1h", "testUrl": "https://a.com/204", "expectedStatus": "204"}))
	for _, x := range g {
		if x.ID != "g1" {
			continue
		}
		b, _ := x.MarshalJSON()
		s := string(b)
		for _, bad := range []string{"tolerance", "idleTimeout", "lanes", "failover"} {
			if strings.Contains(s, `"`+bad+`"`) {
				t.Errorf("负载均衡不该带 %s: %s", bad, s)
			}
		}
		for _, want := range []string{`"testUrl":"https://a.com/204"`, `"expectedStatus":"204"`, `"interval":"120s"`, `"type":"loadbalance"`} {
			if !strings.Contains(s, want) {
				t.Errorf("缺 %s: %s", want, s)
			}
		}
	}
}

func TestLoadBalanceEmit(t *testing.T) {
	res, err := EmitUserGroups(lbGroup(map[string]any{"interval": "30s"}), lbNodes(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	o := lbOutbound(t, res, "LB")
	want := Outbound{Type: "loadbalance", Tag: "LB", Outbounds: []string{"N0", "N1"}, URL: DefaultTestURL, Interval: "30s"}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("出站 %+v,要 %+v", o, want)
	}
	if o.Tolerance != nil || o.IdleTimeout != "" {
		t.Errorf("不该有 tolerance / idle_timeout: %+v", o)
	}
}

func TestLoadBalanceEmitGlobalAndOwnTestSettings(t *testing.T) {
	opts := map[string]any{"testUrl": "https://t.example/204", "expectedStatus": "200-299"}
	res, _ := EmitUserGroups(lbGroup(nil), lbNodes(2), opts)
	o := lbOutbound(t, res, "LB")
	if o.URL != "https://t.example/204" || o.ExpectedStatus != "200-299" {
		t.Errorf("没用上全局设置: %+v", o)
	}
	res, _ = EmitUserGroups(lbGroup(map[string]any{"testUrl": "https://own/204", "expectedStatus": "204"}), lbNodes(2), opts)
	o = lbOutbound(t, res, "LB")
	if o.URL != "https://own/204" || o.ExpectedStatus != "204" {
		t.Errorf("组自己的设置没优先: %+v", o)
	}
}

func TestLoadBalanceOnlyRealNodes(t *testing.T) {
	groups := []any{
		lbGroup(map[string]any{"members": []any{"N0", "G2", "不存在"}})[0],
		map[string]any{"id": "g2", "name": "G2", "type": "selector", "mode": "static", "members": []any{"N1"}},
	}
	res, err := EmitUserGroups(groups, lbNodes(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := lbOutbound(t, res, "LB").Outbounds; !reflect.DeepEqual(got, []string{"N0"}) {
		t.Errorf("成员 %v,要只剩真实节点 [N0]", got)
	}
	if len(res.Dropped) != 0 {
		t.Errorf("不该有被丢弃的组: %+v", res.Dropped)
	}
}

func TestLoadBalanceDynamicKeywords(t *testing.T) {
	nodes := []any{
		map[string]any{"tag": "HK-01"}, map[string]any{"tag": "US-01"}, map[string]any{"tag": "HK-02"},
	}
	res, err := EmitUserGroups(lbGroup(map[string]any{"mode": "dynamic", "keywords": []any{"HK"}, "members": []any{}}), nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := lbOutbound(t, res, "LB").Outbounds; !reflect.DeepEqual(got, []string{"HK-01", "HK-02"}) {
		t.Errorf("动态成员 %v", got)
	}
}

func TestLoadBalanceMaxNodes(t *testing.T) {
	names := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "N" + strconv.Itoa(i)
		}
		return out
	}
	if _, err := EmitUserGroups(lbGroup(map[string]any{"members": names(64)}), lbNodes(70), nil); err != nil {
		t.Errorf("64 个应该可以: %v", err)
	}
	if _, err := EmitUserGroups(lbGroup(map[string]any{"members": names(65)}), lbNodes(70), nil); err == nil {
		t.Error("65 个应该报错")
	}
	if _, err := EmitUserGroups(lbGroup(map[string]any{"mode": "dynamic", "members": []any{}}), lbNodes(70), nil); err == nil {
		t.Error("动态命中 70 个应该报错")
	}
}

func TestLoadBalanceEmptyKeepsEmptyOutbounds(t *testing.T) {
	res, err := EmitUserGroups(lbGroup(map[string]any{"members": []any{}}), lbNodes(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	// 与上游一致:空成员原样写出 "outbounds":[],不挂占位
	o := lbOutbound(t, res, "LB")
	b, _ := json.Marshal(o)
	if !strings.Contains(string(b), `"outbounds":[]`) || len(res.Placeholders) != 0 {
		t.Errorf("出站 %s placeholders=%v", b, res.Placeholders)
	}
}
