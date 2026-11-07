package clientroutes

import (
	"reflect"
	"testing"
)

func TestExpandSourcesWithBindings(t *testing.T) {
	bindings := map[string]any{
		"192.168.1.10": []any{"fd00::10", "fe80::abcd"},
		"10.0.0.5/32":  "fd00::5",
		"bad":          "not-ip",
		"192.168.1.99": []any{"%invalid", "2001:db8::1"},
	}
	got := ExpandSourcesWithBindings([]string{"192.168.1.10/32", "10.0.0.5/32", "fd00::99/128"}, bindings)
	want := []string{"192.168.1.10/32", "fd00::10/128", "fe80::abcd/128", "10.0.0.5/32", "fd00::5/128", "fd00::99/128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	// 无绑定原样
	if g := ExpandSourcesWithBindings([]string{"1.2.3.4/32"}, nil); !reflect.DeepEqual(g, []string{"1.2.3.4/32"}) {
		t.Fatalf("nil bindings: %#v", g)
	}
	if g := ExpandSourcesWithBindings([]string{"1.2.3.4/32"}, map[string]any{}); !reflect.DeepEqual(g, []string{"1.2.3.4/32"}) {
		t.Fatalf("empty bindings: %#v", g)
	}
}

func TestNormalizeClientRoutesWithBindings(t *testing.T) {
	list := []any{
		map[string]any{
			"enabled": true, "id": "r1", "name": "phone",
			"match": "ip", "outbound": "proxy",
			"sources": []any{"192.168.1.10", "10.0.0.5/32"},
		},
		map[string]any{
			"enabled": true, "id": "r2", "match": "mac", "outbound": "direct",
			"macs": []any{"aa:bb:cc:dd:ee:ff"},
		},
	}
	opts := map[string]any{
		"directTag": "直连",
		"ipv6Bindings": map[string]any{
			"192.168.1.10": []any{"fd00::10"},
		},
	}
	routes := NormalizeClientRoutes(list, opts)
	if len(routes) != 2 {
		t.Fatalf("len=%d", len(routes))
	}
	if !reflect.DeepEqual(routes[0].Sources, []string{"192.168.1.10/32", "fd00::10/128", "10.0.0.5/32"}) {
		t.Fatalf("sources=%#v", routes[0].Sources)
	}
	// mac 路由不受影响
	if len(routes[1].Macs) != 1 || routes[1].Macs[0] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("macs=%#v", routes[1].Macs)
	}
	// 旧传法:直接传 directTag 字符串仍可用
	routes2 := NormalizeClientRoutes(list, "直连")
	if len(routes2) != 2 || routes2[0].Sources[0] != "192.168.1.10/32" {
		t.Fatalf("legacy: %#v", routes2)
	}
}
