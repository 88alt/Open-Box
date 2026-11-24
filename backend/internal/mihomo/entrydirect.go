package mihomo

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
)

// 「直连 IP 段」提供者(双内核 3e-4d;对应上游 v0.1.462 Node 的 SC,缓存 lg)。
//
// 入口旁路放行的网段有三处来源:入口旁路计划里每个站点集扣完地址后剩下的网段(3e-4b-1 的 ii)、
// 直连 IP 计划(3e-4b-2 的 ai),两处合起来再扣掉 Mihomo 自己的两个假 IP 池(198.18.0.0/16、
// 2001:2:0:8000::/49 与 198.19.0.0/16、2001:2::/49)。结果是:
//   - 入口是白名单模式(scoped,有终端要按来源分辨):网段交给入口 nft 直接用(DirectIPCidrs);
//   - 否则:网段写成 provider(ob-entry-direct,ipcidr 行为),给规则表里的 IP-CIDR 用。
//
// 两边都会额外给出「兜底 IPv4 直连」要放行的网段(上游 Nu,即 FallbackKeepIPv4)。

// ProviderTag 是这张提供者的名字(上游字面量)。
const entryDirectTag = "ob-entry-direct"

// entryDirectReserved 是生成直连 IP 段时要扣掉的地址:先扣 Mihomo 的两个假 IP 池,再扣 benchmark 旁边那段。
// 顺序照上游 `[...ec, ...tc]`(合并后其实不分先后,写清楚是为了对照)。
func entryDirectReserved() []string {
	out := make([]string, 0, len(benchmarkSiblings)+len(fakeIPPools))
	out = append(out, benchmarkSiblings...)
	out = append(out, fakeIPPools...)
	return out
}

// EntryDirectDeps 是 BuildEntryDirect 要的外部东西。
type EntryDirectDeps struct {
	FS FS
	// RulesDir 是 provider 落盘目录(<DataDir>/mihomo/rules)。
	RulesDir string
	// MihomoDir 是 Mihomo 的 -d 目录(<DataDir>/mihomo),读已部署 provider 用。
	MihomoDir string
	// Bypass 是入口旁路的入参(形状 / 用户规则集 / .srs 路径 / FS);Shape 会被这里套一层缓存。
	Bypass EntryBypassDeps
	// Deployed 是已部署配置里的 rule-providers(tag → provider),用来直接读回 geoip 的网段。
	Deployed map[string]Provider
}

// EntryDirect 是 SC 的结果。Provider 为 nil 表示没有网段可写(上游 provider:null);
// DirectIPCidrs 只有白名单模式(scoped)下才有意义。
type EntryDirect struct {
	DirectIPCidrs    []string
	FallbackKeepIPv4 []string
	Provider         *Provider
}

// EntryDirectCache 是 lg:按缓存键存计算结果,命中时如果 provider 文件已经不在了就当没命中。
// 零值不能用,用 NewEntryDirectCache;nil 就是不要缓存。
type EntryDirectCache struct {
	mu    sync.Mutex
	max   int
	order []string
	items map[string]EntryDirect
}

// NewEntryDirectCache 建一个缓存;max <= 0 时按上游的 16。
func NewEntryDirectCache(max int) *EntryDirectCache {
	if max <= 0 {
		max = 16
	}
	return &EntryDirectCache{max: max, items: map[string]EntryDirect{}}
}

