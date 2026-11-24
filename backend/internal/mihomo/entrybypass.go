package mihomo

import (
	"context"
	"fmt"
	"math/big"
	"strings"
)

// 入口旁路的规则集网段(双内核 3e-4b;对应上游 v0.1.462 Node 的 ii)。
//
// core 的 bypassPlan 告诉后端「哪些站点集想让入口直接放行(不进内核)」,以及「排在前面的、可能抢走一部分地址的规则」。
// 这里把每个想放行的规则集读成网段,扣掉不能放行的地址(假 IP 池、前面规则的地址、地址空间的最后一个地址),
// 能放行的记进 Sets / CIDRs / Trimmed,放不了的记原因、仍然走内核(「按兼容路径进内核」)。

// EarlierRule 是 core bypassPlan 里「排在前面的规则」(字段同 core/internal/routing/bypass.go)。
type EarlierRule struct {
	Name  string   `json:"name"`
	Geoip []string `json:"geoip"`
	Cidrs []string `json:"cidrs"`
	Lists []string `json:"lists,omitempty"`
}

// PendingItem 是 bypassPlan 里一个「要放行、但前面有规则会抢地址」的站点集。
type PendingItem struct {
	Policy  string        `json:"policy"`
	Sets    []string      `json:"sets"`
	Against []EarlierRule `json:"against"`
}

// BypassPlan 是 core bypassPlan 方法的返回(上游的 gn)。
type BypassPlan struct {
	Enabled bool          `json:"enabled"`
	Sets    []string      `json:"sets"`
	Pending []PendingItem `json:"pending"`
	FakeIP  bool          `json:"fakeIp"`
	Reason  string        `json:"reason"`
}

// UserList 是一份用户规则集链接的信息(上游 pt 的值里 ii 只看 ip)。
type UserList struct {
	IP bool
}

// CheckedSet 是一个站点集的检查结果。
type CheckedSet struct {
	Policy  string   `json:"policy"`
	Sets    []string `json:"sets"`
	OK      bool     `json:"ok"`
	Trimmed bool     `json:"trimmed"`
	Reason  string   `json:"reason"`
}

// TrimmedSet 是被扣过地址的规则集:CIDRs 是扣完剩下的网段,Removed 是扣掉了什么,Original 是原来有几条网段。
type TrimmedSet struct {
	CIDRs    []string `json:"cidrs"`
	Removed  []string `json:"removed"`
	Original int      `json:"original"`
}

// EntryBypass 是 ii 的结果。切片和 map 都不是 nil(JSON 里是 [] / {})。
type EntryBypass struct {
	Enabled bool                  `json:"enabled"`
	Sets    []string              `json:"sets"`
	Trimmed map[string]TrimmedSet `json:"trimmed"`
	Pending []PendingItem         `json:"pending"`
	FakeIP  bool                  `json:"fakeIp"`
	Checked []CheckedSet          `json:"checked"`
	CIDRs   map[string][]string   `json:"cidrs"`
	Reason  string                `json:"reason"`
}

// EntryBypassDeps 是 BuildEntryBypass 要的外部东西。
type EntryBypassDeps struct {
	// Shape 取规则集形状(ShapeReader.Shape,或 3e-4d 里先读已部署 provider 再退回它的那一层)。
	Shape ShapeFunc
	// UserLists 列出用户规则集链接(上游 pt);出错当作没有(上游同样吞掉错误)。
	UserLists func() (map[string]UserList, error)
	// SrsPath 给规则集名对应的 .srs 路径(RuleSetPathFunc);FS 用来看它在不在。
	SrsPath func(tag string) (string, error)
	FS      FS
}

type shapeResult struct {
	shape RuleShape
	err   error
}

type rangesResult struct {
	ranges []Range
	err    string
}

