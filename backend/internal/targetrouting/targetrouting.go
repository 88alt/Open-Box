// Package targetrouting 实现连接页「改分流」背后的逻辑(/api/openbox/profile/target-routing):
// 按目标(域名 / IP)查它命中哪些站点集(目标分流)的规则,并在站点集里增删一条手写规则。
//
// 对齐依据(上游闭源,只看前端行为):panel/src/api/openbox.ts 的 OpenboxTargetRouting* 类型与
// ConnectionTargetRoutingDialog.vue ——
//   - 「目标分流」就是档案 routing.policies 里的站点集;停用的站点集不进内核配置,所以既不参与命中、也不能被选;
//   - 命中按站点集的先后排列,同一个站点集里按「domain → domain_suffix → domain_keyword → ip_cidr → 规则集」的先后
//     (与 policyentries 展开、内核生成路由规则的顺序一致);站点集手写的条目 source=policy(可删),
//     规则集链接 / 规则库分类里的条目 source=ruleset(只读);
//   - addition 是「按当前输入添加时会写进去的那一条」(域名 / 后缀小写去点,IP 写成 /32 或 /128 的 CIDR,IP 段取网络地址);
//   - 增删只改档案、不自动重启内核(重启会断开连接),调用方据 pendingRestart 提示「内核尚未应用」。
package targetrouting

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

const (
	maxMatches   = 200 // 一次查询最多回多少条命中(路由器内存小,也没人要看更多)
	maxPolicyIDs = 64  // 一次最多往多少个站点集里加
	maxRegexLen  = 512 // 规则集里的正则超过这个长度不拿来匹配
)

// 条目类型(与前端 OpenboxTargetRoutingMatch.type、policyentries 的类型串一致)。
const (
	TypeDomain        = rulelist.TypeDomain
	TypeDomainSuffix  = rulelist.TypeDomainSuffix
	TypeDomainKeyword = rulelist.TypeDomainKeyword
	TypeIPCIDR        = rulelist.TypeIPCIDR
	// 输入框里的「IP」,查询 / 添加时规整成 /32 或 /128 的 ip_cidr。
	typeIP = "ip"
)

// 命中的来源。
const (
	SourcePolicy  = "policy"
	SourceRuleset = "ruleset"
)

// ErrCoreUnavailable 表示没有可用的 openbox-core(站点集的规范化在核心里)。
var ErrCoreUnavailable = policyentries.ErrCoreUnavailable

// InputError 是调用方参数有问题(接口回 400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Source 提供规范化后的分流和站点集条目展开(*policyentries.Service 满足它)。
type Source interface {
	Routing(ctx context.Context) (policyentries.Routing, error)
	ExpandPolicy(ctx context.Context, p policyentries.Policy) ([]policyentries.Entry, []string, error)
}

// Store 在存储锁里读改写站点集列表(*profile.Service 满足它)。
type Store interface {
	UpdatePolicies(ctx context.Context, change func(list []any) ([]any, error)) ([]any, error)
}

// Service 是「改分流」服务。
type Service struct {
	src   Source
	store Store
}

// New 创建服务。
func New(src Source, store Store) *Service { return &Service{src: src, store: store} }

