package mihomo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// 3e-2 的自写向量:上游 EC 的输入是 profile + 分流规则 + 两侧上游 DNS,
// 输出是 dns 段(键序固定)与 ob-local-dns / ob-dns-* 这些 provider。

func dnsTestUpstreams() DNSUpstreamSet {
	return DNSUpstreamSet{
		Direct: DNSUpstreamServer{Protocol: "udp", Server: "223.5.5.5", Port: 53},
		Proxy: DNSUpstreamServer{
			Protocol: "https", Server: "dns.google", Port: 443, Path: "/dns-query",
			Bootstrap: &DNSUpstreamBoot{Protocol: "udp", Server: "223.5.5.5", Port: 53},
		},
	}
}

func dnsConfigMap(t *testing.T, r *DNSBuildResult) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, k := range r.Config.Keys() {
		v, _ := r.Config.Get(k)
		out[k] = v
	}
	return out
}

func TestBuildDNSConfigBasics(t *testing.T) {
	fs := newMemFS()
	in := DNSBuildInput{
		Profile:   map[string]any{"ipv6": false, "dns": map[string]any{"split": false, "mode": "hijack"}},
		Upstreams: dnsTestUpstreams(),
		IPv6Mode:  "off",
		Platform:  "linux",
	}
	r, err := BuildDNSConfig(fs, "/d/mihomo/rules", in)
	if err != nil {
		t.Fatal(err)
	}
	// 键序与上游一致
	wantKeys := []string{
		"enable", "listen", "ipv6", "enhanced-mode", "fake-ip-range", "fake-ip-range6",
		"default-nameserver", "use-system-hosts", "proxy-server-nameserver", "direct-nameserver",
		"nameserver", "nameserver-policy", "openbox-admit-dns-listen", "openbox-dns-policy",
	}
	if got := strings.Join(r.Config.Keys(), ","); got != strings.Join(wantKeys, ",") {
		t.Fatalf("键序 = %s", got)
	}
	cfg := dnsConfigMap(t, r)
	if cfg["listen"] != "0.0.0.0:7853" || cfg["enhanced-mode"] != "redir-host" {
		t.Errorf("listen / enhanced-mode = %v / %v", cfg["listen"], cfg["enhanced-mode"])
	}
	if cfg["fake-ip-range"] != "198.18.0.1/16" || cfg["fake-ip-range6"] != "" {
		t.Errorf("fake-ip-range = %v / ", cfg["fake-ip-range"])
	}
	if got := cfg["default-nameserver"].([]string); len(got) != 1 || got[0] != "udp://223.5.5.5:53" {
		t.Errorf("default-nameserver = %v", got)
	}
	// 直连侧不加 #RULES;代理侧要加,并把 bootstrap 与 https 的标记带上
	if got := cfg["direct-nameserver"].([]string); len(got) != 1 || got[0] != "udp://223.5.5.5:53" {
		t.Errorf("direct-nameserver = %v", got)
	}
	proxy := cfg["proxy-server-nameserver"].([]string)
	if len(proxy) != 1 || !strings.HasPrefix(proxy[0], "https://dns.google:443/dns-query#RULES&openbox-bootstrap=") ||
		!strings.Contains(proxy[0], "openbox-node-post=true") {
		t.Errorf("proxy-server-nameserver = %v", proxy)
	}
	// 兜底分类是 direct
	if got := cfg["nameserver"].([]string); len(got) != 1 || got[0] != "udp://223.5.5.5:53" {
		t.Errorf("nameserver = %v", got)
	}
	// hijack + 非 container:要有 ob-local-dns(带 domain_regex,所以是 classical 行为,与上游 Du 一致)
	p, ok := r.Providers["ob-local-dns"]
	if !ok || p.Behavior != "classical" || !strings.HasPrefix(p.Path, "./rules/ob-local-dns-") {
		t.Fatalf("provider = %+v", p)
	}
	payload := string(fs.files[filepath.Join("/d/mihomo/rules", strings.TrimPrefix(p.Path, "./rules/"))])
	if !strings.Contains(payload, "DOMAIN-REGEX,") || !strings.Contains(payload, `\.(lan|local|home|internal|home\.arpa)$`) {
		t.Errorf("ob-local-dns 载荷 = %q", payload)
	}
	policy := cfg["nameserver-policy"].(*OrderedMap)
	if v, _ := policy.Get("rule-set:ob-local-dns"); v != "system://" {
		t.Errorf("本地 DNS = %v", v)
	}
	// 没有 openbox-* 参数、不是 bypass、不是 container:不带 openbox-upstream-dns
	if _, has := cfg["openbox-upstream-dns"]; has {
		t.Error("不该带 openbox-upstream-dns")
	}
	// admit DNS 上游恒开(admitDns 就是 DNS 重写对象),非 openwrt 不带 -local
	if cfg["openbox-admit-dns-listen"] != "0.0.0.0:7855" {
		t.Errorf("openbox-admit-dns-listen = %v", cfg["openbox-admit-dns-listen"])
	}
	if _, has := cfg["openbox-admit-dns-local"]; has {
		t.Error("非 openwrt 不该带 openbox-admit-dns-local")
	}
	pb := cfg["openbox-dns-policy"].(*OrderedMap)
	if v, _ := pb.Get("local"); v != true {
		t.Errorf("openbox-dns-policy.local = %v", v)
	}
	if v, _ := pb.Get("suppress-https"); v != false {
		t.Errorf("suppress-https = %v", v)
	}
}

