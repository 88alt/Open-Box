package routing

// 与基线的一处有意差异(2026-11-25,见 core/HANDOFF.md 第四节 3 与 backend/STATUS.md):
// 前置自定义分流里「引用了规则集、出口又不在已知出站表里」的行(节点组被删 / 停用,规则还指着它)
// 不再写进 DNS 规则。
//
// 为什么只处理这一类:连接侧(BuildRoute)本来就会跳过所有出口不认识的行——它连这些行的规则集都不进
// route.rule_set;但 DNS 侧原先会照写一条 `rule_set: geosite-xxx`(或 `list-xxx`)的解析规则,而这份
// 规则集在路由里没有声明。sing-box 1.14 起会把这种配置判为非法(「DNS 规则引用了未声明的规则集」),
// 整份配置被拒,内核直接起不来。
//
// 出口不认识、但不引用规则集的行(domain / domainSuffix / domainKeyword / ipCidr / port)不会让配置
// 变非法,DNS 侧照旧按代理侧解析,保持基线行为——这样改动面最小。
//
// 注意这里只给「跳不跳」的标记,不删行:行还在,行下标就还是档案里的下标,`dnsRuleOwners` 记的
// `index` 才仍然对得上用户看到的那一行。

// DanglingRuleSetRows 返回每个前置自定义行是否「引用规则集且出口不在已知出站表里」。
//
// 返回的切片与 rules 一一对应;known 为 nil(调用方没给出站表)或没有规则时返回 nil,表示不跳。
func DanglingRuleSetRows(rules []CustomRule, known map[string]bool, b Builtin) []bool {
	if known == nil || len(rules) == 0 {
		return nil
	}
	skip := make([]bool, len(rules))
	any := false
	for i, r := range rules {
		if customRuleTag(r) != "" && !known[customOutboundTag(r, b)] {
			skip[i] = true
			any = true
		}
	}
	if !any {
		return nil
	}
	return skip
}
