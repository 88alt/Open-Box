package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 规则集的「形状」(双内核 3e-4b;对应上游 v0.1.462 Node 的 TM / Ru / Pm / Nu / AM / xM)。
//
// 入口旁路要知道一个 geoip / 用户规则集「是不是纯目标 IP 规则」、里面有哪些网段:
// 先 `sing-box rule-set decompile` 还原成源文件 JSON,再看每条规则有哪些字段。
// 含 logical / invert / 嵌套 rules 的算「说不清范围」(Unbounded);含域名类字段或别的字段的,入口只按 IP 放行会放大范围。

const (
	shapeCIDRLimit = 32768 // 上游 YA:一份形状最多缓存这么多网段,所有缓存项合计也不超过它
	shapeCacheMax  = 32    // 上游 kM:最多缓存这么多份形状
)

// 取不到(或不是纯网段)的规则集形状里,域名类字段的名字(上游 vM)。
var shapeDomainKeys = map[string]bool{"domain": true, "domain_suffix": true, "domain_keyword": true, "domain_regex": true}

// 假 IP 池与它旁边那段保留地址(上游 or / sr / ec / tc)。
const (
	fakeIPv4Pool = "198.19.0.0/16"
	fakeIPv6Pool = "2001:2::/49"
)

var (
	fakeIPPools       = []string{fakeIPv4Pool, fakeIPv6Pool}
	benchmarkSiblings = []string{"198.18.0.0/16", "2001:2:0:8000::/49"}
)

// RuleShape 是上游 TM 的结果。四个切片永远不是 nil(JSON 里是 [] 而不是 null)。
type RuleShape struct {
	CIDRs      []string `json:"cidrs"`
	Unbounded  bool     `json:"unbounded"`
	DomainKeys []string `json:"domainKeys"`
	OtherKeys  []string `json:"otherKeys"`
}

func (s RuleShape) clone() RuleShape {
	return RuleShape{
		CIDRs:      append([]string{}, s.CIDRs...),
		Unbounded:  s.Unbounded,
		DomainKeys: append([]string{}, s.DomainKeys...),
		OtherKeys:  append([]string{}, s.OtherKeys...),
	}
}

// ShapeFunc 取一个规则集的形状。错误文案直接拼进「说不清」的原因里。
type ShapeFunc func(ctx context.Context, tag string) (RuleShape, error)

// orderedObject 按 JS 的 Object.keys 口径读一个 JSON 对象:数组下标样的键(0、1、2…)先按数值升序,
// 其余键按第一次出现的顺序;重复的键以最后一个值为准、位置留在第一次出现处。
func orderedObject(raw json.RawMessage) (keys []string, vals map[string]json.RawMessage, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, nil, false
	}
	vals = map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, false
		}
		k, isStr := kt.(string)
		if !isStr {
			return nil, nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, false
		}
		if _, seen := vals[k]; !seen {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	return jsKeyOrder(keys), vals, true
}

// arrayIndex 判断 JS 把这个键当不当数组下标:"0",或不带前导零的十进制且不超过 2^32-2。
func arrayIndex(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	if err != nil || n > 4294967294 {
		return 0, false
	}
	return n, true
}

func jsKeyOrder(keys []string) []string {
	var idx, rest []string
	for _, k := range keys {
		if _, ok := arrayIndex(k); ok {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, _ := arrayIndex(idx[i])
		b, _ := arrayIndex(idx[j])
		return a < b
	})
	return append(idx, rest...)
}

// jsTruthy 是 JS 的真假判断,作用在一个 JSON 值上:null / false / 0 / "" 为假,其余(含空数组、空对象)为真。
func jsTruthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "" || s == "null" || s == "false":
		return false
	case s == "true":
		return true
	case s[0] == '"':
		return s != `""`
	case s[0] == '{' || s[0] == '[':
		return true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return errors.Is(err, strconv.ErrRange) && f != 0 // 1e999 在 JS 里是 Infinity,为真
	}
	return f != 0
}