// BuildEntryBypass 对应上游 ii。plan 为 nil、或没有要放行的站点集时,返回空结果(只带 plan 的 fakeIp 与 reason)。
//
// 与上游的两处差别(都偏保守):规则集名不合法(SrsPath 报错)时,上游会抛异常中断整个计算,
// 这里把它当成「这份规则集取不到」,只让相关站点集走兼容路径进内核;取不到的原因文案同上游。
func BuildEntryBypass(ctx context.Context, plan *BypassPlan, d EntryBypassDeps) EntryBypass {
	out := EntryBypass{
		Sets: []string{}, Trimmed: map[string]TrimmedSet{}, Pending: []PendingItem{},
		Checked: []CheckedSet{}, CIDRs: map[string][]string{},
	}
	if plan == nil {
		return out
	}
	out.FakeIP = plan.FakeIP
	out.Reason = plan.Reason

	type item struct {
		policy  string
		sets    []string
		against []EarlierRule
	}
	var items []item
	for _, s := range plan.Sets {
		items = append(items, item{sets: []string{s}})
	}
	for _, p := range plan.Pending {
		items = append(items, item{policy: p.Policy, sets: p.Sets, against: p.Against})
	}
	if len(items) == 0 {
		return out
	}

	memo := map[string]shapeResult{}
	shape := func(tag string) (RuleShape, error) {
		if r, ok := memo[tag]; ok {
			return r.shape, r.err
		}
		sh, err := d.Shape(ctx, tag)
		memo[tag] = shapeResult{sh, err}
		return sh, err
	}

	var users map[string]UserList
	usersLoaded := false
	userList := func(tag string) (UserList, bool) {
		if !usersLoaded {
			usersLoaded = true
			if d.UserLists != nil {
				if m, err := d.UserLists(); err == nil {
					users = m
				}
			}
		}
		u, ok := users[tag]
		return u, ok
	}

	// 一份「规则集链接」里的网段(上游 h):不是 IP 链接 / 文件不在 → 空;取不到 / 说不清 → 原因。
	listRanges := func(tag string) rangesResult {
		u, isUser := userList(tag)
		if isUser && !u.IP {
			return rangesResult{}
		}
		name := tag
		if isUser {
			name = tag + "-ip"
			srs, err := d.SrsPath(name)
			if err != nil {
				return rangesResult{err: err.Error()}
			}
			if !d.FS.Exists(srs) {
				return rangesResult{}
			}
		}
		sh, err := shape(name)
		switch {
		case err != nil:
			return rangesResult{err: err.Error()}
		case sh.Unbounded:
			return rangesResult{err: "含逻辑 / 取反规则,范围说不清"}
		}
		return rangesResult{ranges: ParseRanges(sh.CIDRs...)}
	}

	var reasons []string
	if plan.Reason != "" {
		reasons = append(reasons, plan.Reason)
	}
	policyLabel := func(policy string) string {
		if policy == "" {
			return ""
		}
		return "站点集「" + policy + "」"
	}

	type earlier struct {
		name   string
		ranges []Range
	}
	for _, w := range items {
		blocked := ""
		var against []earlier
		for _, s := range w.against {
			e := earlier{name: s.Name, ranges: ParseRanges(s.Cidrs...)}
			if len(s.Lists) > 0 {
				if !plan.FakeIP {
					blocked = fmt.Sprintf("「%s」用了规则集链接「%s」,里面有没有 IP 段说不清", s.Name, s.Lists[0])
					break
				}
				for _, x := range s.Lists {
					r := listRanges(x)
					if r.err != "" {
						blocked = fmt.Sprintf("「%s」的规则集链接「%s」%s", s.Name, x, r.err)
						break
					}
					e.ranges = append(e.ranges, r.ranges...)
				}
				if blocked != "" {
					break
				}
			}
			for _, x := range s.Geoip {
				sh, err := shape(x)
				if err != nil {
					blocked = fmt.Sprintf("「%s」的集合「%s」%s", s.Name, x, err.Error())
					break
				}
				if sh.Unbounded {
					blocked = fmt.Sprintf("「%s」的集合「%s」含逻辑 / 取反规则,范围说不清", s.Name, x)
					break
				}
				if len(sh.DomainKeys) > 0 {
					blocked = fmt.Sprintf("「%s」的集合「%s」含域名条件（%s）,它解析出来的 IP 在入口分不出来", s.Name, x, strings.Join(sh.DomainKeys, " / "))
					break
				}
				e.ranges = append(e.ranges, ParseRanges(sh.CIDRs...)...)
			}
			if blocked != "" {
				break
			}
			against = append(against, e)
		}

		for _, set := range w.sets {
			reason := blocked
			var removed []string
			var raw []string
			if reason == "" {
				sh, err := shape(set)
				switch {
				case err != nil:
					reason = fmt.Sprintf("集合「%s」%s", set, err.Error())
				case sh.Unbounded:
					reason = fmt.Sprintf("集合「%s」含逻辑 / 取反规则,范围说不清", set)
				case len(sh.DomainKeys) > 0 || len(sh.OtherKeys) > 0:
					keys := append(append([]string{}, sh.DomainKeys...), sh.OtherKeys...)
					reason = fmt.Sprintf("集合「%s」不是纯目标 IP 规则（含 %s 条件）,入口只按 IP 放行会放大它的范围", set, strings.Join(keys, " / "))
				case len(sh.CIDRs) > 0:
					raw = sh.CIDRs
				default:
					reason = fmt.Sprintf("集合「%s」里没有 ip_cidr", set)
				}
			}

			var kept []Range
			if reason == "" {
				kept = MergeRanges(ParseRanges(raw...))
				if last := lastAddrCIDR(raw); last != "" {
					var trimmedEnd []Range
					for _, r := range kept {
						if r.End.Cmp(maxAddr(r.Family)) == 0 {
							r = Range{Family: r.Family, Start: r.Start, End: new(big.Int).Sub(r.End, big.NewInt(1))}
						}
						if r.Start.Cmp(r.End) <= 0 {
							trimmedEnd = append(trimmedEnd, r)
						}
					}
					kept = trimmedEnd
					removed = append(removed, fmt.Sprintf("到地址空间末尾的区间（%s）的最后一个地址", last))
				}
				if plan.FakeIP {
					k, gone := SubtractRanges(kept, ParseRanges(fakeIPPools...))
					if len(gone) > 0 {
						kept = k
						removed = append(removed, "FakeIP 占位地址池")
					}
				}
				for _, e := range against {
					k, gone := SubtractRanges(kept, e.ranges)
					if len(gone) > 0 {
						kept = k
						removed = append(removed, fmt.Sprintf("和前面「%s」重叠的 %s", e.name, describeRanges(gone)))
					}
				}
				if len(kept) == 0 {
					if len(removed) > 0 {
						reason = fmt.Sprintf("集合「%s」扣掉 %s 之后一个地址都不剩", set, strings.Join(removed, "、"))
					} else {
						reason = fmt.Sprintf("集合「%s」里没有可旁路的地址", set)
					}
				}
			}

			note := reason
			if note == "" && len(removed) > 0 {
				note = fmt.Sprintf("扣掉 %s 后旁路", strings.Join(removed, "、"))
			}
			out.Checked = append(out.Checked, CheckedSet{
				Policy: w.policy, Sets: []string{set}, OK: reason == "",
				Trimmed: reason == "" && len(removed) > 0, Reason: note,
			})
			if reason != "" {
				reasons = append(reasons, policyLabel(w.policy)+reason+",按兼容路径进内核")
				continue
			}
			if !containsString(out.Sets, set) {
				out.Sets = append(out.Sets, set)
			}
			if len(removed) > 0 {
				cidrs := RangesToCIDRs(kept)
				out.CIDRs[set] = cidrs
				out.Trimmed[set] = TrimmedSet{CIDRs: cidrs, Removed: removed, Original: len(raw)}
				reasons = append(reasons, fmt.Sprintf("%s集合「%s」扣掉 %s 后入口旁路（保留 %d 段）",
					policyLabel(w.policy), set, strings.Join(removed, "、"), len(cidrs)))
			} else {
				out.CIDRs[set] = append([]string{}, raw...)
			}
		}
	}
	out.Enabled = len(out.Sets) > 0
	out.Reason = strings.Join(reasons, ";")
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
