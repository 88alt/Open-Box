package clientroutes

import (
	"reflect"
	"testing"
)

func TestBypassAndAdmitSources(t *testing.T) {
	list := []any{
		M{"bypass": true, "match": "ip", "sources": []any{"1.1.1.1", "1.1.1.1/32", "2.2.2.0/24", "bad"}},
		M{"bypass": true, "macs": []any{"AA-BB-CC-DD-EE-FF"}}, // 老档案:bypass 带 macs 算 mac
		M{"bypass": true, "enabled": false, "sources": []any{"9.9.9.9"}},
		M{"bypass": "true", "sources": []any{"8.8.8.8"}}, // 只认布尔 true
		M{"admit": true, "sources": []any{"3.3.3.3"}},
		M{"bypass": true, "sources": []any{"1.1.1.1", "4.4.4.4"}}, // 跨规则去重,保持首次出现的顺序
		M{"bypass": true, "enabled": "false", "sources": []any{"5.5.5.5"}},
		M{"admit": true, "match": "mac", "macs": []any{"11:22:33:44:55:66", "11-22-33-44-55-66"}},
		M{"bypass": true, "admit": true, "sources": []any{"6.6.6.6"}}, // 两类都标的,各算各的
		nil, "x", float64(1),
	}
	b := BypassSources(list)
	if want := (Sources{IPs: []string{"1.1.1.1/32", "2.2.2.0/24", "4.4.4.4/32", "5.5.5.5/32", "6.6.6.6/32"}, Macs: []string{"aa:bb:cc:dd:ee:ff"}}); !reflect.DeepEqual(b, want) {
		t.Errorf("BypassSources = %+v, want %+v", b, want)
	}
	a := AdmitSources(list)
	if want := (Sources{IPs: []string{"3.3.3.3/32", "6.6.6.6/32"}, Macs: []string{"11:22:33:44:55:66"}}); !reflect.DeepEqual(a, want) {
		t.Errorf("AdmitSources = %+v, want %+v", a, want)
	}
}

func TestSourcesNonArray(t *testing.T) {
	for _, in := range []any{nil, "x", M{}, float64(1)} {
		for _, s := range []Sources{BypassSources(in), AdmitSources(in)} {
			if s.IPs == nil || s.Macs == nil || len(s.IPs) != 0 || len(s.Macs) != 0 {
				t.Errorf("%v: want 非 nil 空切片, got %+v", in, s)
			}
		}
	}
}

