package dnsfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// 名单「应用」的产物。自己的设计,依据 core 的 dnsFilter 入参约定(core/internal/dnsfilter 的包注释和测试数据里的形状)
// 和 sing-box 官方文档的规则集格式,没有参照上游:
//
//   - 每份启用的名单按「拦截 / 放行 × 是否 $important」分成最多四组,每组编成一个 .srs 规则集(域名精确、域名后缀、域名正则三个字段);
//   - 用户的放行域名(allowDomains)另编一个规则集;
//   - core 拿到 {key, sets, blocks, allow, allowImportant, userAllow},生成「命中拦截集且不在放行集里就回 NXDOMAIN」的 DNS 规则。

// TagPrefix 是过滤规则集 tag 和文件名的共同前缀;清理旧文件时只认这个前缀。
const TagPrefix = "dns-filter-"

// 一组条目的种类,参与 tag 的哈希,免得同一份名单里内容相同的不同种类撞 tag。
const (
	kindBlock          = "b"
	kindBlockImportant = "bi"
	kindAllow          = "a"
	kindAllowImportant = "ai"
	kindUser           = "u"
)

// Group 是要编成一个 .srs 的一组条目。
type Group struct {
	Tag  string
	Rule map[string]any // sing-box 规则集源码里的一条规则:domain / domain_suffix / domain_regex
	N    int            // 条目数
}

// Block 对应 core 的 blocks 项。
type Block struct {
	Tag       string `json:"tag"`
	Important bool   `json:"important"`
}

// Artifact 是一次应用编出来的全部内容。
type Artifact struct {
	Key            string
	Groups         []Group
	Blocks         []Block
	Allow          []string
	AllowImportant []string
	UserAllow      []string
}

// SettingsKey 是设置的指纹:只看会改变过滤结果的部分(启用的名单的 id 和地址,以及放行域名),
// 开关、名单名字、自动更新计划不参与。core 用它判断「产物是不是按当前设置编的」。
func SettingsKey(s Settings) string {
	type listKey struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	v := struct {
		Lists []listKey `json:"lists"`
		Allow []string  `json:"allow"`
	}{Lists: []listKey{}, Allow: []string{}}
	for _, l := range s.Lists {
		if l.Enabled {
			v.Lists = append(v.Lists, listKey{l.ID, l.URL})
		}
	}
	v.Allow = append(v.Allow, s.AllowDomains...)
	sort.Strings(v.Allow)
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Compile 把各份名单的解析结果和设置里的放行域名编成产物。parsed 的键是名单 id,没有数据的名单直接略过。
// 按设置里名单的顺序处理,结果与 map 遍历顺序无关。
func Compile(s Settings, parsed map[string]*Parsed) *Artifact {
	a := &Artifact{Key: SettingsKey(s), Groups: []Group{}, Blocks: []Block{}, Allow: []string{}, AllowImportant: []string{}, UserAllow: []string{}}
	seen := map[string]bool{}
	add := func(g Group) bool {
		if g.N == 0 || seen[g.Tag] {
			return false
		}
		seen[g.Tag] = true
		a.Groups = append(a.Groups, g)
		return true
	}
	for _, l := range s.Lists {
		p := parsed[l.ID]
		if !l.Enabled || p == nil {
			continue
		}
		by := map[string]map[string][]string{}
		for _, e := range p.Entries {
			kind := kindBlock
			switch {
			case e.Action == ActionAllow && e.Important:
				kind = kindAllowImportant
			case e.Action == ActionAllow:
				kind = kindAllow
			case e.Important:
				kind = kindBlockImportant
			}
			if e.Type != TypeDomain && e.Type != TypeDomainSuffix && e.Type != TypeDomainRegex {
				continue
			}
			if by[kind] == nil {
				by[kind] = map[string][]string{}
			}
			by[kind][e.Type] = append(by[kind][e.Type], e.Value)
		}
		for _, kind := range []string{kindBlockImportant, kindBlock, kindAllowImportant, kindAllow} {
			g := makeGroup(l.ID, kind, by[kind])
			if !add(g) {
				continue
			}
			switch kind {
			case kindBlockImportant:
				a.Blocks = append(a.Blocks, Block{Tag: g.Tag, Important: true})
			case kindBlock:
				a.Blocks = append(a.Blocks, Block{Tag: g.Tag})
			case kindAllowImportant:
				a.AllowImportant = append(a.AllowImportant, g.Tag)
			case kindAllow:
				a.Allow = append(a.Allow, g.Tag)
			}
		}
	}
	// 用户放行:"*.x" 是 x 和它所有子域名(domain_suffix),其余是精确域名。
	user := map[string][]string{}
	for _, d := range s.AllowDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		switch {
		case d == "":
		case strings.HasPrefix(d, "*."):
			user[TypeDomainSuffix] = append(user[TypeDomainSuffix], strings.TrimPrefix(d, "*."))
		default:
			user[TypeDomain] = append(user[TypeDomain], d)
		}
	}
	if g := makeGroup("user", kindUser, user); add(g) {
		a.UserAllow = append(a.UserAllow, g.Tag)
	}
	return a
}

// makeGroup 把一组条目排序去重后拼成一条规则;tag 由 id、种类和内容决定,内容不变 tag 就不变(文件不用重编)。
func makeGroup(id, kind string, fields map[string][]string) Group {
	rule := map[string]any{}
	n := 0
	h := sha256.New()
	h.Write([]byte(kind))
	for _, f := range []string{TypeDomain, TypeDomainSuffix, TypeDomainRegex} {
		vals := uniqSorted(fields[f])
		if len(vals) == 0 {
			continue
		}
		rule[f] = vals
		n += len(vals)
		h.Write([]byte{0})
		h.Write([]byte(f))
		for _, v := range vals {
			h.Write([]byte{0})
			h.Write([]byte(v))
		}
	}
	if n == 0 {
		return Group{}
	}
	return Group{Tag: TagPrefix + id + "-" + hex.EncodeToString(h.Sum(nil)[:8]), Rule: rule, N: n}
}

func uniqSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	j := 0
	for i, v := range out {
		if i == 0 || v != out[j-1] {
			out[j] = v
			j++
		}
	}
	return out[:j]
}

// ForCore 拼成 core 的 dnsFilter 入参。dir 是规则集目录:每个规则集是 <dir>/<tag>.srs。
// 所有数组都不为 nil。
func (a *Artifact) ForCore(dir string) map[string]any {
	sets := make([]any, 0, len(a.Groups))
	for _, g := range a.Groups {
		sets = append(sets, map[string]any{"type": "local", "tag": g.Tag, "format": "binary", "path": filepath.Join(dir, g.Tag+".srs")})
	}
	blocks := make([]any, 0, len(a.Blocks))
	for _, b := range a.Blocks {
		blocks = append(blocks, map[string]any{"tag": b.Tag, "important": b.Important})
	}
	strs := func(in []string) []any {
		out := make([]any, 0, len(in))
		for _, s := range in {
			out = append(out, s)
		}
		return out
	}
	return map[string]any{
		"key": a.Key, "sets": sets, "blocks": blocks,
		"allow": strs(a.Allow), "allowImportant": strs(a.AllowImportant), "userAllow": strs(a.UserAllow),
	}
}
