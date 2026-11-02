// Package bandwidth 是「带宽择优」在后端的部分:统一测速参数、从已部署的内核配置里算出要测哪些节点、
// 估算每日下载量,以及 GET /api/openbox/bandwidth 的状态。
// 对应上游 v0.1.388 面板服务端的 Zc / Uf / SA / describe;内核侧的选择逻辑不在这里。
package bandwidth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

const (
	DefaultIntervalHours = 2
	DefaultMaxDownloadMB = 64
	MinIntervalHours     = 1
	MaxIntervalHours     = 24
	MinMaxDownloadMB     = 1
	MaxMaxDownloadMB     = 1024

	// endpointTag 进指纹:换了测速地址 / 方法,旧的历史就不该再沿用
	endpointTag = "cloudflare-down-v1"
)

// Settings 是所有启用带宽择优的组共用的测速参数(档案里的 bandwidth)。
type Settings struct {
	IntervalHours int `json:"intervalHours"`
	MaxDownloadMB int `json:"maxDownloadMB"`
}

// SettingsFrom 从档案里取测速参数:缺省或不是整数用默认值,超出范围夹进范围(与上游 Zc 一致)。
func SettingsFrom(profile map[string]any) Settings {
	s := Settings{IntervalHours: DefaultIntervalHours, MaxDownloadMB: DefaultMaxDownloadMB}
	b, _ := profile["bandwidth"].(map[string]any)
	if n, ok := wholeNumber(b["intervalHours"]); ok {
		s.IntervalHours = min(max(n, MinIntervalHours), MaxIntervalHours)
	}
	if n, ok := wholeNumber(b["maxDownloadMB"]); ok {
		s.MaxDownloadMB = min(max(n, MinMaxDownloadMB), MaxMaxDownloadMB)
	}
	return s
}

func wholeNumber(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 1<<31 {
		return 0, false
	}
	return int(f), true
}

// RoundsPerDay 是一天测几轮:24 小时 / 周期,向上取整。
func (s Settings) RoundsPerDay() int {
	return int(math.Ceil(24 / float64(max(1, s.IntervalHours))))
}

// DailyBytes 是 nodes 个要测的节点一天最多下载多少字节(估算上限,不含握手等额外开销)。
func DailyBytes(nodes int, s Settings) int64 {
	return int64(nodes) * int64(s.MaxDownloadMB) * 1_000_000 * int64(s.RoundsPerDay())
}

// Target 是一条要测的线路。同一条线路(节点配置、DNS、网络都相同)出现在多个组里只测一次,
// Tags 是它对应的全部节点名,Groups 是用到它的带宽择优组。
type Target struct {
	Fingerprint string
	Tags        []string
	Groups      []string
}

// 这几种出站不是真实线路,不测速
var notLine = map[string]bool{"selector": true, "urltest": true, "loadbalance": true, "direct": true, "block": true}

