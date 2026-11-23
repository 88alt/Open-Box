package mihomo

import (
	"fmt"

	"github.com/timeflysoon/Open-Box/core/internal/config"
	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// 第四块:mihomoCompatibility(切到 Mihomo 之前,档案里有没有 Mihomo 表达不了的东西)。
//
// 入参 {profile, nodes, groups, chainNodes, selections}(与 mihomoGroups 相同),返回字符串数组 problems
// (没有问题是 [],不是 null);上游 Node 把它和规则集缺失的提示合在一起展示,非空就判「内核专用设置兼容性」未通过。
//
// 检查内容只有两类,都是 core 里实际走一遍就能确定的事实:
//  1. 每个节点(含通过核对的链式节点)能不能翻成 Mihomo proxies,翻不了的逐个列出(mihomoProxies 是遇到第一个就整体报错,
//     这里要一次列全,方便用户一次改完)。
//  2. 去掉翻不了的节点之后,节点组 / 站点集能不能生成 Mihomo 的组(mihomoGroups 的全部校验);失败原因原样带出。
//
// 有意不查的:TUN 的 tcpMss(由入口 nft 规则实现,与内核无关)、共享网络的入站协议(Mihomo 的 shadowsocks / vless /
// tuic / hysteria2 / mixed 监听都有)、DNS 协议(udp / tcp / tls / https 都有)。这几项对着补丁版 Mihomo 1.19.34 的源码核过。
// 上游这个方法的实现在闭源 core 里,看不到,检查范围是自研按「能不能生成」定的,不保证和上游逐条一致。

// Compatibility 对应 core 方法 mihomoCompatibility。
func Compatibility(args any) []string {
	problems := []string{}
	nodes, _ := jsx.Get(args, "nodes").([]any)
	userGroups := jsx.Get(args, "groups")
	if !jsx.Truthy(userGroups) {
		userGroups = []any{}
	}
	chainRaw := jsx.Get(args, "chainNodes")

	check := func(n any) bool {
		if _, err := proxyOf(n); err != nil {
			tag := jsx.ToString(jsx.Get(n, "tag"))
			problems = append(problems, fmt.Sprintf("节点「%s」Mihomo 不支持:%s", tag, err.Error()))
			return false
		}
		return true
	}

	okNodes := []any{}
	for _, n := range nodes {
		if check(n) {
			okNodes = append(okNodes, n)
		}
	}

	// 链式节点:先过和生成配置时相同的核对(它会把节点组整套生成一遍,生成不了就在这里报错,
	// 这时节点组的问题已经说清楚了,不再往下重复跑一遍),通过的再逐个翻。
	chain, err := config.ResolveChainNodes(chainRaw, nodes, userGroups)
	if err != nil {
		problems = append(problems, "节点组:"+err.Error())
		return problems
	}
	resolved := map[string]bool{}
	for _, c := range chain {
		resolved[jsx.ToString(jsx.Get(c, "tag"))] = true
	}
	okChain := []any{}
	if l, ok := chainRaw.([]any); ok {
		for _, c := range l {
			if resolved[jsx.ToString(jsx.Get(c, "tag"))] && check(c) {
				okChain = append(okChain, c)
			}
		}
	}

	// 去掉翻不了的节点之后,节点组 / 站点集还要能生成 Mihomo 的组。
	if _, err := Groups(map[string]any{
		"profile":    jsx.Get(args, "profile"),
		"nodes":      okNodes,
		"groups":     userGroups,
		"chainNodes": okChain,
		"selections": jsx.Get(args, "selections"),
	}); err != nil {
		problems = append(problems, "节点组:"+err.Error())
	}
	return problems
}
