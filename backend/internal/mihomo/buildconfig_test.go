package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 3e-5 装配本体的向量:用桩 core 返回最小输入,核对键序、条件键、规则拼装与 YAML 形状。

type stubCall map[string]string

func (s stubCall) call(_ context.Context, method string, _ ...any) (json.RawMessage, error) {
	if raw, ok := s[method]; ok {
		return json.RawMessage(raw), nil
	}
	return nil, fmt.Errorf("桩 core 没有实现 %s", method)
}

func minimalCall(t *testing.T) stubCall {
	t.Helper()
	return stubCall{
		"builtinTags": `{"direct":"direct","block":"block","directEnabled":true,"blockEnabled":true}`,
		"normalizeRouting": `{"custom":{"enabled":false,"rules":[]},"policies":[],"activePolicies":[],` +
			`"fallback":{"name":"其他","icon":"globe:earth-meridians","default":"proxy"}}`,
		"mihomoGroups": `{"proxies":[{"name":"n1","type":"ss","server":"1.2.3.4","port":8388}],` +
			`"groups":[{"name":"PROXY","type":"select","proxies":["n1","DIRECT"]}],` +
			`"first":{"PROXY":"n1"},"targets":{"direct":"DIRECT","block":"REJECT","PROXY":"PROXY","n1":"n1"},` +
			`"builtins":{"direct":"direct","block":"block"},"failover":[]}`,
		"mihomoDnsUpstreams": `{"direct":{"protocol":"udp","server":"223.5.5.5","port":53},` +
			`"proxy":{"protocol":"tcp","server":"1.1.1.1","port":53},"directExtras":[],"proxyExtras":[]}`,
		"mihomoTargetRules": `{"rules":["DOMAIN-SUFFIX,example.com,PROXY","MATCH,PROXY"],` +
			`"firstTerminal":0,"endTerminal":0,"directTerminal":0,"firstPolicy":0}`,
	}
}

func minimalInput() BuildInput {
	return BuildInput{
		Platform: "linux",
		DataDir:  "/d",
		Profile: map[string]any{
			"ipv6": false, "directForNodes": false,
			"tun": map[string]any{"stack": "mixed", "autoRedirect": false},
			"dns": map[string]any{"mode": "hijack", "split": false},
			"servers": []any{}, "clientRoutes": []any{},
		},
		Nodes:    []map[string]any{{"tag": "n1", "type": "ss", "server": "1.2.3.4", "port": float64(8388)}},
		Groups:   []map[string]any{},
		Secret:   "s3cret",
		SystemDNS: nil,
	}
}

