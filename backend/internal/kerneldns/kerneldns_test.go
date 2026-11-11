package kerneldns

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// 下面的样例行都是真实 sing-box 1.13.14 打出来的(用 Clash API /logs?level=debug 抓的,只把端口、编号保持原样)。
func ents(lines ...string) []singbox.LogEntry {
	var out []singbox.LogEntry
	for _, l := range lines {
		lv, p, _ := strings.Cut(l, "|")
		out = append(out, singbox.LogEntry{Level: lv, Payload: p})
	}
	return out
}

func cfgJSON(t *testing.T, s string) *Config {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return ConfigFrom(m)
}

const testCfg = `{"dns":{"servers":[
 {"type":"hosts","tag":"ha"},{"type":"udp","tag":"up","server":"127.0.0.1","server_port":15353,"detour":"d2"},
 {"type":"tcp","tag":"bad","server":"127.0.0.1","server_port":9},{"type":"fakeip","tag":"fk"},
 {"type":"udp","tag":"dns-rewrite","server":"127.0.0.1","server_port":7854}],
 "rules":[{"domain_suffix":["up.test"]},{"domain_suffix":["bad.test"]},{"domain_suffix":["fk.test"]},{"domain":["rej.test"]}],"final":"ha"},
 "outbounds":[{"type":"direct","tag":"direct"},{"type":"direct","tag":"d2"},{"type":"vless","tag":"节点A"}]}`

func get(t *testing.T, m map[string]any, k string) any {
	t.Helper()
	v, ok := m[k]
	if !ok {
		t.Fatalf("缺字段 %s:%v", k, m)
	}
	return v
}

func TestAnalyzeExchangedViaUDPServer(t *testing.T) {
	r := Analyze(ents(
		"info|[2184370699 0ms] inbound/direct[dns-in]: inbound packet connection from 127.0.0.1:45378",
		"debug|[2184370699 0ms] router: match[0] inbound=dns-in => hijack-dns",
		"debug|[2184370699 0ms] dns: exchange x.up.test. IN A",
		"debug|[2184370699 0ms] dns: match[1] domain_suffix=up.test => route(up)",
		"info|[2184370699 0ms] outbound/direct[d2]: outbound packet connection to 127.0.0.1:15353",
		"debug|[2184370699 12ms] dns: exchanged x.up.test NOERROR 60",
		"info|[2184370699 12ms] dns: exchanged A x.up.test. 60 IN A 198.51.100.5",
	), "x.up.test", cfgJSON(t, testCfg))
	if !r.Seen {
		t.Fatalf("%+v", r)
	}
	m := r.Map()
	srv := get(t, m, "server").(map[string]any)
	if get(t, m, "source") != "127.0.0.1:45378" || get(t, m, "ruleIndex") != 0 || get(t, m, "ruleText") != "domain_suffix=up.test" ||
		get(t, m, "action") != "route(up)" || srv["tag"] != "up" || srv["type"] != "udp" || srv["server"] != "127.0.0.1" || srv["port"] != 15353 || srv["detour"] != "d2" ||
		get(t, m, "result") != "exchanged" || get(t, m, "rcode") != "NOERROR" || get(t, m, "ttl") != 60 || get(t, m, "ms") != 12 ||
		get(t, m, "outbound") != "d2" || get(t, m, "viaProxy") != false || get(t, m, "fakeIp") != false || get(t, m, "fakeIpLocal") != false || get(t, m, "rewrite") != false {
		t.Fatalf("%v", m)
	}
	if a := get(t, m, "answers").([]string); len(a) != 1 || a[0] != "198.51.100.5" {
		t.Fatalf("%v", m)
	}
	if c := get(t, m, "chain").([]string); len(c) != 0 {
		t.Fatalf("没走代理 chain 为空:%v", c)
	}
}

func TestAnalyzeViaProxyNodeFromDialLine(t *testing.T) {
	cfg := cfgJSON(t, strings.Replace(testCfg, `"detour":"d2"`, `"detour":"代理组"`, 1))
	r := Analyze(ents(
		"debug|[9 0ms] dns: exchange x.up.test. IN A",
		"debug|[9 0ms] dns: match[1] domain_suffix=up.test => route(up)",
		"info|[9 3ms] outbound/vless[节点A]: outbound packet connection to 1.1.1.1:53",
		"debug|[9 80ms] dns: exchanged x.up.test NOERROR 300",
	), "x.up.test", cfg)
	m := r.Map()
	if get(t, m, "viaProxy") != true || get(t, m, "outbound") != "节点A" || get(t, m, "ms") != 80 {
		t.Fatalf("%v", m)
	}
	if c := get(t, m, "chain").([]string); len(c) != 1 || c[0] != "代理组" {
		t.Fatalf("chain 先给 detour:%v", c)
	}
	// 没有拨号行时按配置:detour 不是直连类型出站就算走代理
	r = Analyze(ents("debug|[9 0ms] dns: exchange x.up.test. IN A", "debug|[9 0ms] dns: match[1] domain_suffix=up.test => route(up)"), "x.up.test", cfg)
	if r.Map()["viaProxy"] != true {
		t.Fatalf("%v", r.Map())
	}
}

