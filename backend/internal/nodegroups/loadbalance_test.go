package nodegroups

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func lb(extra map[string]any) []any {
	g := map[string]any{"id": "g1", "name": "LB", "type": "loadbalance", "mode": "static", "members": []any{"A", "B"}}
	for k, v := range extra {
		g[k] = v
	}
	return []any{g}
}

var realNodes = map[string]bool{"A": true, "B": true, "C": true}

func wantBad(t *testing.T, err error, sub string) {
	t.Helper()
	var ie *InputError
	if !errors.As(err, &ie) || !strings.Contains(ie.Msg, sub) {
		t.Fatalf("要 InputError 含 %q,实际 %v", sub, err)
	}
}

func TestLoadBalanceInterval(t *testing.T) {
	for _, ok := range []any{nil, "", "5s", "30s", "5m", "1h30m", "24h", "5000ms", "30", "7.5"} {
		if err := checkLoadBalance(lb(map[string]any{"interval": ok}), realNodes, nil); err != nil {
			t.Errorf("interval %#v 应通过: %v", ok, err)
		}
	}
	for _, bad := range []any{"4s", "25h", "abc", "NaN", "Inf", "-5s", "1e3", 30, true} {
		wantBad(t, checkLoadBalance(lb(map[string]any{"interval": bad}), realNodes, nil), "检测间隔范围为 5s 到 24h")
	}
}

func TestLoadBalanceMembers(t *testing.T) {
	wantBad(t, checkLoadBalance(lb(map[string]any{"members": []any{"A", "G2"}}), realNodes, nil), "只能添加真实节点：G2")
	wantBad(t, checkLoadBalance(lb(map[string]any{"members": "A"}), realNodes, nil), "members 必须是节点数组")
	wantBad(t, checkLoadBalance(lb(map[string]any{"members": []any{"A", ""}}), realNodes, nil), "包含无效成员")
	wantBad(t, checkLoadBalance(lb(map[string]any{"members": []any{"A", 1}}), realNodes, nil), "包含无效成员")
	// 动态组不查成员
	if err := checkLoadBalance(lb(map[string]any{"mode": "dynamic", "members": []any{}}), realNodes, nil); err != nil {
		t.Error(err)
	}
	// 其它类型的组不受影响
	other := []any{map[string]any{"id": "x", "name": "X", "type": "urltest", "members": []any{"G2"}, "interval": "1s"}}
	if err := checkLoadBalance(other, realNodes, nil); err != nil {
		t.Error(err)
	}
}

func TestLoadBalanceMax64(t *testing.T) {
	nodes := map[string]bool{}
	mk := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			name := "N" + strconv.Itoa(i)
			nodes[name] = true
			out[i] = name
		}
		return out
	}
	if err := checkLoadBalance(lb(map[string]any{"members": mk(64)}), nodes, nil); err != nil {
		t.Errorf("64 个应通过: %v", err)
	}
	wantBad(t, checkLoadBalance(lb(map[string]any{"members": mk(65)}), nodes, nil), "最多支持 64 个节点")
	// 重复的名字只算一个
	dup := make([]any, 100)
	for i := range dup {
		dup[i] = "A"
	}
	if err := checkLoadBalance(lb(map[string]any{"members": dup}), realNodes, nil); err != nil {
		t.Errorf("重复成员按去重算: %v", err)
	}
}

func TestLoadBalanceKeepsOldMembers(t *testing.T) {
	prev := []any{map[string]any{"id": "g1", "type": "loadbalance", "members": []any{"OLD", "A"}}}
	if err := checkLoadBalance(lb(map[string]any{"members": []any{"OLD", "A"}}), realNodes, prev); err != nil {
		t.Errorf("原来就有的成员应放行: %v", err)
	}
	wantBad(t, checkLoadBalance(lb(map[string]any{"members": []any{"NEW", "A"}}), realNodes, prev), "只能添加真实节点：NEW")
}
