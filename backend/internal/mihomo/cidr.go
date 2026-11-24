package mihomo

import (
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 本文件是上游 Node 里一组网段小工具的 Go 版(v0.1.462 index.mjs 的 Yb / qb / Jt / eg / _n / oi / no / ZA / jt),
// 入口旁路(3e-4)的各块都靠它们。口径和上游一样:IPv4 与 IPv6 分开算,地址用 big.Int,
// 解析宽松到上游那个程度(IPv4 每段 1~3 位十进制,允许前导零,IPv6 自己展开 `::` 与结尾的点分 IPv4)。

var (
	cidrShape    = regexp.MustCompile(`^([^/]+)(?:/(\d{1,3}))?$`)
	v4Shape      = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)
	v6Charset    = regexp.MustCompile(`^[0-9a-fA-F:.]+$`)
	v6TailV4     = regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)$`)
	v6GroupShape = regexp.MustCompile(`^[0-9a-fA-F]{1,4}$`)
)

// Range 是一段连续地址(含两端);Family 是 4 或 6。
type Range struct {
	Family     int
	Start, End *big.Int
}

func familyBits(family int) int {
	if family == 4 {
		return 32
	}
	return 128
}

func parseV4(s string) (*big.Int, bool) {
	m := v4Shape.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	var r uint64
	for i := 1; i <= 4; i++ {
		n, _ := strconv.Atoi(m[i])
		if n > 255 {
			return nil, false
		}
		r = r<<8 | uint64(n)
	}
	return new(big.Int).SetUint64(r), true
}

func parseV6(s string) (*big.Int, bool) {
	if !v6Charset.MatchString(s) || !strings.Contains(s, ":") {
		return nil, false
	}
	t := s
	if m := v6TailV4.FindStringSubmatch(t); m != nil {
		c, ok := parseV4(m[1])
		if !ok {
			return nil, false
		}
		hi := new(big.Int).Rsh(c, 16)
		lo := new(big.Int).And(c, big.NewInt(0xffff))
		t = t[:len(t)-len(m[1])] + hi.Text(16) + ":" + lo.Text(16)
	}
	n := strings.Split(t, "::")
	if len(n) > 2 {
		return nil, false
	}
	var o, rest []string
	if n[0] != "" {
		o = strings.Split(n[0], ":")
	}
	if len(n) == 2 && n[1] != "" {
		rest = strings.Split(n[1], ":")
	}
	if len(n) == 1 && len(o) != 8 {
		return nil, false
	}
	fill := 8 - len(o) - len(rest)
	if fill < 0 || (len(n) == 2 && fill < 1) {
		return nil, false
	}
	groups := append([]string{}, o...)
	if len(n) == 2 {
		for i := 0; i < fill; i++ {
			groups = append(groups, "0")
		}
	}
	groups = append(groups, rest...)
	if len(groups) != 8 {
		return nil, false
	}
	r := new(big.Int)
	for _, g := range groups {
		if !v6GroupShape.MatchString(g) {
			return nil, false
		}
		v, _ := strconv.ParseUint(g, 16, 32)
		r.Lsh(r, 16)
		r.Or(r, new(big.Int).SetUint64(v))
	}
	return r, true
}

// CIDR 是解析后的网段:Net 已按前缀掩掉主机位。
type CIDR struct {
	Family int
	Net    *big.Int
	Prefix int
}

// ParseCIDR 对应上游 Jt:去掉首尾空白(JS trim 口径),`地址` 或 `地址/前缀`(前缀 1~3 位十进制,
// 不超过 32 / 128),缺前缀按单个地址。主机位会被掩掉(`10.1.2.3/8` → `10.0.0.0/8`)。
func ParseCIDR(s string) (CIDR, bool) {
	m := cidrShape.FindStringSubmatch(strings.TrimFunc(s, jsSpace))
	if m == nil {
		return CIDR{}, false
	}
	family := 4
	ip, ok := parseV4(m[1])
	if !ok {
		family = 6
		if ip, ok = parseV6(m[1]); !ok {
			return CIDR{}, false
		}
	}
	bits := familyBits(family)
	prefix := bits
	if m[2] != "" {
		prefix, _ = strconv.Atoi(m[2])
		if prefix > bits {
			return CIDR{}, false
		}
	}
	// 掩码 = 高 prefix 位为 1。
	mask := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	mask.Sub(mask, big.NewInt(1))
	host := new(big.Int).Lsh(big.NewInt(1), uint(bits-prefix))
	host.Sub(host, big.NewInt(1))
	mask.AndNot(mask, host)
	return CIDR{Family: family, Net: new(big.Int).And(ip, mask), Prefix: prefix}, true
}

// Range 把网段展开成起止地址(上游 eg)。
func (c CIDR) Range() Range {
	size := new(big.Int).Lsh(big.NewInt(1), uint(familyBits(c.Family)-c.Prefix))
	end := new(big.Int).Add(c.Net, size)
	end.Sub(end, big.NewInt(1))
	return Range{Family: c.Family, Start: new(big.Int).Set(c.Net), End: end}
}

// ParseRanges 对应上游 jt:解析不了的丢掉,保持顺序。
func ParseRanges(cidrs ...string) []Range {
	out := make([]Range, 0, len(cidrs))
	for _, s := range cidrs {
		if c, ok := ParseCIDR(s); ok {
			out = append(out, c.Range())
		}
	}
	return out
}

// MergeRanges 对应上游 _n:IPv4 在前、IPv6 在后,各自按起点排序,相邻(含首尾相接)或重叠的并成一段。
func MergeRanges(in []Range) []Range {
	var out []Range
	for _, family := range []int{4, 6} {
		var list []Range
		for _, r := range in {
			if r.Family == family {
				list = append(list, r)
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Start.Cmp(list[j].Start) < 0 })
		for _, r := range list {
			if n := len(out); n > 0 && out[n-1].Family == family &&
				r.Start.Cmp(new(big.Int).Add(out[n-1].End, big.NewInt(1))) <= 0 {
				if r.End.Cmp(out[n-1].End) > 0 {
					out[n-1].End = new(big.Int).Set(r.End)
				}
				continue
			}
			out = append(out, Range{Family: family, Start: new(big.Int).Set(r.Start), End: new(big.Int).Set(r.End)})
		}
	}
	return out
}

// SubtractRanges 对应上游 oi:先把两边各自并好,kept 是 a 里不在 holes 中的部分,removed 是 a 与 holes 的交集。
func SubtractRanges(a, holes []Range) (kept, removed []Range) {
	h := MergeRanges(holes)
	one := big.NewInt(1)
	s := 0
	for _, item := range MergeRanges(a) {
		family, last := item.Family, item.End
		cur := new(big.Int).Set(item.Start)
		for s < len(h) && (h[s].Family < family || (h[s].Family == family && h[s].End.Cmp(cur) < 0)) {
			s++
		}
		for s < len(h) && h[s].Family == family && h[s].Start.Cmp(last) <= 0 {
			u := h[s]
			if u.Start.Cmp(cur) > 0 {
				kept = append(kept, Range{Family: family, Start: new(big.Int).Set(cur), End: new(big.Int).Sub(u.Start, one)})
			}
			from := cur
			if u.Start.Cmp(cur) > 0 {
				from = u.Start
			}
			to := last
			if u.End.Cmp(last) < 0 {
				to = u.End
			}
			removed = append(removed, Range{Family: family, Start: new(big.Int).Set(from), End: new(big.Int).Set(to)})
			cur = new(big.Int).Add(u.End, one)
			if cur.Cmp(last) > 0 {
				break
			}
			s++
		}
		if cur.Cmp(last) <= 0 {
			kept = append(kept, Range{Family: family, Start: cur, End: new(big.Int).Set(last)})
		}
	}
	return kept, removed
}

// FormatIP 对应上游 ZA:IPv4 点分,IPv6 小写十六进制、去前导零、把最长(同长取最前)一段 ≥2 个零组写成 `::`。
func FormatIP(family int, v *big.Int) string {
	if family == 4 {
		parts := make([]string, 4)
		for i := 0; i < 4; i++ {
			b := new(big.Int).Rsh(v, uint(24-8*i))
			parts[i] = strconv.Itoa(int(b.Uint64() & 0xff))
		}
		return strings.Join(parts, ".")
	}
	groups := make([]string, 8)
	for i := 0; i < 8; i++ {
		g := new(big.Int).Rsh(v, uint(112-16*i))
		groups[i] = strconv.FormatUint(g.Uint64()&0xffff, 16)
	}
	best := struct{ at, n int }{-1, 0}
	for i := 0; i < 8; i++ {
		if groups[i] != "0" {
			continue
		}
		j := i
		for j < 8 && groups[j] == "0" {
			j++
		}
		if j-i > best.n {
			best.at, best.n = i, j-i
		}
		i = j
	}
	if best.n < 2 {
		return strings.Join(groups, ":")
	}
	return strings.Join(groups[:best.at], ":") + "::" + strings.Join(groups[best.at+best.n:], ":")
}

// RangesToCIDRs 对应上游 no:每段拆成最少的对齐网段(从起点起尽量取大块),按输入顺序输出。
func RangesToCIDRs(in []Range) []string {
	var out []string
	one := big.NewInt(1)
	for _, r := range in {
		bits := familyBits(r.Family)
		cur := new(big.Int).Set(r.Start)
		for cur.Cmp(r.End) <= 0 {
			left := new(big.Int).Sub(r.End, cur)
			left.Add(left, one)
			fit := left.BitLen() - 1
			align := bits
			if cur.Sign() != 0 {
				align = int(cur.TrailingZeroBits())
			}
			step := fit
			if align < step {
				step = align
			}
			out = append(out, FormatIP(r.Family, cur)+"/"+strconv.Itoa(bits-step))
			cur.Add(cur, new(big.Int).Lsh(one, uint(step)))
		}
	}
	return out
}