// Targets 从已部署的内核配置里找出要测的线路:bandwidth 为 true 的 urltest / loadbalance 出站,
// 它们的真实节点成员,按「节点配置(含前置链)+ DNS 服务器 + 网络 + 测速地址」合并。network 是网络指纹(如各网卡的地址),换网络就重测。
func Targets(cfg map[string]any, network string) []Target {
	byTag := map[string]map[string]any{}
	for _, key := range []string{"outbounds", "endpoints"} {
		list, _ := cfg[key].([]any)
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				if tag, _ := m["tag"].(string); tag != "" {
					byTag[tag] = m
				}
			}
		}
	}
	var resolve func(tag string, seen map[string]bool) any
	resolve = func(tag string, seen map[string]bool) any {
		c, ok := byTag[tag]
		if !ok || seen[tag] {
			return nil
		}
		next := map[string]bool{tag: true}
		for k := range seen {
			next[k] = true
		}
		out := make(map[string]any, len(c))
		for k, v := range c {
			if k != "tag" {
				out[k] = v
			}
		}
		if d, _ := c["detour"].(string); d != "" {
			out["detour"] = resolve(d, next)
		}
		if list, ok := c["outbounds"].([]any); ok {
			sub := make([]any, len(list))
			for i, m := range list {
				name, _ := m.(string)
				sub[i] = resolve(name, next)
			}
			out["outbounds"] = sub
		}
		return out
	}

	dns := []any{}
	if d, ok := cfg["dns"].(map[string]any); ok {
		servers, _ := d["servers"].([]any)
		for _, s := range servers {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			p := map[string]any{}
			for k, v := range m {
				if k != "tag" && k != "detour" {
					p[k] = v
				}
			}
			if det, _ := m["detour"].(string); det != "" {
				p["detour"] = resolve(det, map[string]bool{})
			}
			dns = append(dns, p)
		}
	}

	var order []string
	found := map[string]*Target{}
	outbounds, _ := cfg["outbounds"].([]any)
	for _, o := range outbounds {
		g, ok := o.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := g["type"].(string)
		if on, _ := g["bandwidth"].(bool); !on || (typ != "urltest" && typ != "loadbalance") {
			continue
		}
		gtag, _ := g["tag"].(string)
		members, _ := g["outbounds"].([]any)
		for _, m := range members {
			tag, _ := m.(string)
			c, ok := byTag[tag]
			if !ok {
				continue
			}
			if t, _ := c["type"].(string); notLine[t] {
				continue
			}
			fp := fingerprint(resolve(tag, map[string]bool{}), dns, network)
			t := found[fp]
			if t == nil {
				t = &Target{Fingerprint: fp}
				found[fp] = t
				order = append(order, fp)
			}
			if !contains(t.Tags, tag) {
				t.Tags = append(t.Tags, tag)
			}
			if !contains(t.Groups, gtag) {
				t.Groups = append(t.Groups, gtag)
			}
		}
	}
	out := make([]Target, 0, len(order))
	for _, fp := range order {
		out = append(out, *found[fp])
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// fingerprint:encoding/json 对 map 按键排序,所以同样的内容一定得到同样的指纹。
func fingerprint(node any, dns []any, network string) string {
	b, _ := json.Marshal(map[string]any{"node": node, "dns": dns, "network": network, "endpoint": endpointTag})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Status 对应前端 OpenboxBandwidthStatus。
type Status struct {
	Settings   Settings `json:"settings"`
	Nodes      int      `json:"nodes"`
	DailyBytes int64    `json:"dailyBytes"`
	Running    bool     `json:"running"`
	LastError  string   `json:"lastError"`
	UpdatedAt  int64    `json:"updatedAt"`
}

// Runtime 是测速调度运行时的情况(调度器接上之前为空)。
type Runtime struct {
	Running   bool
	LastError string
	UpdatedAt int64
}

// Service 回答 GET /api/openbox/bandwidth。
type Service struct {
	Profile func(ctx context.Context) (map[string]any, error) // 读档案
	Config  func() (map[string]any, error)                    // 读已部署的内核配置
	Runtime func() Runtime                                    // 可为空
}

// Describe 返回当前设置、要测的节点数和每日下载量上限。读不到档案按默认设置;读不到配置(没部署过)按 0 个节点。
func (s *Service) Describe(ctx context.Context) Status {
	set := Settings{IntervalHours: DefaultIntervalHours, MaxDownloadMB: DefaultMaxDownloadMB}
	if s.Profile != nil {
		if p, err := s.Profile(ctx); err == nil {
			set = SettingsFrom(p)
		}
	}
	st := Status{Settings: set}
	if s.Config != nil {
		if cfg, err := s.Config(); err == nil {
			st.Nodes = len(Targets(cfg, ""))
		}
	}
	st.DailyBytes = DailyBytes(st.Nodes, set)
	if s.Runtime != nil {
		r := s.Runtime()
		st.Running, st.LastError, st.UpdatedAt = r.Running, r.LastError, r.UpdatedAt
	}
	return st
}
