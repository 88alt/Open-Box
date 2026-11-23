package routing

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const mihomoRulesArgs = `{
 "routing": {"policies":[
   {"name":"国内","default":"direct","rulesets":["geosite-cn","geoip-cn"],"domain":["a.example.com"],"domainSuffix":["example.cn"],"domainKeyword":["kw"],"ipCidr":["10.9.0.0/16","2001:db8::/32"]},
   {"name":"海外","default":"Proxy","rulesets":["geosite-google"]}],
   "custom":{"enabled":true,"rules":[
     {"type":"domainSuffix","value":"x.com","outbound":"Proxy"},
     {"type":"port","value":"22,1000-2000","outbound":"direct"},
     {"type":"ipCidr","value":"1.2.3.0/24","outbound":"block"},
     {"type":"geosite","value":"netflix","outbound":"hk,1"},
     {"type":"domain","value":"y.com","outbound":"gone"}]},
   "fallbackDefault":"direct"},
 "providers": {"geosite-cn":"domain","geoip-cn":"ipcidr","geosite-google":"domain","geosite-netflix":"domain"},
 "ruleLists": {},
 "targets": {"direct":"DIRECT","block":"REJECT","直连":"DIRECT","拒绝":"REJECT","Proxy":"Proxy","国内":"国内","海外":"海外","其他":"其他","hk,1":"hk,1"},
 "directDomains": ["node.example.com"], "directCIDRs": ["203.0.113.0/24","2001:db8:1::/48"],
 "terminalRoutes": [
   {"match":"ip","sources":["192.168.1.10/32"],"outbound":"Proxy","strict":true},
   {"match":"mac","macs":["aa:bb:cc:dd:ee:ff"],"outbound":"直连","bypass":true,"strict":false},
   {"match":"ip","sources":["192.168.1.20/32"],"outbound":"拒绝","strict":false}],
 "sections": true}`

func TestMihomoRulesLayout(t *testing.T) {
	r, err := MihomoTargetRules(mustJSON(t, mihomoRulesArgs))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		// [0,1) 严格终端
		"SRC-IP-CIDR,192.168.1.10/32,Proxy",
		// [1,6) 前置自定义(出口不认识的 y.com 整行跳过;出口名带逗号的用 ~openbox~ 转义)
		"DOMAIN-SUFFIX,x.com,Proxy",
		"DST-PORT,22,DIRECT",
		"DST-PORT,1000-2000,DIRECT",
		"IP-CIDR,1.2.3.0/24,REJECT,no-resolve",
		"RULE-SET,geosite-netflix,~openbox~aGssMQ",
		// [6,13) 内网 / NTP / 订阅节点直连 + 其余终端
		"GEOIP,LAN,DIRECT,no-resolve",
		"AND,((NETWORK,UDP),(DST-PORT,123)),DIRECT",
		"DOMAIN,node.example.com,DIRECT",
		"IP-CIDR,203.0.113.0/24,DIRECT,no-resolve",
		"IP-CIDR6,2001:db8:1::/48,DIRECT,no-resolve",
		"OPENBOX-SRC-MAC,aa:bb:cc:dd:ee:ff,DIRECT",
		"SRC-IP-CIDR,192.168.1.20/32,REJECT",
		// [13,末) 站点集 + 兜底
		"RULE-SET,geosite-cn,国内",
		"DOMAIN,a.example.com,国内",
		"DOMAIN-SUFFIX,example.cn,国内",
		"DOMAIN-KEYWORD,kw,国内",
		"RULE-SET,geoip-cn,国内,no-resolve",
		"IP-CIDR,10.9.0.0/16,国内,no-resolve",
		"IP-CIDR6,2001:db8::/32,国内,no-resolve",
		"RULE-SET,geosite-google,海外",
		"MATCH,其他",
	}
	if strings.Join(r.Rules, "\n") != strings.Join(want, "\n") {
		t.Errorf("rules:\n got\n%s\n want\n%s", strings.Join(r.Rules, "\n"), strings.Join(want, "\n"))
	}
	if r.FirstTerminal != 0 || r.EndTerminal != 1 || r.DirectTerminal != 6 || r.FirstPolicy != 13 {
		t.Errorf("sections: %+v", r)
	}
}

