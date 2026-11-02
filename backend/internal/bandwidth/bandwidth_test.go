package bandwidth

import (
	"context"
	"errors"
	"testing"
)

func TestSettingsFrom(t *testing.T) {
	cases := []struct {
		in   map[string]any
		want Settings
	}{
		{nil, Settings{2, 64}},
		{map[string]any{}, Settings{2, 64}},
		{map[string]any{"bandwidth": map[string]any{"intervalHours": float64(6), "maxDownloadMB": float64(128)}}, Settings{6, 128}},
		{map[string]any{"bandwidth": map[string]any{"intervalHours": float64(99), "maxDownloadMB": float64(5000)}}, Settings{24, 1024}},
		{map[string]any{"bandwidth": map[string]any{"intervalHours": float64(0), "maxDownloadMB": float64(-3)}}, Settings{1, 1}},
		{map[string]any{"bandwidth": map[string]any{"intervalHours": 1.5, "maxDownloadMB": "x"}}, Settings{2, 64}},
	}
	for _, c := range cases {
		if got := SettingsFrom(c.in); got != c.want {
			t.Errorf("%v → %v 期望 %v", c.in, got, c.want)
		}
	}
}

func TestDailyBytes(t *testing.T) {
	// 2 小时一轮 = 12 轮;3 个节点 × 64 MB × 12
	if got := DailyBytes(3, Settings{2, 64}); got != 3*64*1_000_000*12 {
		t.Errorf("got %d", got)
	}
	if got := (Settings{5, 1}).RoundsPerDay(); got != 5 { // 24/5 向上取整
		t.Errorf("rounds %d", got)
	}
	if got := DailyBytes(0, Settings{2, 64}); got != 0 {
		t.Errorf("0 个节点应是 0: %d", got)
	}
}

func node(tag, server string) map[string]any {
	return map[string]any{"type": "vless", "tag": tag, "server": server, "server_port": float64(443)}
}

func cfgOf(outbounds ...any) map[string]any {
	return map[string]any{"outbounds": outbounds, "dns": map[string]any{"servers": []any{map[string]any{"tag": "d", "type": "udp", "server": "1.1.1.1"}}}}
}

func group(typ, tag string, bw bool, members ...string) map[string]any {
	ms := make([]any, len(members))
	for i, m := range members {
		ms[i] = m
	}
	g := map[string]any{"type": typ, "tag": tag, "outbounds": ms}
	if bw {
		g["bandwidth"] = true
	}
	return g
}

func TestTargetsOnlyBandwidthGroups(t *testing.T) {
	cfg := cfgOf(node("A", "a.example"), node("B", "b.example"), node("C", "c.example"),
		group("urltest", "U", true, "A", "B"),
		group("urltest", "U2", false, "C"), // 没开,不测
		group("selector", "S", true, "C"),  // selector 不看 bandwidth
	)
	got := Targets(cfg, "")
	if len(got) != 2 || got[0].Tags[0] != "A" || got[1].Tags[0] != "B" || got[0].Groups[0] != "U" {
		t.Fatalf("得到 %+v", got)
	}
	// 没有任何组开着:一个都不测
	if n := len(Targets(cfgOf(node("A", "a"), group("urltest", "U", false, "A")), "")); n != 0 {
		t.Errorf("没开着应是 0: %d", n)
	}
}

func TestTargetsMergeSameLineAcrossGroups(t *testing.T) {
	// A 和 A2 是同一条线路(除 tag 外一模一样),分别在 U 和 L 里:只测一次,Tags / Groups 合并
	cfg := cfgOf(node("A", "same.example"), node("A2", "same.example"), node("B", "other.example"),
		group("urltest", "U", true, "A", "B"),
		group("loadbalance", "L", true, "A2"),
	)
	got := Targets(cfg, "")
	if len(got) != 2 {
		t.Fatalf("应合并成 2 条: %+v", got)
	}
	if len(got[0].Tags) != 2 || len(got[0].Groups) != 2 {
		t.Errorf("合并结果不对: %+v", got[0])
	}
}

func TestTargetsSkipNonLinesAndMissing(t *testing.T) {
	cfg := cfgOf(node("A", "a"),
		map[string]any{"type": "direct", "tag": "direct"},
		group("urltest", "Inner", false, "A"),
		group("urltest", "U", true, "A", "direct", "Inner", "ghost"), // 直连 / 组 / 不存在的都不测
	)
	got := Targets(cfg, "")
	if len(got) != 1 || got[0].Tags[0] != "A" {
		t.Fatalf("只该测 A: %+v", got)
	}
}

func TestTargetsFingerprintFollowsDetourDNSAndNetwork(t *testing.T) {
	mk := func(upstream, dnsServer string) map[string]any {
		a := node("A", "a.example")
		a["detour"] = "up"
		cfg := cfgOf(a, node("up", upstream), group("urltest", "U", true, "A"))
		cfg["dns"] = map[string]any{"servers": []any{map[string]any{"tag": "d", "type": "udp", "server": dnsServer}}}
		return cfg
	}
	base := Targets(mk("u1.example", "1.1.1.1"), "net1")[0].Fingerprint
	if again := Targets(mk("u1.example", "1.1.1.1"), "net1")[0].Fingerprint; again != base {
		t.Error("同样的输入指纹应一致")
	}
	for name, fp := range map[string]string{
		"前置节点变了": Targets(mk("u2.example", "1.1.1.1"), "net1")[0].Fingerprint,
		"DNS 变了": Targets(mk("u1.example", "8.8.8.8"), "net1")[0].Fingerprint,
		"网络变了":   Targets(mk("u1.example", "1.1.1.1"), "net2")[0].Fingerprint,
	} {
		if fp == base {
			t.Errorf("%s,指纹应不同", name)
		}
	}
	// detour 成环不能死循环
	a, b := node("A", "a"), node("B", "b")
	a["detour"], b["detour"] = "B", "A"
	if len(Targets(cfgOf(a, b, group("urltest", "U", true, "A")), "")) != 1 {
		t.Error("成环的配置也要能算")
	}
}

func TestDescribe(t *testing.T) {
	svc := &Service{
		Profile: func(context.Context) (map[string]any, error) {
			return map[string]any{"bandwidth": map[string]any{"intervalHours": float64(4), "maxDownloadMB": float64(100)}}, nil
		},
		Config: func() (map[string]any, error) {
			return cfgOf(node("A", "a"), node("B", "b"), group("urltest", "U", true, "A", "B")), nil
		},
		Runtime: func() Runtime { return Runtime{Running: true, LastError: "x", UpdatedAt: 7} },
	}
	st := svc.Describe(context.Background())
	if st.Settings != (Settings{4, 100}) || st.Nodes != 2 || st.DailyBytes != 2*100*1_000_000*6 || !st.Running || st.LastError != "x" || st.UpdatedAt != 7 {
		t.Errorf("%+v", st)
	}
	// 档案和配置都读不到:按默认设置、0 个节点
	svc = &Service{
		Profile: func(context.Context) (map[string]any, error) { return nil, errors.New("x") },
		Config:  func() (map[string]any, error) { return nil, errors.New("还没有部署过") },
	}
	st = svc.Describe(context.Background())
	if st.Settings != (Settings{2, 64}) || st.Nodes != 0 || st.DailyBytes != 0 {
		t.Errorf("%+v", st)
	}
}
