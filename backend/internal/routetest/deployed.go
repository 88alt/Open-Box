package routetest

import (
	"errors"
	"path/filepath"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// ReadDeployedConfig 按「当前路由器选中的内核」读已部署的配置,解成 map。
//
// 为什么需要它:自研装的是双内核,sing-box 写 <ConfigDir>/config.json,mihomo 写 <ConfigDir>/mihomo.yaml。
// 在这之前,penetration、route-test 的 DNS 预测、terminal-test 的 firstLayer 都写死读 config.json,
// 于是「内核已经切到 mihomo」的设备上会读到一份几天前的旧 sing-box 配置,按它去匹配当前流量自然对不上
// (2026-11-26 真机复现:config.json 是 11-18 的,mihomo.yaml 是 11-26 的,matched.viaIp 因此永远为空)。
//
// 现在的行为:
//   - 内核是 sing-box(或 router-kernel 文件不存在 / 读不出来,与 system.ReadRouterKernel 的默认一致):
//     读 config.json,与以前完全一样;
//   - 内核是 mihomo:返回 ErrKernelConfigUnsupported。**不**退回读 config.json——那正是错的来源,
//     宁可明确报「当前内核是 mihomo,这份推算还不支持」,也不给一个基于过期配置的结论。
//
// 为什么 mihomo 还不支持:mihomo 的规则是 `DOMAIN-SUFFIX,baidu.com,国内直连` 这种字符串,
// 而 routedecide / dnsdecide 认的是 sing-box 的结构化规则(route.rules 里的 map、route.rule_set 里的登记)。
// 要支持得先写一层「mihomo 规则 → sing-box 形状」的翻译(含 rule-providers → route.rule_set 的映射),
// 属于设计取舍,见 STATUS「推断点 / 待维护者复核」。
func ReadDeployedConfig(configDir string, kernel func() (string, error)) (map[string]any, error) {
	if k, err := currentKernel(configDir, kernel); err == nil && k == system.KernelMihomo {
		return nil, ErrKernelConfigUnsupported
	}
	return ReadConfig(filepath.Join(configDir, "config.json"))
}

// ErrKernelConfigUnsupported 表示当前内核的配置格式还没被推算层支持(现在只有 mihomo)。
var ErrKernelConfigUnsupported = errors.New("当前内核是 mihomo,还没有把 mihomo 配置翻译成推算用的形状;请在内核切到 sing-box 后重试")

// currentKernel 取当前内核;kernel 为 nil 时读 <configDir>/router-kernel。
func currentKernel(configDir string, kernel func() (string, error)) (string, error) {
	if kernel != nil {
		return kernel()
	}
	return system.ReadRouterKernel(filepath.Join(configDir, "router-kernel"))
}
