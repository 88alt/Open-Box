package routetest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

func TestNoDomainRule(t *testing.T) {
	// rule 的写法来自真实 sing-box 1.14.4 的 /connections(见 nodomain.go),host 为空 = 这条探测连接没有域名
	for _, c := range []struct {
		name, rule, host string
		want             bool
	}{
		{"纯 IP 规则", "ip_cidr=127.0.0.3/32 => route(viaip)", "", true},
		{"多个 IP", "ip_cidr=[127.0.0.3/32 127.0.0.9/32] => route(viaip)", "", true},
		{"IP 加端口", "ip_cidr=127.0.0.3/32 port=8443 => route(viaip)", "", true},
		{"私网 IP", "ip_is_private=true port=9999 => route(x)", "", true},
		{"逻辑与:IP 和端口", "ip_cidr=127.0.0.6/32 && port=8443 => route(viadom)", "", true},
		{"逻辑或:每支都是 IP", "ip_cidr=127.0.0.5/32 || ip_is_private=true => route(x)", "", true},
		{"没有 => 动作也认", "ip_cidr=10.0.0.0/8", "", true},
		{"连接有域名:不算", "ip_cidr=127.0.0.3/32 => route(viaip)", "other.test", false},
		{"域名规则", "domain_suffix=probe.test => route(viadom)", "", false},
		{"域名加 IP 同时要求", "domain_suffix=probe.test ip_cidr=127.0.0.7/32 => route(viaip)", "", false},
		{"逻辑或里有域名那一支", "ip_cidr=127.0.0.5/32 || domain=z.test => route(viaip)", "", false},
		{"逻辑与里有域名", "ip_cidr=127.0.0.5/32 && domain=z.test => route(x)", "", false},
		{"规则集:看不到里面是什么,不猜", "rule_set=ipset => route(viadom)", "", false},
		{"规则集加 IP", "rule_set=geoip-cn ip_cidr=1.0.0.0/8 => route(x)", "", false},
		{"兜底", "final", "", false},
		{"空", "", "", false},
		{"嵌套逻辑规则:认不出就不提示", "(ip_cidr=1.1.1.1/32 && port=443) || ip_is_private=true => route(x)", "", false},
		{"|| 和 && 混用:认不出", "ip_cidr=1.1.1.1/32 || ip_is_private=true && port=1 => route(x)", "", false},
		{"只有端口:没有 IP 条件", "port=443 => route(x)", "", false},
	} {
		if got := noDomainRule(c.rule, c.host); got != c.want {
			t.Errorf("%s: noDomainRule(%q, %q) = %v,想要 %v", c.name, c.rule, c.host, got, c.want)
		}
	}
}

func TestRuleKeys(t *testing.T) {
	got := strings.Join(ruleKeys("ip_cidr=[1.1.1.1/32 2.2.2.2/32] port=443  domain_regex=^a=b$ final"), ",")
	if got != "ip_cidr,port,domain_regex,final" {
		t.Fatalf("ruleKeys = %q", got)
	}
}

func TestRunMarksNoDomain(t *testing.T) {
	for _, c := range []struct {
		name, host string
		want       bool
	}{{"连接没有域名", "", true}, {"连接有域名", "other.test", false}} {
		t.Run(c.name, func(t *testing.T) {
			port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
			p := newFakeProxy(t, 200, up)
			s := &Service{
				ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second,
				Snapshot: snapshotFor(p, &singbox.KernelConn{
					ID: "1", Chains: []string{"直连"}, Host: c.host, DestIP: "127.0.0.3",
					Rule: "ip_cidr=127.0.0.3/32 => route(直连)",
				}, "直连"),
			}
			res, err := s.Run(context.Background(), Input{Target: "127.0.0.1", Port: port})
			if err != nil {
				t.Fatal(err)
			}
			if res.Exit.NoDomain != c.want {
				t.Fatalf("noDomain = %v,想要 %v: %+v", res.Exit.NoDomain, c.want, res.Exit)
			}
			b, _ := json.Marshal(res.Exit)
			if has := strings.Contains(string(b), `"noDomain":true`); has != c.want {
				t.Fatalf("JSON 里 noDomain 的有无不对: %s", b)
			}
		})
	}
}
