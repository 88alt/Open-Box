package traffic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/traffic"
)

// 路由器自己的地址要被标成 self,普通终端不标;两者在同一批 rows 里要能分得开。
func TestNameMarksSelfAndKeepsHostName(t *testing.T) {
	d := Deps{
		Names: func() map[string]string {
			return map[string]string{"192.168.2.5": "router", "192.168.2.50": "laptop"}
		},
		Self: func(addr string) (string, string, bool) {
			if addr == "192.168.2.5" {
				return "lan", "br-lan", true
			}
			return "", "", false
		},
	}
	rows := []traffic.Row{
		{Key: "192.168.2.5", Up: 10},
		{Key: "192.168.2.50", Up: 20},
		{Key: "203.0.113.9", Up: 30},
	}
	d.name(rows)

	if rows[0].Self == nil || rows[0].Self.Kind != "lan" || rows[0].Self.Iface != "br-lan" {
		t.Fatalf("本机地址要带 self(lan/br-lan):%+v", rows[0])
	}
	if rows[1].Self != nil {
		t.Fatalf("普通终端不该带 self:%+v", rows[1])
	}
	if rows[1].Name != "laptop" {
		t.Fatalf("主机名要照旧带上:%+v", rows[1])
	}
	if rows[2].Name != "" {
		t.Fatalf("没有租约的地址不该有 name:%+v", rows[2])
	}
}

// 没接 Self 时行为与以前完全一致(不带 self,也不崩)。
func TestNameWithoutSelfDependency(t *testing.T) {
	d := Deps{Names: func() map[string]string { return map[string]string{"192.168.2.5": "router"} }}
	rows := []traffic.Row{{Key: "192.168.2.5"}}
	d.name(rows)
	if rows[0].Self != nil || rows[0].Name != "router" {
		t.Fatalf("%+v", rows[0])
	}
}

// 只有 Self、没有 Names 时也要能标出来。
func TestNameSelfWithoutNames(t *testing.T) {
	d := Deps{Self: func(string) (string, string, bool) { return "wan", "pppoe-wan", true }}
	rows := []traffic.Row{{Key: "1.2.3.4"}}
	d.name(rows)
	if rows[0].Self == nil || rows[0].Self.Kind != "wan" || rows[0].Self.Iface != "pppoe-wan" {
		t.Fatalf("%+v", rows[0])
	}
}

// JSON 形状:self 要在,普通行不能出现 "self":null(前端用 row.self 判真假)。
func TestRowJSONShape(t *testing.T) {
	b, _ := json.Marshal(traffic.Row{Key: "a", Up: 1})
	if strings.Contains(string(b), "self") {
		t.Fatalf("没有 self 时不该出现这个键:%s", b)
	}
	b, _ = json.Marshal(traffic.Row{Key: "a", Self: &traffic.SelfRef{Kind: "lan", Iface: "br-lan"}})
	if !strings.Contains(string(b), `"self":{"kind":"lan","iface":"br-lan"}`) {
		t.Fatalf("self 形状不对:%s", b)
	}
}
