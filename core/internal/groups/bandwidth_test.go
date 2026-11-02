package groups

import (
	"encoding/json"
	"strings"
	"testing"
)

// 带宽择优:组上的 bandwidth 开关 → urltest / loadbalance 出站里的 bandwidth:true;
// 故障转移只写在多节点页签的内部自动择优子组上;手动选择没有这项;disableBandwidth(客户端配置)一律不写。

func bwGroup(typ string, extra map[string]any) []any {
	g := map[string]any{"id": "g1", "name": "G", "type": typ, "mode": "static", "members": []any{"N0", "N1"}}
	for k, v := range extra {
		g[k] = v
	}
	return []any{g}
}

func bwEmit(t *testing.T, groups []any, options map[string]any) *EmitResult {
	t.Helper()
	res, err := EmitUserGroups(groups, lbNodes(3), options)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBandwidthNormalize(t *testing.T) {
	for _, typ := range []string{"urltest", "loadbalance"} {
		for in, want := range map[any]bool{true: true, false: false, nil: false, "true": false, 1: false} {
			g := NormalizeGroups(bwGroup(typ, map[string]any{"bandwidth": in}))
			for _, x := range g {
				if x.ID == "g1" && x.Bandwidth != want {
					t.Errorf("%s bandwidth=%v 期望 %v 得到 %v", typ, in, want, x.Bandwidth)
				}
			}
		}
	}
	// selector 不认这个字段
	for _, x := range NormalizeGroups(bwGroup("selector", map[string]any{"bandwidth": true})) {
		if x.ID == "g1" && x.Bandwidth {
			t.Error("selector 不该带 bandwidth")
		}
	}
}

func TestBandwidthMarshalOnlyWhenOn(t *testing.T) {
	find := func(list []*Group) *Group {
		for _, x := range list {
			if x.ID == "g1" {
				return x
			}
		}
		t.Fatal("没有 g1")
		return nil
	}
	on, _ := json.Marshal(find(NormalizeGroups(bwGroup("urltest", map[string]any{"bandwidth": true}))))
	off, _ := json.Marshal(find(NormalizeGroups(bwGroup("urltest", nil))))
	if !strings.Contains(string(on), `"bandwidth":true`) {
		t.Errorf("开着时要序列化: %s", on)
	}
	if strings.Contains(string(off), "bandwidth") {
		t.Errorf("关着时不写,旧档案结果不变: %s", off)
	}
}

func TestBandwidthEmitUrltestAndLoadBalance(t *testing.T) {
	for _, typ := range []string{"urltest", "loadbalance"} {
		o := lbOutbound(t, bwEmit(t, bwGroup(typ, map[string]any{"bandwidth": true}), map[string]any{}), "G")
		if !o.Bandwidth {
			t.Errorf("%s 开着带宽,出站要有 bandwidth", typ)
		}
		b, _ := json.Marshal(o)
		if !strings.Contains(string(b), `"bandwidth":true`) {
			t.Errorf("%s 序列化缺 bandwidth: %s", typ, b)
		}
		off, _ := json.Marshal(lbOutbound(t, bwEmit(t, bwGroup(typ, nil), map[string]any{}), "G"))
		if strings.Contains(string(off), "bandwidth") {
			t.Errorf("%s 没开不该写 bandwidth: %s", typ, off)
		}
		// 客户端配置:disableBandwidth 压住
		c := lbOutbound(t, bwEmit(t, bwGroup(typ, map[string]any{"bandwidth": true}), map[string]any{"disableBandwidth": true}), "G")
		if c.Bandwidth {
			t.Errorf("%s disableBandwidth 时不该写 bandwidth", typ)
		}
	}
}

func TestBandwidthSelectorNever(t *testing.T) {
	o := lbOutbound(t, bwEmit(t, bwGroup("selector", map[string]any{"bandwidth": true}), map[string]any{}), "G")
	if o.Bandwidth {
		t.Error("selector 不写 bandwidth")
	}
}

func TestBandwidthFailoverLanes(t *testing.T) {
	lanes := []any{
		map[string]any{"id": "a", "name": "", "icon": "", "members": []any{"N0", "N1"}},                 // 多节点,自动择优
		map[string]any{"id": "b", "name": "", "icon": "", "members": []any{"N2"}},                       // 单节点
		map[string]any{"id": "c", "name": "", "icon": "", "members": []any{"N0", "N1"}, "manual": true}, // 手动
	}
	g := map[string]any{"id": "f1", "name": "FO", "type": "failover", "mode": "static", "lanes": lanes, "bandwidth": true}
	res := bwEmit(t, []any{g}, map[string]any{})
	var urltests, withBW int
	for _, o := range res.Outbounds {
		switch o.Type {
		case "urltest":
			urltests++
			if o.Bandwidth {
				withBW++
			}
		case "selector":
			if o.Bandwidth {
				t.Errorf("selector %s 不该有 bandwidth", o.Tag)
			}
		}
	}
	if urltests != 1 || withBW != 1 {
		t.Errorf("只有多节点自动页签的内部 urltest 带 bandwidth: urltest=%d 带=%d", urltests, withBW)
	}
	// 客户端
	res = bwEmit(t, []any{g}, map[string]any{"disableBandwidth": true})
	for _, o := range res.Outbounds {
		if o.Bandwidth {
			t.Errorf("disableBandwidth 时 %s 不该有 bandwidth", o.Tag)
		}
	}
}
