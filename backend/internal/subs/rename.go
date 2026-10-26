package subs

import (
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// RenameOptions 是前端 OpenboxRenameOptions 里目前后端认的那部分。
// 其余键(regionDict / featureKeywords / template ...)原样存取、不丢,但还不参与改名(见 STATUS「订阅改名规则」)。
type renameOpts struct {
	enabled         bool
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

func parseRenameOpts(m map[string]any) renameOpts {
	o := renameOpts{enabled: true, disabled: map[string]bool{}, overrides: map[string]string{}}
	if b, ok := m["enabled"].(bool); ok {
		o.enabled = b
	}
	o.excludeKeywords = strList(m["excludeKeywords"])
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

// apply 依次做:过滤关键词 → 逐条禁用 → 手工改名 → 订阅名前缀 → 去重(同订阅内、以及与其它订阅的节点重名)。
// 节点名必须全局唯一:内核出站重名会直接报错,所以重名的加序号而不是拒绝。
func apply(nodes []sub.Node, opts renameOpts, subName string, taken map[string]bool) Applied {
	res := Applied{Nodes: []sub.Node{}, Preview: []PreviewEntry{}}
	used := map[string]bool{}
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
		tag := orig
		if ov, ok := opts.overrides[orig]; ok {
			tag = ov
		}
		if opts.usePrefix && strings.TrimSpace(subName) != "" {
			tag = strings.TrimSpace(subName) + " | " + tag
		}
		tag = unique(tag, used, taken)
		used[tag] = true
		n.OriginalTag = orig
		n.Tag = tag
		res.Nodes = append(res.Nodes, n)
		res.Preview = append(res.Preview, PreviewEntry{OriginalTag: orig, NewTag: tag})
	}
	return res
}

func hitsKeyword(name string, kws []string) bool {
	ln := strings.ToLower(name)
	for _, k := range kws {
		if strings.Contains(ln, strings.ToLower(k)) {
			return true
		}
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