// jsString 是 JS 的 String(值),作用在一个 JSON 值上。只有字符串和「只含一个字符串的数组」可能被认成网段,
// 别的写法只要保证「不会被认成网段」且「个数不变」就够了。
func jsString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "null":
		return "null"
	case s == "true" || s == "false":
		return s
	case s != "" && s[0] == '"':
		var out string
		if json.Unmarshal([]byte(s), &out) == nil {
			return out
		}
		return ""
	case s != "" && s[0] == '[':
		var items []json.RawMessage
		if json.Unmarshal([]byte(s), &items) != nil {
			return ""
		}
		parts := make([]string, len(items))
		for i, it := range items {
			if t := strings.TrimSpace(string(it)); t == "null" {
				parts[i] = "" // 数组里的 null 在 JS 里拼成空串
			} else {
				parts[i] = jsString(it)
			}
		}
		return strings.Join(parts, ",")
	case s != "" && s[0] == '{':
		return "[object Object]"
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) && f != 0 {
			return jsNumber(f)
		}
		return s
	}
	return jsNumber(f)
}

// jsNumber 是 JS 的 Number → 字符串:绝对值在 1e-6 到 1e21 之间写成十进制,否则写成 1e+21 / 1.5e-7 这种。
func jsNumber(f float64) string {
	switch {
	case f == 0:
		return "0"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	if a := math.Abs(f); a >= 1e21 || a < 1e-6 {
		mant, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
		return mant + "e" + exp[:1] + strings.TrimLeft(exp[1:], "0")
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// jsList 是 JS 的 en():数组按元素,null / 缺失是空,别的单个值包成一个元素。
func jsList(raw json.RawMessage, present bool) []string {
	if !present {
		return nil
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return nil
	}
	if s != "" && s[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal([]byte(s), &items) != nil {
			return nil
		}
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = jsString(it)
		}
		return out
	}
	return []string{jsString(raw)}
}

func addOnce(list []string, k string) []string {
	for _, x := range list {
		if x == k {
			return list
		}
	}
	return append(list, k)
}

// ExtractShape 对应上游 TM(JSON.parse 之后的对象)。doc 必须是合法 JSON,否则报错(同上游 JSON.parse 抛错)。
// 只看 `rules` 数组:不是对象的元素跳过;`type` 是 "logical"、或 `rules` / `invert` 为真的元素让整份「说不清」;
// 其余元素里,`ip_cidr` 的值收进网段(保持顺序、不去重),别的键按第一次出现的顺序记进 domainKeys(域名类四个)或 otherKeys。
// 数组元素在 JS 里也是对象,它的「键」是 0、1、2…,会记进 otherKeys。
func ExtractShape(doc []byte) (RuleShape, error) {
	out := RuleShape{CIDRs: []string{}, DomainKeys: []string{}, OtherKeys: []string{}}
	if !json.Valid(doc) {
		var v any
		err := json.Unmarshal(doc, &v)
		if err == nil {
			err = errors.New("不是合法的 JSON")
		}
		return out, err
	}
	_, top, ok := orderedObject(doc)
	if !ok {
		return out, nil
	}
	rawRules, has := top["rules"]
	if !has || strings.TrimSpace(string(rawRules)) == "" || strings.TrimSpace(string(rawRules))[0] != '[' {
		return out, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawRules, &items); err != nil {
		return out, err
	}
	for _, it := range items {
		s := strings.TrimSpace(string(it))
		if s == "" {
			continue
		}
		switch s[0] {
		case '[': // 数组元素:不是 logical,键是下标
			var elems []json.RawMessage
			if json.Unmarshal(it, &elems) != nil {
				continue
			}
			for i := range elems {
				out.OtherKeys = addOnce(out.OtherKeys, strconv.Itoa(i))
			}
		case '{':
			keys, vals, ok := orderedObject(it)
			if !ok {
				continue
			}
			if t, has := vals["type"]; has && strings.TrimSpace(string(t)) == `"logical"` {
				out.Unbounded = true
				continue
			}
			if r, has := vals["rules"]; has && jsTruthy(r) {
				out.Unbounded = true
				continue
			}
			if v, has := vals["invert"]; has && jsTruthy(v) {
				out.Unbounded = true
				continue
			}
			for _, k := range keys {
				switch {
				case k == "ip_cidr" || k == "type":
				case shapeDomainKeys[k]:
					out.DomainKeys = addOnce(out.DomainKeys, k)
				default:
					out.OtherKeys = addOnce(out.OtherKeys, k)
				}
			}
			cidr, has := vals["ip_cidr"]
			out.CIDRs = append(out.CIDRs, jsList(cidr, has)...)
		}
	}
	return out, nil
}

// Revisioner 是 FS 的可选能力:给文件一个「内容没变就不变」的修订号,形状缓存靠它判断规则库文件有没有被换掉。
type Revisioner interface {
	FileRevision(path string) (string, error)
}

type shapeEntry struct {
	revision string
	shape    RuleShape
}

// ShapeReader 对应上游 Ru(含 Pm):取一个规则集的形状。
// 只有随包规则库(GeoDir 下)的文件、且 FS 能给出修订号时才缓存:缓存项带修订号,文件换了就作废;
// 网段为 0 或超过 32768 的不缓存,所有缓存项的网段合计不超过 32768、最多 32 份,超了先丢最早放进去的。
// 返回的形状是副本,调用方可以随便改。
type ShapeReader struct {
	FS        FS
	GeoDir    string                                           // 随包规则库目录
	TmpDir    string                                           // 还原用的临时 JSON 放这里(<DataDir>/tmp)
	SrsPath   func(tag string) (string, error)                 // RuleSetPathFunc
	Decompile func(ctx context.Context, srs, out string) error // SingBoxDecompiler

	mu    sync.Mutex
	order []string
	cache map[string]shapeEntry
}

// Shape 返回 tag 的形状。出错时错误文案是给人看的原因:「本地没有这份规则集文件」「解码失败:…」「解码结果读不出来:…」。
func (r *ShapeReader) Shape(ctx context.Context, tag string) (RuleShape, error) {
	srs, err := r.SrsPath(tag)
	if err != nil {
		return RuleShape{}, err
	}
	rev := r.revision(srs)
	if rev != "" {
		r.mu.Lock()
		if e, ok := r.cache[srs]; ok {
			r.dropLocked(srs)
			if e.revision == rev {
				r.putLocked(srs, e)
				r.mu.Unlock()
				return e.shape.clone(), nil
			}
		}
		r.mu.Unlock()
	}
	doc, err := r.decode(ctx, tag, srs)
	if err != nil {
		return RuleShape{}, err
	}
	shape, err := ExtractShape(doc)
	if err != nil {
		return RuleShape{}, fmt.Errorf("解码结果读不出来:%v", err)
	}
	if rev != "" && len(shape.CIDRs) > 0 && len(shape.CIDRs) <= shapeCIDRLimit && r.revision(srs) == rev {
		r.mu.Lock()
		r.dropLocked(srs)
		r.putLocked(srs, shapeEntry{revision: rev, shape: shape.clone()})
		total := 0
		for _, k := range r.order {
			total += len(r.cache[k].shape.CIDRs)
		}
		for len(r.order) > 0 && (total > shapeCIDRLimit || len(r.order) > shapeCacheMax) {
			oldest := r.order[0]
			total -= len(r.cache[oldest].shape.CIDRs)
			r.dropLocked(oldest)
		}
		r.mu.Unlock()
	}
	return shape, nil
}

func (r *ShapeReader) revision(srs string) string {
	if filepath.Dir(srs) != filepath.Clean(r.GeoDir) {
		return ""
	}
	rv, ok := r.FS.(Revisioner)
	if !ok {
		return ""
	}
	v, err := rv.FileRevision(srs)
	if err != nil {
		return ""
	}
	return v
}

func (r *ShapeReader) dropLocked(k string) {
	if _, ok := r.cache[k]; !ok {
		return
	}
	delete(r.cache, k)
	for i, x := range r.order {
		if x == k {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

func (r *ShapeReader) putLocked(k string, e shapeEntry) {
	if r.cache == nil {
		r.cache = map[string]shapeEntry{}
	}
	r.dropLocked(k)
	r.cache[k] = e
	r.order = append(r.order, k)
}

// decode 对应上游 Pm:文件在不在 → 还原成临时 JSON → 读回 → 删临时文件。
func (r *ShapeReader) decode(ctx context.Context, tag, srs string) ([]byte, error) {
	if !r.FS.Exists(srs) {
		return nil, errors.New("本地没有这份规则集文件")
	}
	if err := r.FS.MkdirAll(r.TmpDir); err != nil {
		return nil, fmt.Errorf("解码结果读不出来:%v", err)
	}
	tmp := filepath.Join(r.TmpDir, tag+"."+randomHex(8)+".shape.json")
	defer func() { _ = r.FS.Remove(tmp) }()
	if err := r.Decompile(ctx, srs, tmp); err != nil {
		return nil, fmt.Errorf("解码失败:%v", err)
	}
	raw, err := r.FS.ReadFile(tmp)
	if err != nil {
		return nil, fmt.Errorf("解码结果读不出来:%v", err)
	}
	return raw, nil
}

func maxAddr(family int) *big.Int {
	m := new(big.Int).Lsh(big.NewInt(1), uint(familyBits(family)))
	return m.Sub(m, big.NewInt(1))
}

// lastAddrCIDR 对应上游 AM:返回列表里第一条「一直到地址空间末尾」的网段原文,没有就返回空串。
func lastAddrCIDR(cidrs []string) string {
	for _, s := range cidrs {
		if c, ok := ParseCIDR(s); ok {
			if r := c.Range(); r.End.Cmp(maxAddr(r.Family)) == 0 {
				return s
			}
		}
	}
	return ""
}

// describeRanges 对应上游 xM:前三段写成「起点…」用顿号连起来,多于三段再加「 等 N 段」。
func describeRanges(rs []Range) string {
	n := len(rs)
	if n > 3 {
		n = 3
	}
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = FormatIP(rs[i].Family, rs[i].Start) + "…"
	}
	out := strings.Join(parts, "、")
	if len(rs) > 3 {
		out += fmt.Sprintf(" 等 %d 段", len(rs))
	}
	return out
}

// FallbackKeepIPv4 对应上游 Nu:「兜底 IPv4 直连」在入口放行的网段 = 假 IP 池、0/8、127/8、169.254/16、224/3、
// extra(调用方给的保留段,上游总是传 benchmark 旁边那段加假 IP 池)、needCIDRs,再加 needSets 里每个规则集的网段,合并后只留 IPv4。
// 任何一个规则集取不到 / 说不清 / 含域名或别的条件 / 一个网段都没有,或任何一个网段解析不了,都返回空(不放行)。
func FallbackKeepIPv4(ctx context.Context, shape ShapeFunc, needSets, needCIDRs, extra []string) []string {
	list := []string{fakeIPv4Pool, "0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/3"}
	list = append(list, extra...)
	list = append(list, needCIDRs...)
	for _, tag := range needSets {
		sh, err := shape(ctx, tag)
		if err != nil || sh.Unbounded || len(sh.DomainKeys) > 0 || len(sh.OtherKeys) > 0 || len(sh.CIDRs) == 0 {
			return []string{}
		}
		list = append(list, sh.CIDRs...)
	}
	var v4 []Range
	for _, s := range list {
		c, ok := ParseCIDR(s)
		if !ok {
			return []string{}
		}
		if r := c.Range(); r.Family == 4 {
			v4 = append(v4, r)
		}
	}
	out := RangesToCIDRs(MergeRanges(v4))
	if out == nil {
		out = []string{}
	}
	return out
}