func TestMihomoRulesErrors(t *testing.T) {
	// 规则集没有对应的 provider:整体报错,不静默丢
	var m map[string]any
	_ = json.Unmarshal([]byte(mihomoRulesArgs), &m)
	delete(m["providers"].(map[string]any), "geoip-cn")
	if _, err := MihomoTargetRules(m); err == nil || !strings.Contains(err.Error(), "geoip-cn") {
		t.Errorf("missing provider: %v", err)
	}
	// 兜底出口不认识
	_ = json.Unmarshal([]byte(mihomoRulesArgs), &m)
	delete(m["targets"].(map[string]any), "其他")
	if _, err := MihomoTargetRules(m); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Errorf("unknown fallback: %v", err)
	}
}

func TestMihomoRulesNoCustomNoTerminals(t *testing.T) {
	r, err := MihomoTargetRules(mustJSON(t, `{"routing":{"policies":[],"fallbackDefault":"direct"},"providers":{},"ruleLists":{},"targets":{"direct":"DIRECT","其他":"其他"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.EndTerminal != 0 || r.DirectTerminal != 0 || r.FirstPolicy != 2 || r.Rules[len(r.Rules)-1] != "MATCH,其他" {
		t.Errorf("%+v", r)
	}
}

func TestEncodeTarget(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       "plain",
		"国内":          "国内",
		"a b":         "a b",
		" lead":       "~openbox~IGxlYWQ",
		"a,b":         "~openbox~YSxi",
		"f(x)":        "~openbox~Zih4KQ",
		"~openbox~xx": "~openbox~fm9wZW5ib3h-eHg",
	} {
		if got := encodeTarget(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

// 设了 OPENBOX_MIHOMO_BIN(上游发布包里打过补丁的内核)才跑:整份规则交给真实内核做 -t。
func TestMihomoRulesRealKernel(t *testing.T) {
	bin := os.Getenv("OPENBOX_MIHOMO_BIN")
	if bin == "" {
		t.Skip("OPENBOX_MIHOMO_BIN not set")
	}
	r, err := MihomoTargetRules(mustJSON(t, mihomoRulesArgs))
	if err != nil {
		t.Fatal(err)
	}
	groups := []any{}
	for _, n := range []string{"Proxy", "hk,1", "国内", "海外", "其他"} {
		groups = append(groups, map[string]any{"name": n, "type": "select", "proxies": []any{"DIRECT"}})
	}
	providers := map[string]any{
		"geosite-cn":      map[string]any{"type": "inline", "behavior": "domain", "payload": []any{"+.example.cn"}},
		"geoip-cn":        map[string]any{"type": "inline", "behavior": "ipcidr", "payload": []any{"1.0.0.0/8"}},
		"geosite-google":  map[string]any{"type": "inline", "behavior": "domain", "payload": []any{"+.google.com"}},
		"geosite-netflix": map[string]any{"type": "inline", "behavior": "domain", "payload": []any{"+.netflix.com"}},
	}
	check := func(providers map[string]any, rules []string) ([]byte, error) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "c.yaml")
		b, _ := json.Marshal(map[string]any{"proxy-groups": groups, "rule-providers": providers, "rules": rules})
		if err := os.WriteFile(cfg, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return exec.Command(bin, "-d", dir, "-f", cfg, "-t").CombinedOutput()
	}
	if out, err := check(providers, r.Rules); err != nil {
		t.Fatalf("mihomo -t: %v\n%s", err, out)
	}
	// 对照:少一个 provider,内核必须拒绝
	fewer := map[string]any{}
	for k, v := range providers {
		if k != "geoip-cn" {
			fewer[k] = v
		}
	}
	if out, err := check(fewer, r.Rules); err == nil {
		t.Fatalf("kernel accepted a missing rule set:\n%s", out)
	}
}

func TestMihomoRulesSkipsTerminalWithUnknownOutbound(t *testing.T) {
	r, err := MihomoTargetRules(mustJSON(t, `{"routing":{"policies":[],"fallbackDefault":"direct"},"providers":{},"ruleLists":{},"targets":{"direct":"DIRECT","其他":"其他"},
	  "terminalRoutes":[{"match":"ip","sources":["192.168.1.9/32"],"outbound":"gone","strict":true},{"match":"ip","sources":["192.168.1.8/32"],"outbound":"","strict":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range r.Rules {
		if strings.HasPrefix(l, "SRC-IP-CIDR") {
			t.Errorf("terminal with unknown outbound produced %s", l)
		}
	}
}
