package terminaltest

import (
	"context"
	"errors"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/conntrack"
	"github.com/timeflysoon/Open-Box/backend/internal/nsprobe"
	"github.com/timeflysoon/Open-Box/backend/internal/routeget"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// holdProber 是能留连接的假探测器:出结果后调 during(连上了的话)。
type holdProber struct {
	*fakeProber
	duringCalled bool
}

func (h *holdProber) ProbeHold(ctx context.Context, ns string, req nsprobe.Request, during func(nsprobe.Result)) (nsprobe.Result, string, error) {
	res, se, err := h.Probe(ctx, ns, req)
	if err == nil && res.LocalPort != 0 && during != nil {
		h.duringCalled = true
		during(res)
	}
	return res, se, err
}

type fakeRoute struct {
	info  routeget.Info
	err   error
	calls [][3]string
}

func (f *fakeRoute) Get(_ context.Context, dst, src, iif string) (routeget.Info, error) {
	f.calls = append(f.calls, [3]string{dst, src, iif})
	return f.info, f.err
}

const (
	ctPlain      = "ipv4 2 tcp 6 100 ESTABLISHED src=192.168.5.77 dst=93.184.216.34 sport=40123 dport=443 src=93.184.216.34 dst=192.168.5.77 sport=443 dport=40123 [ASSURED] mark=0 use=2"
	ctMasq       = "ipv4 2 tcp 6 100 ESTABLISHED src=192.168.5.77 dst=93.184.216.34 sport=40123 dport=443 src=93.184.216.34 dst=10.0.0.2 sport=443 dport=40123 [ASSURED] mark=0 use=2"
	ctRedirectIn = "ipv4 2 tcp 6 100 ESTABLISHED src=192.168.5.77 dst=93.184.216.34 sport=40123 dport=443 src=192.168.5.1 dst=192.168.5.77 sport=7892 dport=40123 [ASSURED] mark=0 use=2"
	ctDnatOther  = "ipv4 2 tcp 6 100 ESTABLISHED src=192.168.5.77 dst=93.184.216.34 sport=40123 dport=443 src=10.9.9.9 dst=192.168.5.77 sport=8443 dport=40123 [ASSURED] mark=0 use=2"
)

type scenario struct {
	res    nsprobe.Result
	conns  []singbox.KernelConn
	kerr   error
	ct     []string
	cterr  error
	route  *fakeRoute
	noHold bool
}

func connectedRes() nsprobe.Result {
	return nsprobe.Result{OK: true, ConnectTo: "93.184.216.34", LocalPort: 40123,
		DNS: &nsprobe.DNSResult{Server: "192.168.5.1", OK: true, Answers: []string{"93.184.216.34"}, MS: 3}}
}

func ownConn() singbox.KernelConn {
	return singbox.KernelConn{ID: "1", Source: "192.168.5.77", SourcePort: "40123", Host: "example.com", DestIP: "93.184.216.34",
		Chains: []string{"hk-01", "海外"}, Rule: "rule_set=geosite-foo", RulePayload: "geosite-foo", Inbound: "redirect/redirect-in"}
}

func runScenario(t *testing.T, sc scenario) map[string]any {
	t.Helper()
	d := capableDeps(newFakeTerm())
	hp := &holdProber{fakeProber: &fakeProber{res: sc.res}}
	if sc.noHold {
		d.Prober = hp.fakeProber
	} else {
		d.Prober = hp
	}
	d.Kernel = func(context.Context) (singbox.KernelSnapshot, error) {
		return singbox.KernelSnapshot{Conns: sc.conns, DirectTags: []string{"直连"}}, sc.kerr
	}
	var ents []conntrack.Entry
	for _, l := range sc.ct {
		ents = append(ents, ctEntry(t, l))
	}
	d.Conntrack = &fakeCT{entries: ents, err: sc.cterr}
	if sc.route != nil {
		d.Route = sc.route
	}
	return decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
}

func entryOf(t *testing.T, got map[string]any) map[string]any {
	t.Helper()
	e, ok := got["entry"].(map[string]any)
	if !ok {
		t.Fatalf("没有 entry:%v", got)
	}
	if _, ok := e["evidence"].(map[string]any); !ok {
		t.Fatalf("entry.evidence 必须是对象:%v", e)
	}
	return e
}

func evOf(e map[string]any) map[string]any { return e["evidence"].(map[string]any) }

var wanRoute = routeget.Info{Line: "93.184.216.34 from 192.168.5.77 via 10.0.0.1 dev eth1", Dev: "eth1", Via: "10.0.0.1"}
var tunRoute = routeget.Info{Line: "93.184.216.34 from 192.168.5.77 dev tun0", Dev: "tun0", Tun: true}

func TestEntryKernelSeenViaConnectionTable(t *testing.T) {
	got := runScenario(t, scenario{res: connectedRes(), conns: []singbox.KernelConn{ownConn()}, ct: []string{ctPlain}, route: &fakeRoute{info: wanRoute}})
	e := entryOf(t, got)
	if e["kind"] != "kernel" || e["via"] != "connection-table" || evOf(e)["kernelConn"] != true || evOf(e)["conntrack"] != ctPlain {
		t.Fatalf("%v", e)
	}
	k := got["kernel"].(map[string]any)
	chains := k["chains"].([]any)
	if k["seen"] != true || k["rule"] != "rule_set=geosite-foo" || k["rulePayload"] != "geosite-foo" || k["viaProxy"] != true ||
		k["inbound"] != "redirect/redirect-in" || k["destinationIP"] != "93.184.216.34" || k["host"] != "example.com" ||
		len(chains) != 2 || chains[0] != "海外" || chains[1] != "hk-01" {
		t.Fatalf("kernel:%v(链要反过来:路由选中的在前)", k)
	}
	if _, has := got["exit"].(map[string]any)["forward"]; has {
		t.Error("进了内核不该有 exit.forward")
	}
}

func TestEntryKernelSeenRedirectAndTun(t *testing.T) {
	e := entryOf(t, runScenario(t, scenario{res: connectedRes(), conns: []singbox.KernelConn{ownConn()}, ct: []string{ctRedirectIn}, route: &fakeRoute{info: wanRoute}}))
	if e["kind"] != "kernel" || e["via"] != "redirect" || e["redirectPort"] != float64(7892) || e["rewrittenTo"] != "192.168.5.1" {
		t.Fatalf("回复方被改写成路由器自己的地址:%v", e)
	}
	e = entryOf(t, runScenario(t, scenario{res: connectedRes(), conns: []singbox.KernelConn{ownConn()}, ct: []string{ctPlain}, route: &fakeRoute{info: tunRoute}}))
	if e["kind"] != "kernel" || e["via"] != "tun" || evOf(e)["tunDevice"] != "tun0" || evOf(e)["route"] != tunRoute.Line {
		t.Fatalf("路由进 tun 且内核里有:%v", e)
	}
}

func TestEntryKernelMatchesOnlyOwnSourceAndPort(t *testing.T) {
	other := ownConn()
	other.SourcePort = "40124"
	otherSrc := ownConn()
	otherSrc.Source = "192.168.5.99"
	got := runScenario(t, scenario{res: connectedRes(), conns: []singbox.KernelConn{other, otherSrc}, ct: []string{ctPlain}, route: &fakeRoute{info: wanRoute}})
	if k := got["kernel"].(map[string]any); k["seen"] != false {
		t.Fatalf("别的端口 / 别的终端的连接不能认成这一条:%v", k)
	}
	if e := entryOf(t, got); e["kind"] != "bypass" {
		t.Fatalf("%v", e)
	}
}

func TestEntryBypass(t *testing.T) {
	rt := &fakeRoute{info: wanRoute}
	got := runScenario(t, scenario{res: connectedRes(), ct: []string{ctMasq}, route: rt})
	e := entryOf(t, got)
	if e["kind"] != "bypass" || e["device"] != "eth1" || e["gateway"] != "10.0.0.1" || e["masquerade"] != true || evOf(e)["kernelConn"] != false {
		t.Fatalf("%v", e)
	}
	fwd := got["exit"].(map[string]any)["forward"].(map[string]any)
	if fwd["device"] != "eth1" || fwd["gateway"] != "10.0.0.1" || fwd["masquerade"] != true || fwd["address"] != "10.0.0.2" {
		t.Fatalf("exit.forward:%v", fwd)
	}
	if len(rt.calls) != 1 || rt.calls[0] != [3]string{"93.184.216.34", "192.168.5.77", "br-lan"} {
		t.Fatalf("路由查询的参数:%v", rt.calls)
	}
	// 没有 NAT:回复直接发回终端
	got = runScenario(t, scenario{res: connectedRes(), ct: []string{ctPlain}, route: &fakeRoute{info: wanRoute}})
	e = entryOf(t, got)
	if e["kind"] != "bypass" || e["masquerade"] != false || got["exit"].(map[string]any)["forward"].(map[string]any)["address"] != "" {
		t.Fatalf("%v", got)
	}
	if k := got["kernel"].(map[string]any); k["seen"] != false {
		t.Fatalf("旁路时 kernel.seen 应是 false:%v", k)
	}
}

func TestEntryKernelNotSeenButRewrittenToRouter(t *testing.T) {
	got := runScenario(t, scenario{res: connectedRes(), ct: []string{ctRedirectIn}, route: &fakeRoute{info: wanRoute}})
	e := entryOf(t, got)
	if e["kind"] != "kernel" || e["via"] != "redirect" || e["redirectPort"] != float64(7892) || evOf(e)["kernelConn"] != false {
		t.Fatalf("%v", e)
	}
	if _, has := got["exit"].(map[string]any)["forward"]; has {
		t.Error("没有旁路,不该有 exit.forward")
	}
}

func TestEntryUnknownCases(t *testing.T) {
	cases := []struct {
		name   string
		sc     scenario
		reason string
		check  func(t *testing.T, e map[string]any)
	}{
		{"改写到别处", scenario{res: connectedRes(), ct: []string{ctDnatOther}, route: &fakeRoute{info: wanRoute}}, "dnat-elsewhere",
			func(t *testing.T, e map[string]any) {
				if e["rewrittenTo"] != "10.9.9.9" {
					t.Errorf("%v", e)
				}
			}},
		{"conntrack 里没有这条连接", scenario{res: connectedRes(), route: &fakeRoute{info: wanRoute}}, "no-conntrack", nil},
		{"读不到 conntrack", scenario{res: connectedRes(), cterr: errors.New("permission denied"), route: &fakeRoute{info: wanRoute}}, "no-conntrack",
			func(t *testing.T, e map[string]any) {
				if evOf(e)["conntrackError"] != "permission denied" {
					t.Errorf("%v", e)
				}
			}},
		{"路由查询失败", scenario{res: connectedRes(), ct: []string{ctPlain}, route: &fakeRoute{err: errors.New("unreachable")}}, "no-route", nil},
		{"没接路由查询", scenario{res: connectedRes(), ct: []string{ctPlain}}, "no-route", nil},
		{"没被改写、内核没有、路由却进 tun", scenario{res: connectedRes(), ct: []string{ctPlain}, route: &fakeRoute{info: tunRoute}}, "route-tun", nil},
		{"读内核连接表出错", scenario{res: connectedRes(), kerr: errors.New("控制接口连不上"), ct: []string{ctPlain}, route: &fakeRoute{info: wanRoute}}, "evidence-failed",
			func(t *testing.T, e map[string]any) {
				if e["error"] != "控制接口连不上" || evOf(e)["kernelError"] != "控制接口连不上" {
					t.Errorf("%v", e)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runScenario(t, c.sc)
			e := entryOf(t, got)
			if e["kind"] != "unknown" || e["reason"] != c.reason {
				t.Fatalf("%v", e)
			}
			if c.check != nil {
				c.check(t, e)
			}
			if _, has := got["exit"].(map[string]any)["forward"]; has {
				t.Error("unknown 不该有 exit.forward")
			}
		})
	}
}

func TestEntryNotConnected(t *testing.T) {
	// DNS 没解析出地址
	r := nsprobe.Result{OK: false, Error: "解析失败", DNS: &nsprobe.DNSResult{Server: "192.168.5.1", OK: false, Answers: []string{}, Error: "解析失败"}}
	if e := entryOf(t, runScenario(t, scenario{res: r})); e["kind"] != "unknown" || e["reason"] != "not-connected" {
		t.Fatalf("%v", e)
	}
	// 目标是 IP、连接没建起来:拿不到连接信息
	r = nsprobe.Result{OK: false, Error: "连接被拒绝"}
	if e := entryOf(t, runScenario(t, scenario{res: r})); e["kind"] != "unknown" || e["reason"] != "no-connection" {
		t.Fatalf("%v", e)
	}
}

func TestEntryAndKernelAbsentWithoutHoldOrKernelDep(t *testing.T) {
	got := runScenario(t, scenario{res: connectedRes(), conns: []singbox.KernelConn{ownConn()}, ct: []string{ctPlain}, noHold: true})
	for _, k := range []string{"entry", "kernel"} {
		if _, has := got[k]; has {
			t.Errorf("探测器不能留连接时不该有 %s", k)
		}
	}
	d := capableDeps(newFakeTerm())
	d.Prober = &holdProber{fakeProber: &fakeProber{res: connectedRes()}} // 没接 Kernel
	got = decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	for _, k := range []string{"entry", "kernel"} {
		if _, has := got[k]; has {
			t.Errorf("没接 Kernel 不该有 %s", k)
		}
	}
}

func TestEntryAsksConntrackForTheExactConnection(t *testing.T) {
	d := capableDeps(newFakeTerm())
	d.Prober = &holdProber{fakeProber: &fakeProber{res: connectedRes()}}
	d.Kernel = func(context.Context) (singbox.KernelSnapshot, error) { return singbox.KernelSnapshot{}, nil }
	ct := &fakeCT{}
	d.Conntrack = ct
	d.Route = &fakeRoute{info: wanRoute}
	decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com","port":8443}`))
	want := conntrack.Query{Proto: "tcp", Src: "192.168.5.77", Sport: 40123, Dst: "93.184.216.34", Dport: 8443}
	found := false
	for _, q := range ct.queries {
		if q == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("要按「终端地址 + 源端口 + 解析出的地址 + 端口」认这一条连接:%+v", ct.queries)
	}
}
