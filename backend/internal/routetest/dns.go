package routetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

const maxConfigBytes = 16 << 20

// DNSPredictor 读已部署的配置,按它的 DNS 规则预测一个域名的 A 查询走向(见 dnsdecide)。
// 只读配置文件和规则集文件,不碰内核、不联网;内核的代理列表只用来补「代理侧此刻实际经过哪条线路」,读不到就不补。
type DNSPredictor struct {
	ConfigFile string // 已部署的 config.json
	SingBox    string // 解二进制规则集用的 sing-box
	TmpDir     string
	// Proxies 读内核的代理列表(节点组当前选中的节点);nil 或返回 error = 不补运行时线路。
	Proxies func(ctx context.Context) ([]singbox.KernelProxy, error)

	sets dnsdecide.FileSets // 只用它的解码缓存
}

// maxChain 是跟着「当前选中」往下走的最大层数,防止配置写出环。
const maxChain = 8

// Predict 返回 domain 的预测。读不到或解不开当前配置时返回 error(调用方写进 dns.error)。
func (p *DNSPredictor) Predict(ctx context.Context, domain string) (*dnsdecide.Decision, error) {
	f, err := os.Open(p.ConfigFile)
	if err != nil {
		return nil, errors.New("读不到当前配置(还没有部署过?)")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(b) > maxConfigBytes {
		return nil, errors.New("读不全当前配置")
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, errors.New("当前配置不是合法 JSON")
	}
	v, err := dnsdecide.FromConfig(cfg)
	if err != nil {
		return nil, err
	}
	p.sets.SingBox, p.sets.TmpDir = p.SingBox, p.TmpDir
	d := dnsdecide.Decide(ctx, v, p.sets.Bind(dnsdecide.SetDefsFromConfig(cfg)), domain, "A")
	if d.Server != nil && d.Server.Detour != "" && p.Proxies != nil {
		if proxies, err := p.Proxies(ctx); err == nil {
			p.fillRuntime(d, v, proxies)
		}
	}
	return d, nil
}

// fillRuntime 从 detour 出发,沿着内核此刻「当前选中」的节点一路走到末尾,得到实际线路;
// 末尾是直连出站时,这台 DNS 服务器此刻其实没走代理。
func (p *DNSPredictor) fillRuntime(d *dnsdecide.Decision, v *dnsdecide.View, proxies []singbox.KernelProxy) {
	now := make(map[string]string, len(proxies))
	known := make(map[string]bool, len(proxies))
	for _, x := range proxies {
		now[x.Name], known[x.Name] = x.Now, true
	}
	name := d.Server.Detour
	if !known[name] {
		return // 内核里没有这个出站:不知道,不填
	}
	chain := []string{name}
	for i := 0; i < maxChain; i++ {
		next := now[name]
		if next == "" || !known[next] {
			if next != "" {
				chain = append(chain, next) // 选中的是内核代理列表里没有的叶子(内置直连等)
			}
			break
		}
		chain = append(chain, next)
		name = next
	}
	d.RuntimeChain, d.RuntimeLeaf = chain, chain[len(chain)-1]
	if v.Types[d.RuntimeLeaf] == "direct" {
		via := false
		d.ViaProxy = &via
	}
}

// cachedMaxMS:代理侧解析不超过这么多毫秒就当是内核缓存里的答案——经线路去问至少要一个来回,不会这么快。
// 是经验阈值,没有在真机上标定过。
const cachedMaxMS = 5

// annotateResolve 按 DNS 判定给 resolve 补两项(判定没有、或算不出服务器是否走代理时都不补,不猜):
//   - fakeIpFrom:答案是占位地址、又不是内核自己发的,而这台服务器走代理,就是 detour 此刻落到的那个节点截下查询并应答的;
//   - cached:服务器走代理、拿到真实地址、几毫秒就回来了(每次探测前会先清内核的 DNS 缓存,所以多半是清缓存没成)。
func annotateResolve(r *Resolve, dns any) {
	d, ok := dns.(*dnsdecide.Decision)
	if r == nil || !ok || d.ViaProxy == nil || !*d.ViaProxy {
		return
	}
	switch {
	case r.FakeIP && !r.FakeIPLocal:
		r.FakeIPFrom = d.RuntimeLeaf
	case r.OK && !r.FakeIP && r.MS <= cachedMaxMS:
		r.Cached = true
	}
}

// predictDNS 给响应里的 dns:IP 目标不需要 DNS 判定;域名目标按预测器算,没接预测器或算不出就如实写 error。
func (s *Service) predictDNS(ctx context.Context, target string) any {
	if isIP(target) {
		return map[string]any{"skipped": true}
	}
	if s.DNS == nil {
		return map[string]any{"error": "DNS 判定没有接入"}
	}
	d, err := s.DNS.Predict(ctx, target)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("DNS 判定失败: %v", err)}
	}
	return d
}