func (c *EntryDirectCache) get(key string) (EntryDirect, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

// put 与上游 pg 一致:已经在里面的先删掉再放回去(等于移到队尾),满了先丢最早放进去的。
func (c *EntryDirectCache) put(key string, v EntryDirect) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drop(key)
	for len(c.order) >= c.max {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
	c.items[key] = v
	c.order = append(c.order, key)
}

func (c *EntryDirectCache) drop(key string) {
	if _, ok := c.items[key]; !ok {
		return
	}
	delete(c.items, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// entryShapeFunc 把「先读已部署的 provider、读不到再解码 .srs」这一层包起来,并按规则集 tag 缓存
// (上游 SC 里的局部 Map l):同一次生成里同一个规则集只取一次。
func entryShapeFunc(next ShapeFunc, deployed map[string]Provider, reader *DeployedReader) ShapeFunc {
	cache := map[string]RuleShape{}
	var mu sync.Mutex
	return func(ctx context.Context, tag string) (RuleShape, error) {
		mu.Lock()
		sh, ok := cache[tag]
		mu.Unlock()
		if ok {
			return sh.clone(), nil
		}
		if reader != nil {
			if p, has := deployed[tag]; has {
				if cidrs, read := reader.GeoIP(tag, p); read {
					sh = RuleShape{CIDRs: cidrs, DomainKeys: []string{}, OtherKeys: []string{}}
					mu.Lock()
					cache[tag] = sh
					mu.Unlock()
					return sh.clone(), nil
				}
			}
		}
		if next == nil {
			return RuleShape{}, fmt.Errorf("没有取规则集形状的方法")
		}
		sh, err := next(ctx, tag)
		if err != nil {
			return RuleShape{}, err
		}
		mu.Lock()
		cache[tag] = sh
		mu.Unlock()
		return sh.clone(), nil
	}
}

// BuildEntryDirect 对应上游 SC。plan 用 3e-4b-3 的 RoutePlan(旁路计划 + 直连 IP 计划 + 兜底 + scoped),
// cache 可以是 nil(不缓存);上游的缓存键是 re(sha256 的 dataDir / 指纹 / 直连 IP 计划),由调用方算好放进 plan.Key。
func BuildEntryDirect(ctx context.Context, plan RoutePlan, d EntryDirectDeps, cache *EntryDirectCache) (EntryDirect, error) {
	if cache != nil && plan.Key != "" {
		if v, ok := cache.get(plan.Key); ok {
			// 命中缓存时,带 provider 的结果要确认文件还在(上游同样看 <dataDir>/mihomo/rules/<name> 在不在)
			if v.Provider == nil || d.FS == nil || d.FS.Exists(filepath.Join(d.RulesDir, filepath.Base(v.Provider.Path))) {
				return v, nil
			}
			cache.drop(plan.Key)
		}
	}

	var reader *DeployedReader
	if d.FS != nil && d.MihomoDir != "" {
		reader = NewDeployedReader(d.FS, d.MihomoDir)
	}
	deps := d.Bypass
	deps.Shape = entryShapeFunc(d.Bypass.Shape, d.Deployed, reader)

	bypass := BuildEntryBypass(ctx, &plan.Plan, deps)

	// Object.values(u.cidrs).flat() + 直连 IP 计划的网段:合并后扣掉保留地址。
	// map 的遍历顺序在 Go 里是随机的,先排序键,保证同样的输入得到同样的中间结果。
	sets := make([]string, 0, len(bypass.CIDRs))
	for tag := range bypass.CIDRs {
		sets = append(sets, tag)
	}
	sort.Strings(sets)
	all := []string{}
	for _, tag := range sets {
		all = append(all, bypass.CIDRs[tag]...)
	}
	all = append(all, plan.DirectIP.CIDRs...)

	reserved := entryDirectReserved()
	kept, _ := SubtractRanges(MergeRanges(ParseRanges(all...)), ParseRanges(reserved...))
	cidrs := RangesToCIDRs(kept)
	if cidrs == nil {
		cidrs = []string{}
	}

	fallback := []string{}
	if plan.Fallback != nil {
		fallback = FallbackKeepIPv4(ctx, deps.Shape, plan.Fallback.NeedSets, plan.Fallback.NeedCidrs, reserved)
	}

	out := EntryDirect{FallbackKeepIPv4: fallback, DirectIPCidrs: []string{}}
	if plan.Scoped {
		out.DirectIPCidrs = cidrs
	} else if len(cidrs) > 0 {
		p, err := WriteProvider(d.FS, d.RulesDir, entryDirectTag, NewSource(3, Rule{Strs("ip_cidr", cidrs...)}), "ipcidr")
		if err != nil {
			return EntryDirect{}, err
		}
		out.Provider = &p
	}
	if cache != nil && plan.Key != "" {
		cache.put(plan.Key, out)
	}
	return out, nil
}