func TestAnalyzeCachedFailedFakeipActionFinal(t *testing.T) {
	cfg := cfgJSON(t, testCfg)
	t.Run("缓存命中", func(t *testing.T) {
		m := Analyze(ents(
			"debug|[2837742837 0ms] dns: exchange x.up.test. IN A",
			"debug|[2837742837 0ms] dns: match[1] domain_suffix=up.test => route(up)",
			"debug|[2837742837 0ms] dns: cached x.up.test NOERROR 59",
			"info|[2837742837 0ms] dns: cached A x.up.test. 59 IN A 198.51.100.5",
		), "X.UP.TEST.", cfg).Map() // 域名大小写、结尾的点不影响
		if m["result"] != "cached" || m["ttl"] != 59 || m["answers"].([]string)[0] != "198.51.100.5" {
			t.Fatalf("%v", m)
		}
	})
	t.Run("上游失败", func(t *testing.T) {
		m := Analyze(ents(
			"debug|[133733194 0ms] dns: exchange x.bad.test. IN A",
			"debug|[133733194 0ms] dns: match[3] domain_suffix=bad.test => route(bad)",
			"error|[133733194 1ms] dns: exchange failed for x.bad.test. IN A: dial TCP connection: dial tcp 127.0.0.1:9: connect: connection refused",
			"error|[133733194 1ms] router: process DNS packet: dial TCP connection: dial tcp 127.0.0.1:9: connect: connection refused",
		), "x.bad.test", cfg).Map()
		if m["result"] != "failed" || m["ruleIndex"] != 1 || !strings.Contains(m["error"].(string), "connection refused") || m["ms"] != 1 {
			t.Fatalf("%v", m)
		}
	})
	t.Run("FakeIP 服务器", func(t *testing.T) {
		m := Analyze(ents(
			"debug|[1308172024 0ms] dns: exchange x.fk.test. IN A",
			"debug|[1308172024 0ms] dns: match[5] domain_suffix=fk.test => route(fk)",
			"debug|[1308172024 0ms] dns: exchanged x.fk.test NOERROR 600",
			"info|[1308172024 0ms] dns: exchanged A x.fk.test. 600 IN A 198.18.0.2",
		), "x.fk.test", cfg).Map()
		if m["fakeIpLocal"] != true || m["fakeIp"] != true || m["ruleIndex"] != 2 {
			t.Fatalf("%v", m)
		}
	})
	t.Run("上游回的是占位地址(不是本机 FakeIP 发的)", func(t *testing.T) {
		m := Analyze(ents(
			"debug|[7 0ms] dns: exchange x.up.test. IN A", "debug|[7 0ms] dns: match[1] domain_suffix=up.test => route(up)",
			"debug|[7 9ms] dns: exchanged x.up.test NOERROR 1", "info|[7 9ms] dns: exchanged A x.up.test. 1 IN A 198.18.3.4",
		), "x.up.test", cfg).Map()
		if m["fakeIp"] != true || m["fakeIpLocal"] != false {
			t.Fatalf("%v", m)
		}
	})
	t.Run("规则直接应答:reject 和 predefined 没有 exchanged 行", func(t *testing.T) {
		m := Analyze(ents(
			"debug|[2157535799 0ms] dns: exchange rej.test. IN A",
			"debug|[2157535799 0ms] dns: match[7] domain=rej.test => reject",
		), "rej.test", cfg).Map()
		if m["result"] != "action" || m["action"] != "reject" || m["ruleIndex"] != 3 || m["server"] != nil {
			t.Fatalf("%v", m)
		}
		m = Analyze(ents(
			"debug|[3932801698 0ms] dns: exchange pre.test. IN A",
			"debug|[3932801698 0ms] dns: match[5] domain=pre.test => predefined(NXDOMAIN)",
		), "pre.test", cfg).Map()
		if m["result"] != "action" || m["rcode"] != "NXDOMAIN" || m["action"] != "predefined(NXDOMAIN)" {
			t.Fatalf("%v", m)
		}
	})
	t.Run("没命中任何规则:走兜底,ruleIndex 为空", func(t *testing.T) {
		m := Analyze(ents(
			"debug|[2322162319 0ms] dns: exchange nomatch.test. IN A",
			"debug|[2322162319 0ms] dns: exchanged nomatch.test NXDOMAIN 0",
		), "nomatch.test", cfg).Map()
		srv := m["server"].(map[string]any)
		if m["ruleIndex"] != nil || srv["tag"] != "ha" || m["rcode"] != "NXDOMAIN" || m["result"] != "exchanged" || srv["port"] != nil {
			t.Fatalf("%v", m)
		}
	})
	t.Run("只收到查询、没等到应答", func(t *testing.T) {
		m := Analyze(ents("debug|[5 0ms] dns: exchange x.up.test. IN A", "debug|[5 0ms] dns: match[1] domain_suffix=up.test => route(up)"), "x.up.test", cfg).Map()
		if m["result"] != "pending" || m["ttl"] != nil || m["ms"] != nil {
			t.Fatalf("%v", m)
		}
	})
	t.Run("交给 DNS 重写服务", func(t *testing.T) {
		m := Analyze(ents("debug|[5 0ms] dns: exchange x.up.test. IN A", "debug|[5 0ms] dns: match[1] domain_suffix=up.test => route(dns-rewrite)",
			"debug|[5 1ms] dns: exchanged x.up.test NOERROR 5"), "x.up.test", cfg).Map()
		if m["rewrite"] != true {
			t.Fatalf("%v", m)
		}
	})
}