func TestAdmitDNSDirect(t *testing.T) {
	ipAdmit := []any{M{"admit": true, "sources": []any{"3.3.3.3"}}}
	macAdmit := []any{M{"admit": true, "match": "mac", "macs": []any{"aa:bb:cc:dd:ee:ff"}}}
	noAdmit := []any{M{"bypass": true, "sources": []any{"3.3.3.3"}}}
	cases := []struct {
		name string
		list any
		opts any
		want bool
	}{
		{"缺省 autoRedirect=false", ipAdmit, nil, false},
		{"autoRedirect 开,缺省 hijack + splitDns", ipAdmit, M{"autoRedirect": true}, true},
		{"autoRedirect 为 truthy 字符串", ipAdmit, M{"autoRedirect": "yes"}, true},
		{"autoRedirect 关", ipAdmit, M{"autoRedirect": false}, false},
		{"splitDns 关", ipAdmit, M{"autoRedirect": true, "splitDns": false}, false},
		{"splitDns 为 0", ipAdmit, M{"autoRedirect": true, "splitDns": float64(0)}, false},
		{"dnsmasq 模式", ipAdmit, M{"autoRedirect": true, "dnsMode": "dnsmasq"}, true},
		{"dns 模式 off", ipAdmit, M{"autoRedirect": true, "dnsMode": "off"}, false},
		{"dns 模式 null 不走缺省", ipAdmit, M{"autoRedirect": true, "dnsMode": nil}, false},
		{"只有 mac 白名单", macAdmit, M{"autoRedirect": true}, false},
		{"没有白名单", noAdmit, M{"autoRedirect": true}, false},
		{"列表不是数组", "x", M{"autoRedirect": true}, false},
	}
	for _, c := range cases {
		if got := AdmitDNSDirect(c.list, c.opts); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNotShareInbound(t *testing.T) {
	want := M{"inbound": []any{"share-a", "share-b"}, "invert": true}
	if got := NotShareInbound([]string{"share-a", "share-b"}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func notShare(tags ...any) M { return M{"inbound": tags, "invert": true} }

func TestGuardTerminalRulesNoop(t *testing.T) {
	rules := []any{M{"source_ip_cidr": []any{"1.1.1.1/32"}, "outbound": "p"}}
	for _, tags := range []any{nil, "share-a", []any{}, []any{"", float64(1), nil}} {
		if got := GuardTerminalRules(rules, tags); !reflect.DeepEqual(got, rules) {
			t.Errorf("tags=%v 没有有效共享入站应原样返回: %#v", tags, got)
		}
	}
	for _, bad := range []any{nil, "x", M{}} {
		if got := GuardTerminalRules(bad, []any{"share-a"}); !reflect.DeepEqual(got, bad) {
			t.Errorf("rules=%v 不是数组应原样返回: %#v", bad, got)
		}
	}
}

func TestGuardTerminalRulesSimpleRule(t *testing.T) {
	tags := []any{"", "share-a", float64(2), "share-b"} // 只留非空字符串
	rules := []any{
		M{"source_ip_cidr": []any{"1.1.1.1/32"}, "outbound": "proxy"},
		M{"source_mac_address": []any{"aa:bb:cc:dd:ee:ff"}, "domain": []any{"a.com"}, "action": "route", "server": "dns-x", "rule_set": []any{"rs"}},
		M{"domain": []any{"a.com"}, "outbound": "x"}, // 没有来源条件:不动
		"not-a-map",
		nil,
	}
	want := []any{
		M{"type": "logical", "mode": "and",
			"rules":    []any{M{"source_ip_cidr": []any{"1.1.1.1/32"}}, notShare("share-a", "share-b")},
			"outbound": "proxy"},
		M{"type": "logical", "mode": "and",
			"rules": []any{M{"source_mac_address": []any{"aa:bb:cc:dd:ee:ff"}, "domain": []any{"a.com"}, "rule_set": []any{"rs"}},
				notShare("share-a", "share-b")},
			"action": "route", "server": "dns-x"},
		M{"domain": []any{"a.com"}, "outbound": "x"},
		"not-a-map",
		nil,
	}
	got := GuardTerminalRules(rules, tags)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestGuardTerminalRulesLogical(t *testing.T) {
	sub := M{"source_ip_cidr": []any{"1.1.1.1/32"}}
	other := M{"domain": []any{"a.com"}}
	andRule := M{"type": "logical", "mode": "and", "rules": []any{other, sub}, "outbound": "p"}
	orRule := M{"type": "logical", "mode": "or", "rules": []any{other, sub}}
	andNoSrc := M{"type": "logical", "mode": "and", "rules": []any{other}}
	andBad := M{"type": "logical", "mode": "and", "rules": "x"}
	rules := []any{andRule, orRule, andNoSrc, andBad}
	got := GuardTerminalRules(rules, []any{"share-a"}).([]any)
	wantAnd := M{"type": "logical", "mode": "and", "rules": []any{other, sub, notShare("share-a")}, "outbound": "p"}
	if len(got) != 4 || !reflect.DeepEqual(got[0], wantAnd) {
		t.Errorf("and 规则应追加 NotShareInbound: %#v", got[0])
	}
	for i, r := range []any{orRule, andNoSrc, andBad} {
		if !reflect.DeepEqual(got[i+1], r) {
			t.Errorf("规则 #%d 应原样: %#v", i+1, got[i+1])
		}
	}
	// 不改动入参
	if len(andRule["rules"].([]any)) != 2 {
		t.Errorf("入参的 rules 被改动: %#v", andRule["rules"])
	}
}

func TestGuardTerminalRulesCondTailSplit(t *testing.T) {
	// 条件类的键进 logical 里的那份,动作类的键留在外面,条数不变
	rules := []any{M{"source_ip_cidr": []any{"1.1.1.1/32"}, "ip_cidr": []any{"2.2.2.0/24"}, "port": float64(443), "network": "tcp", "outbound": "p", "tag": "t"}}
	got := GuardTerminalRules(rules, []any{"s"}).([]any)
	want := M{"type": "logical", "mode": "and",
		"rules":    []any{M{"source_ip_cidr": []any{"1.1.1.1/32"}, "ip_cidr": []any{"2.2.2.0/24"}, "port": float64(443), "network": "tcp"}, notShare("s")},
		"outbound": "p", "tag": "t"}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestGuardTerminalRulesInvertGoesToCondition(t *testing.T) {
	rules := []any{M{"source_ip_cidr": []any{"1.1.1.1/32"}, "invert": true, "outbound": "p"}}
	got := GuardTerminalRules(rules, []any{"s"}).([]any)
	want := M{"type": "logical", "mode": "and",
		"rules":    []any{M{"source_ip_cidr": []any{"1.1.1.1/32"}, "invert": true}, notShare("s")},
		"outbound": "p"}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestGuardTerminalRulesDoesNotWriteThroughInputSlice(t *testing.T) {
	// 入参的 rules 切片还有剩余容量时,追加也不能写进它的底层数组
	subs := make([]any, 2, 4)
	subs[0], subs[1] = M{"domain": []any{"a.com"}}, M{"source_ip_cidr": []any{"1.1.1.1/32"}}
	rule := M{"type": "logical", "mode": "and", "rules": subs}
	GuardTerminalRules([]any{rule}, []any{"s"})
	if subs[:3][2] != nil {
		t.Errorf("入参切片的底层数组被写入: %#v", subs[:3][2])
	}
}