func TestBuildDNSConfigRewriteAndFakeIP(t *testing.T) {
	fs := newMemFS()
	in := DNSBuildInput{
		Profile: map[string]any{
			"ipv6": true,
			"dns": map[string]any{
				"split": true, "mode": "hijack", "fakeIpForProxy": true,
				"rewrite": map[string]any{
					"enabled": true,
					"rules": []any{
						map[string]any{"source": "*.example.com", "domain": "*.youtube.com"},
						map[string]any{"source": "A.Test.", "addresses": []any{"1.2.3.4", "1.2.3.4", "bad", "2001:DB8::1"}},
						map[string]any{"source": "", "domain": "x.com"},
						map[string]any{"source": "*.wild.test", "domain": "*.t.net"},
						map[string]any{"source": "off.test", "domain": "x.com", "enabled": false},
					},
				},
			},
		},
		Upstreams: dnsTestUpstreams(),
		IPv6Mode:  "node",
		Platform:  "linux",
	}
	r, err := BuildDNSConfig(fs, "/d/mihomo/rules", in)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dnsConfigMap(t, r)
	if cfg["listen"] != "[::]:7853" || cfg["ipv6"] != true {
		t.Errorf("listen = %v", cfg["listen"])
	}
	if cfg["enhanced-mode"] != "fake-ip" || cfg["fake-ip-range6"] != "2001:2:0:8000::1/49" {
		t.Errorf("enhanced-mode / range6 = %v / %v", cfg["enhanced-mode"], cfg["fake-ip-range6"])
	}
	policy := cfg["nameserver-policy"].(*OrderedMap)
	keys := strings.Join(policy.Keys(), ",")
	if keys != ".example.com,a.test,.wild.test,rule-set:ob-local-dns" {
		t.Fatalf("nameserver-policy 键序 = %s", keys)
	}
	if v, _ := policy.Get(".example.com"); v != "udp://127.0.0.1:7854" {
		t.Errorf("重写项 = %v", v)
	}
	pb := cfg["openbox-dns-policy"].(*OrderedMap)
	rw, _ := pb.Get("rewrite")
	want := []string{
		`DOMAIN-REGEX,^.+\.example\.com$`,
		"DOMAIN,a.test",
		`DOMAIN-REGEX,^.+\.wild\.test$`,
	}
	if got := rw.([]string); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rewrite = %v", got)
	}
	if v, _ := pb.Get("suppress-https"); v != true {
		t.Errorf("suppress-https = %v", v)
	}
	filter := cfg["fake-ip-filter"].([]string)
	wantFilter := []string{
		`DOMAIN-REGEX,^.+\.example\.com$,real-ip`,
		"DOMAIN,a.test,real-ip",
		`DOMAIN-REGEX,^.+\.wild\.test$,real-ip`,
		"RULE-SET,ob-local-dns,real-ip",
		"MATCH,real-ip",
	}
	if strings.Join(filter, "|") != strings.Join(wantFilter, "|") {
		t.Errorf("fake-ip-filter = %v", filter)
	}
}

