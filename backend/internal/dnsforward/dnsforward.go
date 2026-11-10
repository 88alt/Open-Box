// Package dnsforward 回答 terminal-test 的 dnsForward:LAN 里的终端问 dnsmasq 域名时,dnsmasq 是把查询
// 交给内核 DNS,还是自己直接问上游。自己的设计,依据前端 OpenboxTerminalTest.dnsForward 的字段和注释
// (routeTermDnsForward* 三句文案),没有参照上游。
//
// 只说有把握的:本后端自己做的 dnsmasq 接管(system.DNSTakeover,档案 dns.mode = dnsmasq)是把「全部」查询
// 交给内核 DNS 入站,没有按域名分的转发清单,所以接管生效时回 {forward: kernel, plan: all, to: 内核 DNS 地址}。
// 其它情况(模式是 off / hijack、接管没生效、读不到档案)dnsmasq 的实际配置不归我们管,可能有用户自己的转发清单,
// 不猜,返回 nil,响应里就没有 dnsForward 字段(DESIGN 的「变更安全」同一条:拿不准就如实说拿不准)。
package dnsforward

import "context"

// Result 对应前端 dnsForward 里的 forward / plan / to(suffix 只在按清单转发时才有,这里不会出现)。
type Result struct {
	Forward string // "kernel"
	Plan    string // "all"
	To      string // 内核 DNS 入站的地址,例如 127.0.0.1:7853
}

// Service 读档案模式和接管现状。零值字段都当作「不知道」。
type Service struct {
	// Mode 返回档案的 dns.mode(dnssync.ModeFromProfile 的结果);读不到返回 error。
	Mode func(ctx context.Context) (string, error)
	// Applied 报告 dnsmasq 接管文件是否在、内容对(system.DNSTakeover.Status 的 Applied)。
	Applied func(ctx context.Context) bool
	// KernelAddr 是内核 DNS 入站的地址,接管文件里转发到它。
	KernelAddr string
}

// Describe 返回此刻的 dnsmasq 转发说明;不确定返回 nil。
func (s *Service) Describe(ctx context.Context) *Result {
	if s == nil || s.Mode == nil || s.Applied == nil {
		return nil
	}
	mode, err := s.Mode(ctx)
	if err != nil || mode != "dnsmasq" {
		return nil
	}
	if !s.Applied(ctx) {
		return nil
	}
	return &Result{Forward: "kernel", Plan: "all", To: s.KernelAddr}
}
