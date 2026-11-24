package routing

import (
	"reflect"
	"testing"
)

// DanglingRuleSetRows 的自写向量:只有「引用规则集 + 出口不在已知出站表里」的行被标记;
// 域名类行、出口认识的行、内置直连 / 拒绝行都不标;表为 nil 或没有规则时返回 nil(不跳)。
// 装配层用它让 DNS 侧和连接侧口径一致(详见 customrules.go 与 core/HANDOFF.md 第四节 3)。
func TestDanglingRuleSetRows(t *testing.T) {
	b := defaultBuiltin
	known := map[string]bool{"direct": true, "block": true, "美国": true}
	rows := []CustomRule{
		{Type: "geosite", Value: "netflix", Outbound: "美国"},            // 0 出口认识 → 不跳
		{Type: "geosite", Value: "youtube", Outbound: "已删的组"},          // 1 引用规则集 + 出口不认识 → 跳
		{Type: "ruleset", Value: "list-b99d2d1f", Outbound: "已删的组"},    // 2 同上 → 跳
		{Type: "geoip", Value: "cn", Outbound: "已删的组"},                 // 3 引用规则集(路由侧用)→ 跳
		{Type: "domain", Value: "a.example", Outbound: "已删的组"},         // 4 不引用规则集 → 不跳
		{Type: "domainSuffix", Value: "b.example", Outbound: "direct"}, // 5 内置直连 → 不跳
		{Type: "port", Value: "443", Outbound: "已删的组"},                 // 6 不引用规则集 → 不跳
	}
	want := []bool{false, true, true, true, false, false, false}
	if got := DanglingRuleSetRows(rows, known, b); !reflect.DeepEqual(got, want) {
		t.Fatalf("DanglingRuleSetRows = %v,想要 %v", got, want)
	}

	if got := DanglingRuleSetRows(rows, nil, b); got != nil {
		t.Errorf("表为 nil 时该返回 nil(不跳),得到 %v", got)
	}
	if got := DanglingRuleSetRows(nil, known, b); got != nil {
		t.Errorf("没有规则时该返回 nil,得到 %v", got)
	}
	// 一行都不悬空时也返回 nil,调用方据此省掉一份切片。
	clean := []CustomRule{{Type: "geosite", Value: "netflix", Outbound: "美国"}}
	if got := DanglingRuleSetRows(clean, known, b); got != nil {
		t.Errorf("没有悬空行时该返回 nil,得到 %v", got)
	}
}