func TestBuildDNSConfigSplitRules(t *testing.T) {
	fs := newMemFS()
	classOf := func(name string) string {
		switch name {
		case "P1":
			return "direct"
		case "P2", "P5":
			return "proxy"
		case "P4":
			return "reject"
		}
		return "proxy"
	}
	in := DNSBuildInput{
		Profile: map[string]any{"dns": map[string]any{"split": true, "mode": "hijack"}},
		Rules: []string{
			"DOMAIN,front.com,P1",
			"RULE-SET,geosite-x,P2",
			"DOMAIN-SUFFIX,mid.com,P3",
			"MATCH,P4",
			"DOMAIN,late.com,P5",
		},
		ClassOf:        classOf,
		Kinds:          map[string]string{"geosite-x": "domain", "geoip-y": "ipcidr"},
		Upstreams:      dnsTestUpstreams(),
		EndTerminal:    1,
		DirectTerminal: 2,
		FirstPolicy:    3,
		IPv6Mode:       "off",
		Platform:       "linux",
	}
	r, err := BuildDNSConfig(fs, "/d/mihomo/rules", in)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dnsConfigMap(t, r)
	// 兜底是 reject → rcode://refused
	if got := cfg["nameserver"].([]string); len(got) != 1 || got[0] != "rcode://refused" {
		t.Errorf("nameserver = %v", got)
	}
	policy := cfg["nameserver-policy"].(*OrderedMap)
	keys := policy.Keys()
	// [0,endTerminal) 被跳过(那是终端分流自己的 DNS),留下的按规则顺序:规则集 + 最后一条
	lateTag := "rule-set:ob-dns-" + ruleTagHash("DOMAIN", "late.com")
	if strings.Join(keys, ",") != "rule-set:ob-local-dns,rule-set:geosite-x,"+lateTag {
		t.Fatalf("nameserver-policy = %v", keys)
	}
	if v, _ := policy.Get("rule-set:geosite-x"); v.(string) == "" || !strings.Contains(v.(string), "RULES") {
		t.Errorf("规则集类 = %v", v)
	}
	if v, _ := policy.Get(lateTag); v.(string) == "" || !strings.Contains(v.(string), "RULES") {
		t.Errorf("代理类 = %v", v)
	}
	// 规则集不另建 provider
	if _, has := r.Providers["geosite-x"]; has {
		t.Error("规则集不该再写 provider")
	}
	if _, has := r.Providers["ob-dns-"+ruleTagHash("DOMAIN", "late.com")]; !has {
		t.Error("缺 ob-dns provider")
	}
	// 没有 fakeIpForProxy:不带 fake-ip-filter
	if _, has := cfg["fake-ip-filter"]; has {
		t.Error("不该带 fake-ip-filter")
	}
	// openbox-* 参数(代理侧带了 openbox-bootstrap)→ 开 openbox-upstream-dns
	if cfg["openbox-upstream-dns"] != true {
		t.Error("缺 openbox-upstream-dns")
	}
	// 终端 DNS 为空:不带终端那几键
	if _, has := cfg["openbox-terminal-dns"]; has {
		t.Error("不该带 openbox-terminal-dns")
	}
}

func TestBuildDNSConfigTerminalDNSAndContainer(t *testing.T) {
	fs := newMemFS()
	terminal := []any{map[string]any{"sources": []any{"192.168.1.10/32"}}}
	in := DNSBuildInput{
		Profile:     map[string]any{"dns": map[string]any{"split": true, "mode": "hijack"}},
		Upstreams:   dnsTestUpstreams(),
		TerminalDNS: terminal,
		IPv6Mode:    "bypass",
		Platform:    "container",
	}
	r, err := BuildDNSConfig(fs, "/d/mihomo/rules", in)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dnsConfigMap(t, r)
	// container 没有本地 DNS:不写 ob-local-dns
	if _, has := r.Providers["ob-local-dns"]; has {
		t.Error("container 不该写 ob-local-dns")
	}
	if cfg["openbox-lan-dns-listen"] != "0.0.0.0:53" || cfg["openbox-real-ipv6"] != true || cfg["openbox-upstream-dns"] != true {
		t.Errorf("container 补丁键 = %v / %v / %v", cfg["openbox-lan-dns-listen"], cfg["openbox-real-ipv6"], cfg["openbox-upstream-dns"])
	}
	pb := cfg["openbox-dns-policy"].(*OrderedMap)
	if v, _ := pb.Get("local"); v != false {
		t.Errorf("container 的 local = %v", v)
	}
	// 终端 DNS:container 不带本地 nameserver 那一键
	if cfg["openbox-terminal-dns"] == nil {
		t.Error("缺 openbox-terminal-dns")
	}
	if _, has := cfg["openbox-terminal-local-nameserver"]; has {
		t.Error("container 不该带 openbox-terminal-local-nameserver")
	}
	keys := r.Config.Keys()
	want := []string{
		"enable", "listen", "ipv6", "enhanced-mode", "fake-ip-range", "fake-ip-range6",
		"default-nameserver", "use-system-hosts", "proxy-server-nameserver", "direct-nameserver",
		"nameserver", "nameserver-policy", "openbox-upstream-dns", "openbox-lan-dns-listen",
		"openbox-real-ipv6", "openbox-admit-dns-listen", "openbox-dns-policy",
		"openbox-terminal-dns", "openbox-terminal-dns-rewrite", "openbox-terminal-dns-exempt",
		"openbox-terminal-proxy-nameserver",
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("键序 = %s", strings.Join(keys, ","))
	}
}