func TestAnalyzeNotSeenAndUnrecognized(t *testing.T) {
	cfg := cfgJSON(t, testCfg)
	other := ents("debug|[1 0ms] dns: exchange other.test. IN A", "debug|[1 0ms] dns: exchanged other.test NOERROR 1")
	if r := Analyze(other, "x.up.test", cfg); r.Seen || r.Reason != "not-seen" {
		t.Fatalf("没有这个域名的查询:%+v", r)
	}
	if r := Analyze(nil, "x.up.test", cfg); r.Seen || r.Reason != "not-seen" {
		t.Fatalf("没收到任何日志:%+v", r)
	}
	// 规则序号是偶数 / 超出规则条数:认不出,不猜
	for _, n := range []string{"2", "99"} {
		r := Analyze(ents("debug|[1 0ms] dns: exchange x.up.test. IN A", "debug|[1 0ms] dns: match["+n+"] domain_suffix=up.test => route(up)"), "x.up.test", cfg)
		if r.Seen || r.Reason != "no-log" || !strings.Contains(r.Err, "规则序号 "+n) {
			t.Fatalf("序号 %s:%+v", n, r)
		}
		if m := r.Map(); m["seen"] != false || m["reason"] != "no-log" || m["error"] == "" {
			t.Fatalf("%v", m)
		}
	}
	// 不是日志格式的行、别的标签的同名文字不干扰
	junk := ents("debug|garbage", "info|[1 0ms] inbound/direct[x]: exchange x.up.test. IN A")
	if r := Analyze(junk, "x.up.test", cfg); r.Seen {
		t.Fatalf("%+v", r)
	}
}

func TestAnalyzePicksLastQueryAndV6(t *testing.T) {
	cfg := cfgJSON(t, testCfg)
	r := Analyze(ents(
		"debug|[1 0ms] dns: exchange x.up.test. IN A", "debug|[1 0ms] dns: match[1] domain_suffix=up.test => route(up)", "debug|[1 5ms] dns: exchanged x.up.test SERVFAIL 0",
		"debug|[2 0ms] dns: exchange x.up.test. IN A", "debug|[2 0ms] dns: match[1] domain_suffix=up.test => route(up)", "debug|[2 7ms] dns: exchanged x.up.test NOERROR 60",
		"info|[2 7ms] dns: exchanged A x.up.test. 60 IN A 198.51.100.5",
		"debug|[3 0ms] dns: exchange x.up.test. IN AAAA", "debug|[3 0ms] dns: match[1] domain_suffix=up.test => route(up)", "debug|[3 2ms] dns: exchanged x.up.test NOERROR 60",
		"info|[3 2ms] dns: exchanged AAAA x.up.test. 60 IN AAAA 2001:db8::5",
	), "x.up.test", cfg)
	m := r.Map()
	v6 := m["v6"].(map[string]any)
	if m["rcode"] != "NOERROR" || m["ms"] != 7 || v6["result"] != "exchanged" || v6["answers"].([]string)[0] != "2001:db8::5" || v6["ms"] != 2 {
		t.Fatalf("%v", m)
	}
	// 只有 AAAA:以 AAAA 为主,没有 v6
	m = Analyze(ents("debug|[3 0ms] dns: exchange x.up.test. IN AAAA", "debug|[3 2ms] dns: exchanged x.up.test NOERROR 60"), "x.up.test", cfg).Map()
	if m["seen"] != true || m["v6"] != nil {
		t.Fatalf("%v", m)
	}
}

func TestDurMS(t *testing.T) {
	for in, want := range map[string]int{"0ms": 0, "12ms": 12, "1.50s": 1500, "3.0s": 3000} {
		if got := durMS(in); got == nil || *got != want {
			t.Errorf("%q → %v,要 %d", in, got, want)
		}
	}
	for _, in := range []string{"", "2m3s", "abc"} {
		if durMS(in) != nil {
			t.Errorf("%q 认不出要返回 nil", in)
		}
	}
}

func TestConfigFrom(t *testing.T) {
	c := cfgJSON(t, testCfg)
	if c.RuleCount != 4 || c.Final != "ha" || c.Servers["up"].Port != 15353 || c.Servers["up"].Detour != "d2" || c.Outbounds["节点A"] != "vless" {
		t.Fatalf("%+v", c)
	}
	// 没写 final:用第一台
	c = cfgJSON(t, `{"dns":{"servers":[{"tag":"first","type":"udp"},{"tag":"second","type":"udp"}]}}`)
	if c.Final != "first" || c.RuleCount != 0 {
		t.Fatalf("%+v", c)
	}
}
