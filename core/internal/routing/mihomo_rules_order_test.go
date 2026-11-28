package routing

import (
	"strings"
	"testing"
)

// 分段顺序:严格终端 → 前置自定义 → 内网/NTP/节点直连(+其余终端)→ 站点集。
//
// 这个排法本身没有上游明文(见 mihomo_rules.go 顶部的推断说明),但它是被
// **真实补丁内核接受**的:同一份 rules 交给 mihomo 1.19.34-openbox.25 `-t` 会回
// "configuration file ... test is successful"(TestMihomoRulesRealKernel)。
// 这里再钉住几个具体位置,避免以后改拼接顺序时悄悄破坏语义:
//   - 严格终端那条 SRC-IP-CIDR 必须排在所有前置自定义之前
//   - GEOIP,LAN 必须早于 NTP,也早于节点直连
//   - 站点集的 RULE-SET 必须晚于上面所有段
//   - MATCH 必须是最后一条
func TestMihomoRulesSectionOrder(t *testing.T) {
	r, err := MihomoTargetRules(mustJSON(t, mihomoRulesArgs))
	if err != nil {
		t.Fatal(err)
	}
	idx := func(sub string) int {
		for i, line := range r.Rules {
			if strings.HasPrefix(line, sub) {
				return i
			}
		}
		return -1
	}
	strict := idx("SRC-IP-CIDR,192.168.1.10/32,Proxy")
	custom := idx("DOMAIN-SUFFIX,x.com,Proxy")
	lan := idx("GEOIP,LAN,")
	ntp := idx("AND,((NETWORK,UDP),(DST-PORT,123))")
	nodeDomain := idx("DOMAIN,node.example.com,")
	policy := idx("RULE-SET,geosite-cn,")
	match := idx("MATCH,")

	if strict < 0 || custom < 0 || lan < 0 || ntp < 0 || nodeDomain < 0 || policy < 0 || match < 0 {
		t.Fatalf("有段没出现: strict=%d custom=%d lan=%d ntp=%d node=%d policy=%d match=%d",
			strict, custom, lan, ntp, nodeDomain, policy, match)
	}
	if !(strict < custom) {
		t.Errorf("严格终端应排在自定义之前: %d vs %d", strict, custom)
	}
	if !(custom < lan) {
		t.Errorf("自定义应排在「内网直连」之前: %d vs %d", custom, lan)
	}
	if !(lan < ntp) {
		t.Errorf("GEOIP,LAN 应早于 NTP: %d vs %d", lan, ntp)
	}
	if !(ntp < nodeDomain) {
		t.Errorf("NTP 应早于节点直连: %d vs %d", ntp, nodeDomain)
	}
	if !(nodeDomain < policy) {
		t.Errorf("节点直连应早于站点集: %d vs %d", nodeDomain, policy)
	}
	if match != len(r.Rules)-1 {
		t.Errorf("MATCH 必须是最后一条: %d / 共 %d", match, len(r.Rules))
	}

	// 四个下标要能把这四段切得整整齐齐
	if r.FirstTerminal != 0 || r.EndTerminal <= r.FirstTerminal {
		t.Errorf("严格终端段范围不对: %+v", r)
	}
	if r.DirectTerminal <= r.EndTerminal || r.FirstPolicy <= r.DirectTerminal || r.FirstPolicy > len(r.Rules) {
		t.Errorf("三个段的边界不单调: %+v", r)
	}
	if r.FirstPolicy != policy {
		t.Errorf("FirstPolicy 应指向第一条站点集规则: 下标 %d,实际 %d", policy, r.FirstPolicy)
	}
}