func TestBuildConfigMinimal(t *testing.T) {
	fs := newMemFS()
	in := minimalInput()
	res, err := BuildConfig(context.Background(), in, BuildDeps{
		FS: fs, Compiler: NewCompiler(minimalCall(t).call),
		RulesDir: "/d/mihomo/rules", MihomoDir: "/d/mihomo",
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := strings.Join(res.Config.Keys(), ",")
	want := "mode,ipv6,allow-lan,bind-address,external-controller,secret,log-level,mixed-port," +
		"openbox-builtin-names,hosts,profile,tun,sniffer,dns,proxies,proxy-groups,rule-providers,rules"
	if keys != want {
		t.Fatalf("顶层键序 = %s\n想要 = %s", keys, want)
	}
	if v, _ := res.Config.Get("ipv6"); v != false {
		t.Errorf("ipv6 = %v", v)
	}
	if v, _ := res.Config.Get("secret"); v != "s3cret" {
		t.Errorf("secret = %v", v)
	}
	// 纯 TUN(autoRedirect=false)且有 tcpMss 才写 routing-mark;默认 tcpMss 是 0,所以这里不该有。
	if _, has := res.Config.Get("routing-mark"); has {
		t.Error("没配 tcpMss 就不该写 routing-mark")
	}
	tun, _ := res.Config.Get("tun")
	tunMap := tun.(*OrderedMap)
	if got := strings.Join(tunMap.Keys(), ","); got != "enable,device,stack,auto-route,auto-redirect,auto-detect-interface,dns-mode,dns-hijack" {
		t.Fatalf("tun 键序 = %s", got)
	}
	if v, _ := tunMap.Get("auto-redirect"); v != false {
		t.Errorf("auto-redirect = %v", v)
	}
	if v, _ := tunMap.Get("dns-mode"); v != "hijack" {
		t.Errorf("dns-mode = %v", v)
	}
	sniffer, _ := res.Config.Get("sniffer")
	if got := strings.Join(sniffer.(*OrderedMap).Keys(), ","); got != "enable,parse-pure-ip,override-destination,sniff" {
		t.Fatalf("sniffer 键序 = %s", got)
	}
	rules, _ := res.Config.Get("rules")
	rulesList := rules.([]any)
	if len(rulesList) != 5 {
		t.Fatalf("规则条数 = %d:%v", len(rulesList), rulesList)
	}
	// 代理出口的规则在 ipv6 关的时候不加 ::/0 的拒绝,但 rejectQuic 没开也不加 QUIC 拒绝。
	if rulesList[0] != "DOMAIN,openbox-health.invalid,DIRECT" ||
		rulesList[1] != "IP-CIDR,198.19.0.0/16,REJECT,no-resolve" ||
		rulesList[2] != "IP-CIDR6,2001:2::/49,REJECT,no-resolve" ||
		rulesList[3] != "DOMAIN-SUFFIX,example.com,PROXY" || rulesList[4] != "MATCH,PROXY" {
		t.Errorf("规则 = %v", rulesList)
	}
	// 没有共享网络入口、也没有直连 IP 段:listeners 与 ob-entry-direct 都不该出现。
	if _, has := res.Config.Get("listeners"); has {
		t.Error("没有共享入口就不该写 listeners")
	}
	rp, _ := res.Config.Get("rule-providers")
	if rp.(*OrderedMap).Has(mihomoEntryDirect) {
		t.Error("没有直连 IP 段就不该写 ob-entry-direct")
	}
	// 入口 nft 上游每次装配都会生成(纯 TUN 也要给终端 DNS 改道),只是内容不同。
	if res.Entry == "" {
		t.Error("入口 nft 不该是空串")
	}
}

func TestBuildConfigSecretRequired(t *testing.T) {
	in := minimalInput()
	in.Secret = " "
	if _, err := BuildConfig(context.Background(), in, BuildDeps{
		FS: newMemFS(), Compiler: NewCompiler(minimalCall(t).call),
	}); err == nil || !strings.Contains(err.Error(), "内核控制密钥") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildConfigDnsmasqOnNonOpenWrt(t *testing.T) {
	in := minimalInput()
	in.Profile["dns"] = map[string]any{"mode": "dnsmasq"}
	res, err := BuildConfig(context.Background(), in, BuildDeps{
		FS: newMemFS(), Compiler: NewCompiler(minimalCall(t).call),
	})
	if err != nil {
		t.Fatal(err)
	}
	tun, _ := res.Config.Get("tun")
	if v, _ := tun.(*OrderedMap).Get("dns-mode"); v != "hijack" {
		t.Errorf("非 openwrt 平台的 dnsmasq 应该按 hijack 生成,拿到 %v", v)
	}
}

func TestBuildConfigOpenWrtKeepsDnsmasq(t *testing.T) {
	in := minimalInput()
	in.Platform = "openwrt"
	in.Profile["dns"] = map[string]any{"mode": "dnsmasq"}
	in.Profile["clientRoutes"] = []any{
		map[string]any{"id": "c1", "enabled": true, "match": "ip", "sources": []any{"192.168.2.10"}, "outbound": "PROXY"},
	}
	res, err := BuildConfig(context.Background(), in, BuildDeps{
		FS: newMemFS(), Compiler: NewCompiler(minimalCall(t).call),
	})
	if err != nil {
		t.Fatal(err)
	}
	tun, _ := res.Config.Get("tun")
	tunMap := tun.(*OrderedMap)
	if v, _ := tunMap.Get("dns-mode"); v != "disabled" {
		t.Errorf("openwrt 的 dnsmasq 模式要保留,dns-mode = %v", v)
	}
	// dnsmasq 下没有劫持,严格终端要显式把 TUN 的 DNS 写进 dns-hijack。
	hijack, _ := tunMap.Get("dns-hijack")
	list := hijack.([]any)
	if len(list) != 2 || list[0] != "198.18.0.2:53" || list[1] != "tcp://198.18.0.2:53" {
		t.Errorf("dns-hijack = %v", list)
	}
}

func TestProviderTags(t *testing.T) {
	r := RoutingModel{ActivePolicies: []RoutingPolicy{
		{Name: "A", Rulesets: []string{"geosite-cn", "list-aaaaaaaa"}},
		{Name: "B", Rulesets: []string{"list-bbbbbbbb"}},
	}}
	ruleLists := map[string]any{
		"list-aaaaaaaa": map[string]any{"domain": true, "ip": true},
		"list-bbbbbbbb": map[string]any{"domain": false, "ip": true},
	}
	got, err := providerTags(r, ruleLists)
	if err != nil {
		t.Fatal(err)
	}
	want := "geosite-cn,list-aaaaaaaa,list-aaaaaaaa-ip,list-bbbbbbbb-ip"
	if strings.Join(got, ",") != want {
		t.Fatalf("provider 名单 = %v,想要 %s", got, want)
	}

	// 规则集链接在档案里根本没登记(取不到 domain / ip 两个标记)时整份报错。
	_, err = providerTags(RoutingModel{ActivePolicies: []RoutingPolicy{
		{Name: "C", Rulesets: []string{"list-cccccccc"}},
	}}, nil)
	if err != errRuleListNotReady {
		t.Fatalf("err = %v", err)
	}
	// 登记了但两侧都关着 = 这条链接不产生任何规则集,不算没准备好(与上游 flatMap 同口径)。
	got, err = providerTags(RoutingModel{ActivePolicies: []RoutingPolicy{
		{Name: "B", Rulesets: []string{"list-bbbbbbbb"}},
	}}, map[string]any{"list-bbbbbbbb": map[string]any{"domain": false, "ip": false}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got = %v, err = %v", got, err)
	}
}

func TestProviderTagsCustom(t *testing.T) {
	r := RoutingModel{Custom: &CustomPolicy{
		Enabled: boolPointer(true),
		Rules: []CustomRule{
			{Type: "geosite", Value: "netflix", Outbound: "proxy"},
			{Type: "ruleUrl", Value: "https://e.com/a.list", Outbound: "proxy"},
			{Type: "domain", Value: "a.com", Outbound: "proxy"},
		},
	}}
	// 规则集链接在档案里登记过(domain 侧),才算准备好。
	got, err := providerTags(r, map[string]any{
		fnvListTag("https://e.com/a.list"): map[string]any{"domain": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "geosite-netflix" || got[1] != fnvListTag("https://e.com/a.list") {
		t.Fatalf("provider 名单 = %v", got)
	}
}

func boolPointer(v bool) *bool { return &v }

func TestSplitRule(t *testing.T) {
	cases := []struct{ in, match, target string }{
		{"DOMAIN,a.com,PROXY", "DOMAIN,a.com", "PROXY"},
		{"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,10.0.0.0/8,no-resolve", "DIRECT"},
		{"AND,((NETWORK,udp),(DST-PORT,443)),REJECT", "AND,((NETWORK,udp),(DST-PORT,443))", "REJECT"},
		{"MATCH,PROXY", "MATCH", "PROXY"},
	}
	for _, c := range cases {
		m, tg := splitRule(c.in)
		if m != c.match || tg != c.target {
			t.Errorf("splitRule(%q) = (%q,%q),想要 (%q,%q)", c.in, m, tg, c.match, c.target)
		}
	}
}

func TestRuleTargetName(t *testing.T) {
	if got := ruleTargetName("PROXY"); got != "PROXY" {
		t.Errorf("普通名字被改了:%q", got)
	}
	got := ruleTargetName("a,b(c)")
	if !strings.HasPrefix(got, specialNamePrefix) {
		t.Fatalf("特殊名字要编码:%q", got)
	}
	if decodeRuleTarget(got) != "a,b(c)" {
		t.Errorf("解码不回去:%q", decodeRuleTarget(got))
	}
	if got := ruleTargetName(" x "); !strings.HasPrefix(got, specialNamePrefix) {
		t.Errorf("首尾空格的名字要编码:%q", got)
	}
}

func TestRouteDNSServers(t *testing.T) {
	// udp:// 只认 IP,端口非 53 要写 server_port
	got := routeDNSServers(&RouteDNS{URL: "udp://9.9.9.9:5353"}, "c1")
	if len(got) != 1 || got[0].vals["type"] != "udp" || got[0].vals["server_port"] != 5353 {
		t.Fatalf("udp = %v", got)
	}
	if got[0].vals["tag"] != "dns-terminal-custom-c1.upstream" {
		t.Errorf("tag = %v", got[0].vals["tag"])
	}
	// https:// 域名要补 bootstrap,并给主服务器挂 domain_resolver
	got = routeDNSServers(&RouteDNS{URL: "https://dns.google/resolve", Bootstrap: "223.5.5.5"}, "c2")
	if len(got) != 2 {
		t.Fatalf("https = %v", got)
	}
	if got[0].vals["type"] != "https" || got[0].vals["path"] != "/resolve" {
		t.Errorf("主服务器 = %v", got[0].vals)
	}
	if got[0].vals["domain_resolver"] != "dns-terminal-custom-c2.upstream-bootstrap" {
		t.Errorf("domain_resolver = %v", got[0].vals["domain_resolver"])
	}
	if got[1].vals["type"] != "udp" || got[1].vals["server"] != "223.5.5.5" {
		t.Errorf("bootstrap = %v", got[1].vals)
	}
	// tls:// 默认 853,主机是 IP 就不用 bootstrap
	got = routeDNSServers(&RouteDNS{URL: "tls://1.1.1.1"}, "c3")
	if len(got) != 1 || got[0].vals["type"] != "tls" {
		t.Fatalf("tls = %v", got)
	}
	if _, has := got[0].vals["server_port"]; has {
		t.Error("853 是默认值,不该写 server_port")
	}
	// 认不出来的地址整条丢掉
	if got := routeDNSServers(&RouteDNS{URL: "ftp://x"}, "c4"); got != nil {
		t.Errorf("非法地址 = %v", got)
	}
}

func TestBuildTerminalDNS(t *testing.T) {
	routes := []ClientRoute{
		{ID: "c1", Sources: []string{"192.168.2.10"}, Outbound: "PROXY", Strict: true},
		{ID: "c2", Sources: []string{"192.168.2.11"}, Outbound: "DIRECT", Bypass: true},
		{ID: "c3", Sources: []string{"192.168.2.12"}, Outbound: "REJECT"},
	}
	builtin := BuiltinNames{Direct: "DIRECT", Block: "REJECT"}
	upstreams := DNSUpstreamSet{Proxy: DNSUpstreamServer{Protocol: "tcp", Server: "1.1.1.1", Port: 53}}
	rows := buildTerminalDNS(routes, func(string) string { return "proxy" }, builtin, "hijack",
		RouteOpts{DNSMode: "hijack", AutoRedirect: true}, upstreams,
		map[string]any{"dns": map[string]any{"split": true}}, "off")
	if len(rows) != 3 {
		t.Fatalf("行数 = %d", len(rows))
	}
	// 不进内核的排前面
	if v, _ := rows[0].(*OrderedMap).Get("bypass"); v != true {
		t.Errorf("第一行应该是 bypass:%v", rows[0])
	}
	strictRow := rows[1].(*OrderedMap)
	if v, _ := strictRow.Get("action"); v != "proxy" {
		t.Errorf("action = %v", v)
	}
	if v, _ := strictRow.Get("outbound"); v != "PROXY" {
		t.Errorf("outbound = %v", v)
	}
	if v, _ := strictRow.Get("nameserver"); v != "tcp://1.1.1.1:53" {
		t.Errorf("严格终端该用代理侧上游,拿到 %v", v)
	}
	rejectRow := rows[2].(*OrderedMap)
	if v, _ := rejectRow.Get("action"); v != "reject" {
		t.Errorf("拒绝行 action = %v", v)
	}
	if _, has := rejectRow.Get("nameserver"); has {
		t.Error("拒绝行不该给 nameserver")
	}
}

func TestBuildListeners(t *testing.T) {
	servers := []map[string]any{
		{"id": "a1", "protocol": "mixed", "port": float64(1080), "username": "u", "password": "p"},
		{"id": "a2", "protocol": "shadowsocks", "port": float64(8388), "method": "aes-128-gcm", "password": "pw"},
	}
	out, err := buildListeners(servers, BuildDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("listeners = %v", out)
	}
	mixed := out[0].(*OrderedMap)
	if v, _ := mixed.Get("name"); v != "share-a1" {
		t.Errorf("name = %v", v)
	}
	if v, _ := mixed.Get("udp"); v != false {
		t.Errorf("mixed 的 udp = %v", v)
	}
	ss := out[1].(*OrderedMap)
	if v, _ := ss.Get("udp"); v != true {
		t.Errorf("shadowsocks 的 udp = %v", v)
	}

	// 需要 TLS 的入口没给证书就报错(不静默降级)。
	if _, err := buildListeners([]map[string]any{{"id": "a3", "protocol": "tuic", "port": float64(443)}}, BuildDeps{}); err == nil {
		t.Fatal("tuic 没有证书应该报错")
	}

	// vless 不开 TLS 时是 allow-insecure,不需要证书。
	out, err = buildListeners([]map[string]any{{"id": "a4", "protocol": "vless", "port": float64(443), "uuid": "u"}}, BuildDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := out[0].(*OrderedMap).Get("allow-insecure"); v != true {
		t.Errorf("vless 明文入口 = %v", out[0])
	}
}

func TestMarshalYAML(t *testing.T) {
	m := NewOrdered()
	m.Set("mode", "rule")
	m.Set("port", 7891)
	m.Set("flag", true)
	m.Set("ratio", 1.5)
	nested := NewOrdered()
	nested.Set("a", "b")
	m.Set("nested", nested)
	m.Set("empty", NewOrdered())
	m.Set("list", []any{"x", "1.1.1.1", "a,b", true, NewOrdered()})
	item := NewOrdered()
	item.Set("name", "n1")
	item.Set("port", 8388)
	m.Set("items", []any{item})
	m.Set("nothing", []any{})

	got, err := MarshalYAML(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `mode: rule
port: 7891
flag: true
ratio: 1.5
nested:
  a: b
empty: {}
list:
  - x
  - 1.1.1.1
  - a,b
  - true
  - {}
items:
  - name: n1
    port: 8388
nothing: []
`
	if string(got) != want {
		t.Fatalf("YAML =\n%s\n想要 =\n%s", got, want)
	}
}

func TestMarshalYAMLQuotes(t *testing.T) {
	cases := map[string]string{
		"":            `""`,
		"true":        `"true"`,
		"123":         `"123"`,
		// yaml 库(上游用的那个)认为 1.1.1.1 和 -x 都是安全的纯文本,不引。
		"1.1.1.1":     "1.1.1.1",
		"MATCH,PROXY": "MATCH,PROXY",
		"a: b":        `"a: b"`,
		"-x":          "-x",
	}
	for in, want := range cases {
		if got := yamlScalarValue(in); got != want {
			t.Errorf("yamlScalarValue(%q) = %s,想要 %s", in, got, want)
		}
	}
}
