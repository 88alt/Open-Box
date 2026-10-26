package subs

import (
	"math"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// 改名规则对应前端 OpenboxRenameOptions(panel/src/api/openbox.ts)和规则编辑器
// (components/subscription/RenameRulesEditor.vue)。前端是规格:
//   - 过滤关键词命中原名 → 整条不导入;逐条禁用(按原名)→ 不导入也不占序号。
//   - 地区:regionDict 按顺序找第一个有关键词命中原名的条目,顺序即优先级;命中后记下它的国家代码(regionCode),
//     重命名总开关关掉时也照常识别(国旗、按地区选成员的节点组要用)。
//   - 特征:featureKeywords 里第一个命中的词,转大写写进名字(只取第一个)。旧档案的 featureDict 会被扁平化。
//   - 命名:template 里的 {region} {feature} {seq};没有特征时 {feature} 连同相邻一个「-」一起去掉;
//     没识别出地区用 unknownLabel;序号在同一个(地区, 特征)内从 1 数,按 seqPad 补零;手工改过名的节点不参与编号。
//   - 订阅名前缀:usePrefix 时在最前面加「订阅名 | 」。
//   - 节点名必须全局唯一(内核出站重名会报错):重名的加序号而不是拒绝。
// 关键词一律不区分大小写;只有字母数字、不超过 3 个字符的 ASCII 词(hk、us、2x ...)要求前后不是字母,
// 免得 us 命中 Russia、de 命中 Sweden。

const (
	defaultTemplate     = "{region}-{feature}-{seq}"
	defaultUnknownLabel = "其他"
	defaultSeqPad       = 2
	maxSeqPad           = 6
)

type regionEntry struct {
	code     string
	name     string
	keywords []string
}

// 与前端 rename-defaults.ts 的默认值一致:前端在没存过规则时用它们预填编辑器,后端对缺失的项用同一份。
var (
	defaultRegionDict = []regionEntry{
		{"US", "美国", []string{"us", "united states", "america", "美国", "美國", "洛杉矶", "洛杉磯", "硅谷", "圣何塞", "西雅图", "纽约"}},
		{"HK", "香港", []string{"hk", "hong kong", "hongkong", "香港", "深港"}},
		{"JP", "日本", []string{"jp", "japan", "日本", "东京", "東京", "大阪"}},
		{"SG", "新加坡", []string{"sg", "singapore", "新加坡", "狮城", "獅城"}},
		{"TW", "台湾", []string{"tw", "taiwan", "台湾", "台灣", "臺灣", "台北"}},
		{"KR", "韩国", []string{"kr", "korea", "韩国", "韓國", "首尔", "首爾"}},
		{"GB", "英国", []string{"uk", "gb", "united kingdom", "britain", "英国", "英國", "伦敦", "倫敦"}},
		{"DE", "德国", []string{"de", "germany", "德国", "德國", "法兰克福", "法蘭克福"}},
		// 回国 / 中转类排最后:带地区词的节点(「美国 CN2 GIA」)先命中前面的地区
		{"CN", "中国", []string{"cn", "china", "中国", "中國", "回国", "回國", "back to china"}},
	}
	defaultFeatureKeywords = []string{"iepl", "iplc", "ipv6", "专线", "家宽", "2x"}
	defaultExcludeKeywords = []string{"官网", "工单", "客服"}
)

type renameOpts struct {
	enabled         bool
	template        string
	unknownLabel    string
	seqPad          int
	regions         []regionEntry
	featureKeywords []string
	excludeKeywords []string
	disabled        map[string]bool
	overrides       map[string]string
	usePrefix       bool
}

func strList(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, e := range a {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

// featureList 读特征关键词:featureKeywords 有内容就用它,否则把旧的 featureDict([{label, keywords}])扁平化,
// 都没有才用默认值。
func featureList(m map[string]any) []string {
	if l := strList(m["featureKeywords"]); len(l) > 0 {
		return l
	}
	var flat []string
	if a, ok := m["featureDict"].([]any); ok {
		for _, e := range a {
			switch t := e.(type) {
			case string:
				if s := strings.TrimSpace(t); s != "" {
					flat = append(flat, s)
				}
			case map[string]any:
				flat = append(flat, strList(t["keywords"])...)
			}
		}
	}
	if len(flat) > 0 {
		return flat
	}
	// 显式给了空数组就是「不加特征」,只有字段缺失才回落到默认
	if _, has := m["featureKeywords"]; has {
		if a, ok := m["featureKeywords"].([]any); ok && len(a) == 0 {
			return nil
		}
	}
	if _, has := m["featureDict"]; has {
		return nil
	}
	return append([]string(nil), defaultFeatureKeywords...)
}

func regionList(m map[string]any) []regionEntry {
	a, ok := m["regionDict"].([]any)
	if !ok {
		return defaultRegionDict
	}
	out := []regionEntry{}
	for _, e := range a {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		r := regionEntry{keywords: strList(em["keywords"])}
		r.code, _ = em["code"].(string)
		r.code = strings.ToUpper(strings.TrimSpace(r.code))
		r.name, _ = em["name"].(string)
		r.name = strings.TrimSpace(r.name)
		if len(r.keywords) == 0 {
			continue
		}
		out = append(out, r)
	}
	return out
}

func parseRenameOpts(m map[string]any) renameOpts {
	o := renameOpts{
		enabled: true, disabled: map[string]bool{}, overrides: map[string]string{},
		template: defaultTemplate, unknownLabel: defaultUnknownLabel, seqPad: defaultSeqPad,
	}
	if b, ok := m["enabled"].(bool); ok {
		o.enabled = b
	}
	if s, ok := m["template"].(string); ok && strings.TrimSpace(s) != "" {
		o.template = strings.TrimSpace(s)
	}
	if s, ok := m["unknownLabel"].(string); ok && strings.TrimSpace(s) != "" {
		o.unknownLabel = strings.TrimSpace(s)
	}
	if f, ok := m["seqPad"].(float64); ok && f >= 1 && !math.IsInf(f, 0) {
		o.seqPad = int(math.Min(math.Floor(f), maxSeqPad))
	}
	o.regions = regionList(m)
	o.featureKeywords = featureList(m)
	// 过滤关键词:显式给了空数组就是「不过滤」,字段缺失才用默认(默认值保守,只有三个没歧义的词)
	if _, has := m["excludeKeywords"]; has {
		o.excludeKeywords = strList(m["excludeKeywords"])
	} else {
		o.excludeKeywords = append([]string(nil), defaultExcludeKeywords...)
	}
	for _, d := range strList(m["disabled"]) {
		o.disabled[d] = true
	}
	if ov, ok := m["overrides"].(map[string]any); ok {
		for k, v := range ov {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				o.overrides[k] = strings.TrimSpace(s)
			}
		}
	}
	if b, ok := m["usePrefix"].(bool); ok {
		o.usePrefix = b
	}
	return o
}

// Applied 是改名 / 过滤之后的结果。
type Applied struct {
	Nodes    []sub.Node
	Excluded []string // 被过滤关键词剔除的原名
	Disabled []string // 被逐条禁用的原名
	Preview  []PreviewEntry
}

// PreviewEntry 对应前端 OpenboxRenamePreviewEntry。
type PreviewEntry struct {
	OriginalTag string `json:"originalTag"`
	NewTag      string `json:"newTag"`
	RegionCode  string `json:"regionCode,omitempty"`
}

// apply 依次做:过滤关键词 → 逐条禁用 → 识别地区 / 特征 → 手工改名或按模板命名 → 订阅名前缀 → 去重。
func apply(nodes []sub.Node, opts renameOpts, subName string, taken map[string]bool) Applied {
	res := Applied{Nodes: []sub.Node{}, Preview: []PreviewEntry{}}
	used := map[string]bool{}
	seqs := map[string]int{}
	for _, n := range nodes {
		orig := n.OriginalTag
		if orig == "" {
			orig = n.Tag
		}
		if hitsKeyword(orig, opts.excludeKeywords) {
			res.Excluded = append(res.Excluded, orig)
			continue
		}
		if opts.disabled[orig] {
			res.Disabled = append(res.Disabled, orig)
			continue
		}
		lower := strings.ToLower(orig)
		region := matchRegion(lower, opts.regions)

		tag := orig
		if ov, ok := opts.overrides[orig]; ok {
			tag = ov // 改过名的不参与序号编号
		} else if opts.enabled {
			label := opts.unknownLabel
			if region != nil {
				if label = region.name; label == "" {
					label = region.code
				}
				if label == "" {
					label = opts.unknownLabel
				}
			}
			feature := ""
			for _, kw := range opts.featureKeywords {
				if hasKeyword(lower, strings.ToLower(kw)) {
					feature = strings.ToUpper(kw)
					break
				}
			}
			key := label + "\x00" + feature
			seqs[key]++
			seq := padSeq(seqs[key], opts.seqPad)
			if t := renderTemplate(opts.template, label, feature, seq); t != "" {
				tag = t
			}
		}
		if opts.usePrefix && strings.TrimSpace(subName) != "" {
			tag = strings.TrimSpace(subName) + " | " + tag
		}
		tag = unique(tag, used, taken)
		used[tag] = true
		n.OriginalTag = orig
		n.Tag = tag
		n.RegionCode, n.RegionName = "", ""
		code := ""
		if region != nil {
			code = region.code
			n.RegionCode, n.RegionName = code, region.name
		}
		res.Nodes = append(res.Nodes, n)
		res.Preview = append(res.Preview, PreviewEntry{OriginalTag: orig, NewTag: tag, RegionCode: code})
	}
	return res
}

func padSeq(n, pad int) string {
	s := strconv.Itoa(n)
	for len(s) < pad {
		s = "0" + s
	}
	return s
}

// renderTemplate 把 {region} {feature} {seq} 代入模板。值为空的记号连同相邻的一个「-」去掉,
// 免得出现「美国--01」。
func renderTemplate(tpl, region, feature, seq string) string {
	for _, t := range []struct{ tok, val string }{{"{region}", region}, {"{feature}", feature}, {"{seq}", seq}} {
		if t.val == "" {
			tpl = dropToken(tpl, t.tok)
			continue
		}
		tpl = strings.ReplaceAll(tpl, t.tok, t.val)
	}
	return strings.TrimSpace(tpl)
}

func dropToken(tpl, tok string) string {
	for {
		i := strings.Index(tpl, tok)
		if i < 0 {
			return tpl
		}
		end := i + len(tok)
		switch {
		case i > 0 && tpl[i-1] == '-':
			tpl = tpl[:i-1] + tpl[end:]
		case strings.HasPrefix(tpl[end:], "-"):
			tpl = tpl[:i] + tpl[end+1:]
		default:
			tpl = tpl[:i] + tpl[end:]
		}
	}
}

func matchRegion(lowerName string, regions []regionEntry) *regionEntry {
	for i := range regions {
		for _, kw := range regions[i].keywords {
			if hasKeyword(lowerName, strings.ToLower(kw)) {
				return &regions[i]
			}
		}
	}
	return nil
}

func hitsKeyword(name string, kws []string) bool {
	ln := strings.ToLower(name)
	for _, k := range kws {
		if hasKeyword(ln, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

func shortASCII(kw string) bool {
	if len(kw) == 0 || len(kw) > 3 {
		return false
	}
	for i := 0; i < len(kw); i++ {
		if !isLetter(kw[i]) && !isDigit(kw[i]) {
			return false
		}
	}
	return true
}

// hasKeyword:name 和 kw 都已转小写。短 ASCII 词要求两侧不是字母(词本身以数字开头 / 结尾时,该侧也不能是数字)。
func hasKeyword(name, kw string) bool {
	if kw == "" {
		return false
	}
	if !shortASCII(kw) {
		return strings.Contains(name, kw)
	}
	for from := 0; from < len(name); {
		i := strings.Index(name[from:], kw)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(kw)
		ok := true
		if i > 0 {
			c := name[i-1]
			ok = !isLetter(c) && !(isDigit(c) && isDigit(kw[0]))
		}
		if ok && end < len(name) {
			c := name[end]
			ok = !isLetter(c) && !(isDigit(c) && isDigit(kw[len(kw)-1]))
		}
		if ok {
			return true
		}
		from = i + 1
	}
	return false
}

func unique(tag string, used, taken map[string]bool) string {
	if !used[tag] && !taken[tag] {
		return tag
	}
	for i := 2; ; i++ {
		c := tag + " " + strconv.Itoa(i)
		if !used[c] && !taken[c] {
			return c
		}
	}
}