// Policy 是可选的目标分流组(前端 OpenboxTargetRoutingPolicy)。
type Policy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Match 是一条命中的规则(前端 OpenboxTargetRoutingMatch)。
type Match struct {
	Source    string `json:"source"`
	Name      string `json:"name"`
	PolicyID  string `json:"policyId,omitempty"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Outbound  string `json:"outbound,omitempty"`
	Removable bool   `json:"removable"`
}

// Addition 是按当前输入「添加」时会写进去的那一条。
type Addition struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Lookup 是查询结果(前端 OpenboxTargetRoutingLookup,不含 first)。
type Lookup struct {
	Policies   []Policy  `json:"policies"`
	Matches    []Match   `json:"matches"`
	Addition   *Addition `json:"addition,omitempty"`
	Incomplete bool      `json:"incomplete,omitempty"`
}

// FirstLookup 是 first=1 的结果:在 Lookup 之上多一个 first(没有命中时是 null,键始终在)。
type FirstLookup struct {
	Lookup
	First *Match `json:"first"`
}

// ---- 目标解析 ----

// target 是规整后的查询目标。
type target struct {
	host   string       // 域名类:小写、去尾点
	prefix netip.Prefix // IP 类:已取网络地址
	isIP   bool
	add    Addition
}

func invalidTarget() error { return &InputError{Msg: "请输入有效的域名、IP 或 IP 段"} }

func stripBrackets(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']' {
		return s[1 : len(s)-1]
	}
	return s
}

// validHost 判断 s(已小写、去尾点)是不是合法的主机名:只收 ASCII(punycode 形式),
// 每段 1~63 个字符,字母 / 数字 / 连字符 / 下划线,连字符不在首尾,总长 ≤ 253;纯 IP 不算域名。
func validHost(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	return true
}

func parseAddr(s string) (netip.Addr, bool) {
	if strings.Contains(s, "%") {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// parseTarget 按 typ(domain / domain_suffix / ip / ip_cidr)规整 text。typ 为空 = 自动判断(first=1 用)。
func parseTarget(text, typ string) (target, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 300 {
		return target{}, invalidTarget()
	}
	if typ == "" {
		host := stripBrackets(strings.SplitN(text, "/", 2)[0])
		switch _, ok := parseAddr(host); {
		case ok && strings.Contains(text, "/"):
			typ = TypeIPCIDR
		case ok:
			typ = typeIP
		default:
			typ = TypeDomain
		}
	}
	switch typ {
	case TypeDomain, TypeDomainSuffix:
		h := strings.ToLower(text)
		h = strings.TrimSuffix(h, ".")
		if typ == TypeDomainSuffix {
			h = strings.TrimPrefix(h, "*.")
			h = strings.TrimLeft(h, ".")
		}
		if !validHost(h) {
			return target{}, invalidTarget()
		}
		return target{host: h, add: Addition{Type: typ, Value: h}}, nil
	case typeIP:
		a, ok := parseAddr(stripBrackets(text))
		if !ok {
			return target{}, invalidTarget()
		}
		p := netip.PrefixFrom(a, a.BitLen())
		return target{isIP: true, prefix: p, add: Addition{Type: TypeIPCIDR, Value: p.String()}}, nil
	case TypeIPCIDR:
		p, ok := parseCIDR(stripBrackets(text))
		if !ok {
			return target{}, invalidTarget()
		}
		return target{isIP: true, prefix: p, add: Addition{Type: TypeIPCIDR, Value: p.String()}}, nil
	}
	return target{}, &InputError{Msg: "type 只能是 domain、domain_suffix、ip 或 ip_cidr"}
}

// parseCIDR 认 "a.b.c.d/n" 和裸 IP(当作整台主机),取网络地址。
func parseCIDR(s string) (netip.Prefix, bool) {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		host := stripBrackets(s[:i])
		if strings.Contains(host, "%") {
			return netip.Prefix{}, false
		}
		p, err := netip.ParsePrefix(host + s[i:])
		if err != nil {
			return netip.Prefix{}, false
		}
		if p.Addr().Is4In6() {
			return netip.Prefix{}, false
		}
		return p.Masked(), true
	}
	a, ok := parseAddr(s)
	if !ok {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

// ---- 匹配 ----

type matcher struct {
	t     target
	regex map[string]*regexp.Regexp // 编不出来的记 nil
}

func (m *matcher) hit(e policyentries.Entry) bool {
	if !m.t.isIP {
		h := m.t.host
		switch e.Type {
		case TypeDomain:
			return strings.EqualFold(strings.TrimSuffix(e.Content, "."), h)
		case TypeDomainSuffix:
			v := strings.ToLower(strings.TrimLeft(e.Content, "."))
			return v != "" && (h == v || strings.HasSuffix(h, "."+v))
		case TypeDomainKeyword:
			return e.Content != "" && strings.Contains(h, strings.ToLower(e.Content))
		case rulelist.TypeDomainRegex:
			re, seen := m.regex[e.Content]
			if !seen {
				if len(e.Content) <= maxRegexLen {
					re, _ = regexp.Compile(e.Content)
				}
				m.regex[e.Content] = re
			}
			return re != nil && re.MatchString(h)
		}
		return false
	}
	if e.Type != TypeIPCIDR {
		return false
	}
	p, ok := parseCIDR(e.Content)
	if !ok {
		return false
	}
	// 条目的范围把整个目标(IP 或 IP 段)都包住,才算命中
	return p.Bits() <= m.t.prefix.Bits() && p.Contains(m.t.prefix.Addr())
}

// Find 查目标命中哪些站点集的规则。typ 为空(first=1)按文本自动判断类型,且只回第一个命中、不给 addition。
// original 是连接原本的目标,目前只作参数占位(命中只看 text)。
func (s *Service) Find(ctx context.Context, text, typ string, first bool) (*FirstLookup, error) {
	if first {
		typ = ""
	}
	t, err := parseTarget(text, typ)
	if err != nil {
		return nil, err
	}
	rt, err := s.src.Routing(ctx)
	if err != nil {
		return nil, err
	}
	out := &FirstLookup{Lookup: Lookup{Policies: []Policy{}, Matches: []Match{}}}
	if !first {
		add := t.add
		out.Addition = &add
	}
	m := &matcher{t: t, regex: map[string]*regexp.Regexp{}}
	for _, p := range rt.Policies {
		if !p.Enabled {
			continue
		}
		out.Policies = append(out.Policies, Policy{ID: p.ID, Name: p.Name})
		if first && len(out.Matches) > 0 {
			continue
		}
		entries, missing, err := s.src.ExpandPolicy(ctx, p)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			out.Incomplete = true
		}
		for _, e := range entries {
			if len(out.Matches) >= maxMatches {
				out.Incomplete = true
				break
			}
			if !m.hit(e) {
				continue
			}
			src, removable := SourceRuleset, false
			if e.Source == policyentries.SourceCustom {
				src, removable = SourcePolicy, true
			}
			out.Matches = append(out.Matches, Match{
				Source: src, Name: p.Name, PolicyID: p.ID, Type: e.Type, Value: e.Content,
				Outbound: p.Default, Removable: removable,
			})
			if first {
				break
			}
		}
	}
	if first && len(out.Matches) > 0 {
		f := out.Matches[0]
		out.First = &f
		out.Matches = out.Matches[:1]
	}
	return out, nil
}

// ---- 增删 ----

// Change 是增删的结果:Changed=false 表示档案里本来就是这个样子(没有写盘)。
type Change struct{ Changed bool }

var errUnchanged = errors.New("unchanged")

// fieldOf 返回站点集档案里存某类规则的键。
func fieldOf(typ string) (string, bool) {
	switch typ {
	case TypeDomain:
		return "domain", true
	case TypeDomainSuffix, "domainSuffix":
		return "domainSuffix", true
	case TypeDomainKeyword, "domainKeyword":
		return "domainKeyword", true
	case TypeIPCIDR, "ipCidr":
		return "ipCidr", true
	}
	return "", false
}

// canon 把一条已存的值规整成可比较的样子(与 parseTarget 的规整一致);认不出的原样比。
func canon(field, v string) string {
	v = strings.TrimSpace(v)
	switch field {
	case "domain":
		return strings.ToLower(strings.TrimSuffix(v, "."))
	case "domainSuffix":
		return strings.ToLower(strings.TrimLeft(strings.TrimPrefix(v, "*."), "."))
	case "domainKeyword":
		return strings.ToLower(v)
	case "ipCidr":
		if p, ok := parseCIDR(v); ok {
			return p.String()
		}
	}
	return v
}

// policyKey 是站点集在档案里的 id:写了 id 用 id,没写的按核心的规矩补成 policy-<序号>。
func policyKey(m map[string]any, index int) string {
	if id, _ := m["id"].(string); strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	return fmt.Sprintf("policy-%d", index)
}

func policyLabel(m map[string]any, index int) string {
	if n, _ := m["name"].(string); strings.TrimSpace(n) != "" {
		return strings.TrimSpace(n)
	}
	return fmt.Sprintf("策略-%d", index+1)
}

func findPolicy(list []any, id string) (map[string]any, int) {
	for i, e := range list {
		if m, ok := e.(map[string]any); ok && policyKey(m, i) == id {
			return m, i
		}
	}
	return nil, -1
}

func strList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, x...)
	}
	return out
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (s *Service) write(ctx context.Context, change func(list []any) error) (*Change, error) {
	_, err := s.store.UpdatePolicies(ctx, func(list []any) ([]any, error) {
		if err := change(list); err != nil {
			return nil, err
		}
		return list, nil
	})
	if errors.Is(err, errUnchanged) {
		return &Change{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &Change{Changed: true}, nil
}

// Add 把 text(按 typ 规整)作为手写规则加进 policyIDs 里每一个站点集。已经有了的跳过;全都已有时 Changed=false。
// 站点集不存在或已停用回 *InputError,什么都不写。
func (s *Service) Add(ctx context.Context, text, typ string, policyIDs []string) (*Change, error) {
	if typ == "" {
		return nil, &InputError{Msg: "缺少 type"}
	}
	t, err := parseTarget(text, typ)
	if err != nil {
		return nil, err
	}
	ids := dedupeIDs(policyIDs)
	if len(ids) == 0 {
		return nil, &InputError{Msg: "请选择目标分流组"}
	}
	if len(ids) > maxPolicyIDs {
		return nil, &InputError{Msg: "一次选的目标分流组太多"}
	}
	field, _ := fieldOf(t.add.Type)
	want := canon(field, t.add.Value)
	return s.write(ctx, func(list []any) error {
		changed := false
		for _, id := range ids {
			m, i := findPolicy(list, id)
			if m == nil {
				return &InputError{Msg: "没有这个目标分流组: " + id}
			}
			if en, ok := m["enabled"].(bool); ok && !en {
				return &InputError{Msg: "目标分流组「" + policyLabel(m, i) + "」已停用"}
			}
			cur := strList(m[field])
			dup := false
			for _, v := range cur {
				if canon(field, v) == want {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			m[field] = toAny(append(cur, t.add.Value))
			changed = true
		}
		if !changed {
			return errUnchanged
		}
		return nil
	})
}

func dedupeIDs(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// RemoveRequest 是要删的那条命中(前端把整条 match 原样发来,这里只认这几个字段)。
type RemoveRequest struct {
	Source   string `json:"source"`
	PolicyID string `json:"policyId"`
	Type     string `json:"type"`
	Value    string `json:"value"`
}

// Remove 从站点集里删掉一条手写规则(连同它的备注)。规则集里的条目不能删(*InputError);站点集不存在回 *InputError;
// 站点集里已经没有这条规则时 Changed=false(删除是幂等的)。
func (s *Service) Remove(ctx context.Context, req RemoveRequest) (*Change, error) {
	if req.Source != SourcePolicy {
		return nil, &InputError{Msg: "规则集中的条目请在规则集源头管理"}
	}
	field, ok := fieldOf(req.Type)
	if !ok {
		return nil, &InputError{Msg: "不能删除这种类型的规则: " + req.Type}
	}
	if strings.TrimSpace(req.Value) == "" || strings.TrimSpace(req.PolicyID) == "" {
		return nil, &InputError{Msg: "缺少 policyId 或 value"}
	}
	want := canon(field, req.Value)
	return s.write(ctx, func(list []any) error {
		m, _ := findPolicy(list, strings.TrimSpace(req.PolicyID))
		if m == nil {
			return &InputError{Msg: "没有这个目标分流组: " + req.PolicyID}
		}
		cur := strList(m[field])
		var keep []string
		var dropped []string
		for _, v := range cur {
			if canon(field, v) == want {
				dropped = append(dropped, v)
				continue
			}
			keep = append(keep, v)
		}
		if len(dropped) == 0 {
			return errUnchanged
		}
		m[field] = toAny(keep)
		// 备注的键是「类型:值」,一并清掉
		if notes, ok := m["notes"].(map[string]any); ok {
			for _, v := range dropped {
				delete(notes, field+":"+v)
			}
		}
		return nil
	})
}