func TestNormalizeDNSRewriteHostAndTarget(t *testing.T) {
	cases := []struct {
		in     any
		want   string
		reason string
	}{
		{"  Example.COM. ", "example.com", "去空白、小写、去末尾点"},
		{"*.a.test", "*.a.test", "允许泛域名"},
		{"*", "", "只有星号"},
		{"a..b", "", "空标签"},
		{"-a.test", "", "标签以 - 开头"},
		{"a_b.test", "a_b.test", "下划线合法"},
		{strings.Repeat("a", 64) + ".test", "", "标签超过 63"},
	}
	for _, c := range cases {
		if got := normalizeRewriteHost(c.in, true); got != c.want {
			t.Errorf("%s: %v → %q,想要 %q", c.reason, c.in, got, c.want)
		}
	}
	if got := decodeRuleTarget("~openbox~YSxi"); got != "a,b" {
		t.Errorf("decodeRuleTarget = %q", got)
	}
	if got := decodeRuleTarget("plain"); got != "plain" {
		t.Errorf("decodeRuleTarget = %q", got)
	}
	if got := normalizeRewriteAddress(" 2001:DB8::1 "); got != "2001:db8::1" {
		t.Errorf("address = %q", got)
	}
	if got := normalizeRewriteAddress("bad"); got != "" {
		t.Errorf("address = %q", got)
	}
}

// core 的真实返回(2026-11-25 用 core 的 mihomoDnsUpstreams 跑出来的样子)要能解成 DNSUpstreamSet。
func TestFetchDNSUpstreams(t *testing.T) {
	raw := `{"direct":{"protocol":"udp","server":"9.9.9.9","port":53,"wan":true},
		"proxy":{"protocol":"https","server":"dns.google","port":443,"path":"/dns-query",
			"bootstrap":{"protocol":"udp","server":"223.5.5.5","port":53}},
		"directExtras":[],"proxyExtras":[{"protocol":"tls","server":"1.1.1.1","port":853}]}`
	var gotMethod string
	var gotArgs map[string]any
	call := func(ctx context.Context, method string, args ...any) (json.RawMessage, error) {
		gotMethod = method
		gotArgs, _ = args[0].(map[string]any)
		return json.RawMessage(raw), nil
	}
	set, err := FetchDNSUpstreams(context.Background(), call, map[string]any{"dns": map[string]any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "mihomoDnsUpstreams" {
		t.Errorf("方法 = %s", gotMethod)
	}
	if _, ok := gotArgs["systemDns"].([]any); !ok {
		t.Errorf("systemDns 应该是空数组,拿到 %#v", gotArgs["systemDns"])
	}
	if !set.Direct.Wan || set.Direct.Server != "9.9.9.9" {
		t.Errorf("direct = %+v", set.Direct)
	}
	if set.Proxy.Path != "/dns-query" || set.Proxy.Bootstrap == nil || set.Proxy.Bootstrap.Port != 53 {
		t.Errorf("proxy = %+v", set.Proxy)
	}
	if len(set.ProxyExtras) != 1 || set.ProxyExtras[0].Protocol != "tls" || set.ProxyExtras[0].Port != 853 {
		t.Errorf("proxyExtras = %+v", set.ProxyExtras)
	}
}
