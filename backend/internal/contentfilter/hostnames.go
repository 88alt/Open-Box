package contentfilter

import (
	"regexp"
	"strings"
)

var (
	appendPrefix = regexp.MustCompile(`(?i)^\s*%(?:APPEND|INSERT)%\s*`)
	hostnameKV   = regexp.MustCompile(`(?i)^hostname\s*=\s*(.*)$`)
)

// Hostnames 取出需要解密(MITM)的主机名:逗号分隔、去掉 `%APPEND%` / `%INSERT%` 前缀、小写、去重,保持首次出现的顺序。
// 对齐上游 v0.1.402:
//   - Surge / Shadowrocket / Loon 只认 `[MITM]` 段里的 `hostname =` 行;
//   - Quantumult X 认 `[mitm]` 段和没有段头的行里的 `hostname =`(它的 `[rewrite_local]` 里不会有);
//   - Stash 取 `http:` 块下 `mitm:` 列表的每一项(整项当一个主机名,不按逗号拆)。
//
// 文本读不懂(Stash 的 YAML 不合格)时返回空,规则解析会先报同一个错。
func Hostnames(d Dialect, text string) []string {
	var list []string
	if d == Stash {
		doc, err := parseStashDoc(text)
		if err != nil {
			return []string{}
		}
		for _, it := range doc.items["mitm"] {
			list = append(list, it.value)
		}
		return dedupHostnames(list)
	}
	for _, it := range sections(text) {
		if d == QuantumultX {
			if it.section != "mitm" && it.section != "" {
				continue
			}
		} else if it.section != "mitm" {
			continue
		}
		m := hostnameKV.FindStringSubmatch(it.text)
		if m == nil {
			continue
		}
		for _, h := range strings.Split(m[1], ",") {
			list = append(list, strings.ToLower(trimJS(appendPrefix.ReplaceAllString(h, ""))))
		}
	}
	return dedupHostnames(list)
}

func dedupHostnames(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, h := range in {
		h = strings.ToLower(trimJS(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}
