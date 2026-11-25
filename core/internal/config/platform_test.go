package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 手写覆盖官方 v0.1.353 的两处行为(金标准里 platform / rulesetDir 的取值有限,边界在这里补):
//  1. platform:"container":dns-in 后面多一个监听 0.0.0.0:53 的 dns-in-lan,route.rules[0] 是它的 hijack-dns;
//  2. rulesetDir 为 null 时当空字符串,.srs 路径不会拼出字面的 "null"。
func baseArgs(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/config_golden.json")
	if err != nil {
		t.Skip("没有金标准文件:", err)
	}
	var g struct {
		Cases []buildCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.Cases {
		if c.Error != "" {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal(c.Args, &args); err != nil {
			t.Fatal(err)
		}
		return args
	}
	t.Fatal("金标准里没有可用的用例")
	return nil
}

func buildConfig(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	out, err := BuildDetailed(args)
	if err != nil {
		t.Fatal(err)
	}
	return toAny(out["config"]).(map[string]any)
}

func inboundTags(cfg map[string]any) []string {
	tags := []string{}
	for _, i := range asList(cfg["inbounds"]) {
		tags = append(tags, i.(map[string]any)["tag"].(string))
	}
	return tags
}

func TestPlatformContainerDNSLan(t *testing.T) {
	args := baseArgs(t)
	for _, plat := range []any{nil, "openwrt", "systemd"} {
		if plat == nil {
			delete(args, "platform")
		} else {
			args["platform"] = plat
		}
		cfg := buildConfig(t, args)
		for _, tag := range inboundTags(cfg) {
			if tag == DNSLanInboundTag {
				t.Fatalf("platform=%v 不该有 %s", plat, DNSLanInboundTag)
			}
		}
		first := asList(cfg["route"].(map[string]any)["rules"])[0].(map[string]any)
		if first["action"] == "hijack-dns" && len(asList(first["inbound"])) == 1 && asList(first["inbound"])[0] == DNSLanInboundTag {
			t.Fatalf("platform=%v 不该有 %s 的劫持规则", plat, DNSLanInboundTag)
		}
	}
	args["platform"] = "container"
	cfg := buildConfig(t, args)
	tags := inboundTags(cfg)
	at := -1
	for i, tag := range tags {
		if tag == "dns-in" {
			at = i
		}
	}
	if at < 0 || at+1 >= len(tags) || tags[at+1] != DNSLanInboundTag {
		t.Fatalf("dns-in-lan 应紧跟在 dns-in 后面: %v", tags)
	}
	lan := asList(cfg["inbounds"])[at+1].(map[string]any)
	if lan["type"] != "direct" || lan["listen"] != "0.0.0.0" || lan["listen_port"] != float64(53) {
		t.Fatalf("dns-in-lan 入站不对: %v", lan)
	}
	first := asList(cfg["route"].(map[string]any)["rules"])[0].(map[string]any)
	in := asList(first["inbound"])
	if len(first) != 2 || first["action"] != "hijack-dns" || len(in) != 1 || in[0] != DNSLanInboundTag {
		t.Fatalf("route.rules[0] 应是 dns-in-lan 的 hijack-dns: %v", first)
	}
}

// 上游 v0.1.485(DNS 页「监听 53 端口」):systemd 部署且档案 dns.listen53 === true 时,和容器一样多一个
// 0.0.0.0:53 的 dns-in-lan 及它的 hijack-dns;没开、值不是布尔 true、或不是 systemd(openwrt 的 53 归 dnsmasq)都不该有。
func TestPlatformSystemdListen53(t *testing.T) {
	cases := []struct {
		name     string
		platform any
		listen53 any
		want     bool
	}{
		{"systemd 开", "systemd", true, true},
		{"systemd 关", "systemd", false, false},
		{"systemd 没写", "systemd", nil, false},
		{"systemd 写成字符串", "systemd", "true", false},
		{"openwrt 开也不监听", "openwrt", true, false},
		{"没有 platform 开也不监听", nil, true, false},
		{"container 没写也监听(老行为)", "container", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := baseArgs(t)
			if c.platform == nil {
				delete(args, "platform")
			} else {
				args["platform"] = c.platform
			}
			profile, _ := args["profile"].(map[string]any)
			if profile == nil {
				profile = map[string]any{}
				args["profile"] = profile
			}
			dns, _ := profile["dns"].(map[string]any)
			if dns == nil {
				dns = map[string]any{}
				profile["dns"] = dns
			}
			delete(dns, "listen53")
			if c.listen53 != nil {
				dns["listen53"] = c.listen53
			}
			cfg := buildConfig(t, args)
			got := false
			for _, tag := range inboundTags(cfg) {
				if tag == DNSLanInboundTag {
					got = true
				}
			}
			if got != c.want {
				t.Fatalf("dns-in-lan 存在 = %v,想要 %v", got, c.want)
			}
			first := asList(cfg["route"].(map[string]any)["rules"])[0].(map[string]any)
			hijack := first["action"] == "hijack-dns" && len(asList(first["inbound"])) == 1 && asList(first["inbound"])[0] == DNSLanInboundTag
			if hijack != c.want {
				t.Fatalf("route.rules[0] 是 dns-in-lan 的 hijack-dns = %v,想要 %v", hijack, c.want)
			}
		})
	}
}

func TestRulesetDirNullIsEmpty(t *testing.T) {
	args := baseArgs(t)
	args["rulesetDir"] = nil
	cfg := buildConfig(t, args)
	n := 0
	for _, e := range asList(cfg["route"].(map[string]any)["rule_set"]) {
		rs := e.(map[string]any)
		p, _ := rs["path"].(string)
		if strings.Contains(p, "null") {
			t.Fatalf("rulesetDir=null 不该拼出字面的 null: %q", p)
		}
		if rs["format"] == "binary" && strings.HasPrefix(p, "/") && strings.HasSuffix(p, ".srs") && strings.Count(p, "/") == 1 {
			n++
		}
	}
	if n == 0 {
		t.Skip("这个用例的 route 里没有直接拼 rulesetDir 的 .srs 规则集")
	}
}
