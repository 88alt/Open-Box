package sub

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 只为 Clash / Mihomo 订阅写的 YAML 子集解析器(没有第三方 YAML 库可用,见 STATUS)。
//
// 支持:块映射、块序列(含「- key: v」内联映射、与父键同缩进的序列)、流式 {..} / [..](可跨行)、
// 单双引号字符串、注释、锚点 &a / 别名 *a / 合并键 <<。
// 不支持(报错):多行文本 | 和 >、tab 缩进、多文档、复杂键(? )。
// 标量一律保持字符串,只有 true / false 变成布尔、null / ~ / 空变成 nil;
// 密码、uuid 这类可能长得像数字的值因此不会丢前导零,取数字时由调用方自己转换。

const (
	yamlMaxDepth = 64
	yamlMaxFlow  = 32
)

type yamlLine struct {
	no     int // 原文行号(从 1 开始),给错误信息用
	indent int
	text   string
}

type yamlParser struct {
	lines   []yamlLine
	pos     int
	depth   int
	anchors map[string]any
}

// parseYAML 解析 src,返回 map[string]any / []any / string / bool / nil。
func parseYAML(src string) (any, error) {
	lines, err := yamlPrepare(src)
	if err != nil {
		return nil, err
	}
	p := &yamlParser{lines: lines, anchors: map[string]any{}}
	if len(lines) == 0 {
		return nil, nil
	}
	v, err := p.block(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, p.errf("缩进不对或内容无法识别")
	}
	return v, nil
}

func (p *yamlParser) errf(format string, a ...any) error {
	no := 0
	if p.pos < len(p.lines) {
		no = p.lines[p.pos].no
	} else if len(p.lines) > 0 {
		no = p.lines[len(p.lines)-1].no
	}
	return fmt.Errorf("第 %d 行:%s", no, fmt.Sprintf(format, a...))
}

// ---- 预处理:去注释、算缩进、把跨行的流式集合并成一行 ----

func yamlPrepare(src string) ([]yamlLine, error) {
	src = strings.TrimPrefix(src, "\xef\xbb\xbf")
	raw := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []yamlLine
	for i := 0; i < len(raw); i++ {
		s := strings.TrimRight(stripYAMLComment(strings.TrimRight(raw[i], "\r")), " \t")
		if strings.TrimSpace(s) == "" {
			continue
		}
		if s == "---" || s == "..." {
			if len(out) > 0 {
				return nil, fmt.Errorf("第 %d 行:不支持多文档", i+1)
			}
			continue
		}
		indent := 0
		for indent < len(s) && s[indent] == ' ' {
			indent++
		}
		if indent < len(s) && s[indent] == '\t' {
			return nil, fmt.Errorf("第 %d 行:缩进里有 tab", i+1)
		}
		text := s[indent:]
		first := i + 1
		for flowDepth(text) > 0 && i+1 < len(raw) {
			i++
			next := strings.TrimSpace(stripYAMLComment(raw[i]))
			if next != "" {
				text += " " + next
			}
		}
		if flowDepth(text) > 0 {
			return nil, fmt.Errorf("第 %d 行:括号没有闭合", first)
		}
		out = append(out, yamlLine{no: first, indent: indent, text: text})
	}
	return out, nil
}

// walkYAML 逐字符遍历。引号只在值的开头(前一个字符是空白或分隔符)才算开引号,
// 这样「it's」「a#b」这类普通字符串里的符号不会打乱判断。f 返回 false 时停止。
func walkYAML(s string, f func(i int, c byte, out bool) bool) {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch q {
		case 0:
			if (c == '"' || c == '\'') && (i == 0 || strings.IndexByte(" \t[{,:", s[i-1]) >= 0) {
				q = c
				if !f(i, c, false) {
					return
				}
				continue
			}
			if !f(i, c, true) {
				return
			}
		case '"':
			if c == '\\' && i+1 < len(s) {
				if !f(i, c, false) || !f(i+1, s[i+1], false) {
					return
				}
				i++
				continue
			}
			if c == '"' {
				q = 0
			}
			if !f(i, c, false) {
				return
			}
		case '\'':
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					if !f(i, c, false) || !f(i+1, c, false) {
						return
					}
					i++
					continue
				}
				q = 0
			}
			if !f(i, c, false) {
				return
			}
		}
	}
}

func stripYAMLComment(s string) string {
	cut := -1
	walkYAML(s, func(i int, c byte, out bool) bool {
		if out && c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			cut = i
			return false
		}
		return true
	})
	if cut >= 0 {
		return s[:cut]
	}
	return s
}

// flowDepth 返回引号外未闭合的 { [ 个数(只在值里的括号才算,行首的「- 」之类不影响)。
func flowDepth(s string) int {
	d := 0
	walkYAML(s, func(i int, c byte, out bool) bool {
		if !out {
			return true
		}
		switch c {
		case '{', '[':
			if i == 0 || strings.IndexByte(" \t[{,:", s[i-1]) >= 0 {
				d++
			}
		case '}', ']':
			if d > 0 {
				d--
			}
		}
		return true
	})
	return d
}

// ---- 块结构 ----

func isSeqItem(t string) bool { return t == "-" || strings.HasPrefix(t, "- ") }

// splitKey 把「key: rest」拆开。以 { [ 开头的不是映射;冒号必须后跟空格或行尾,且在引号外。
func splitKey(t string) (key, rest string, ok bool) {
	if t == "" || t[0] == '{' || t[0] == '[' {
		return "", "", false
	}
	idx := -1
	walkYAML(t, func(i int, c byte, out bool) bool {
		if out && c == ':' && (i+1 == len(t) || t[i+1] == ' ') {
			idx = i
			return false
		}
		return true
	})
	if idx <= 0 {
		return "", "", false
	}
	k := strings.TrimSpace(t[:idx])
	if len(k) >= 2 && (k[0] == '"' || k[0] == '\'') {
		s, n, err := yamlQuoted(k, 0)
		if err != nil || n != len(k) {
			return "", "", false
		}
		k = s
	}
	return k, strings.TrimSpace(t[idx+1:]), true
}

func (p *yamlParser) block(minIndent int) (any, error) {
	if p.pos >= len(p.lines) || p.lines[p.pos].indent < minIndent {
		return nil, nil
	}
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > yamlMaxDepth {
		return nil, p.errf("嵌套太深")
	}
	ln := p.lines[p.pos]
	if isSeqItem(ln.text) {
		return p.seq(ln.indent)
	}
	if _, _, ok := splitKey(ln.text); ok {
		return p.mapping(ln.indent)
	}
	p.pos++
	return p.value(ln.text)
}

func (p *yamlParser) seq(indent int) (any, error) {
	out := []any{}
	for p.pos < len(p.lines) && p.lines[p.pos].indent == indent && isSeqItem(p.lines[p.pos].text) {
		ln := p.lines[p.pos]
		after := ln.text[1:]
		rest := strings.TrimLeft(after, " ")
		if rest == "" {
			p.pos++
			v, err := p.block(indent + 1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		// 「- key: v」:把剩余部分当作缩进更深的一行重新解析
		p.lines[p.pos] = yamlLine{no: ln.no, indent: indent + 1 + (len(after) - len(rest)), text: rest}
		v, err := p.block(indent + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (p *yamlParser) mapping(indent int) (any, error) {
	m := map[string]any{}
	var merges []any
	for p.pos < len(p.lines) && p.lines[p.pos].indent == indent && !isSeqItem(p.lines[p.pos].text) {
		ln := p.lines[p.pos]
		key, rest, ok := splitKey(ln.text)
		if !ok {
			return nil, p.errf("不是「键: 值」的写法")
		}
		p.pos++
		anchor := ""
		if strings.HasPrefix(rest, "&") {
			name, remain := splitAnchor(rest)
			anchor, rest = name, remain
		}
		var v any
		var err error
		if rest == "" {
			if p.pos < len(p.lines) {
				nl := p.lines[p.pos]
				if nl.indent > indent || (nl.indent == indent && isSeqItem(nl.text)) {
					v, err = p.block(nl.indent)
				}
			}
		} else {
			v, err = p.value(rest)
		}
		if err != nil {
			return nil, err
		}
		if anchor != "" {
			p.anchors[anchor] = v
		}
		if key == "<<" {
			merges = append(merges, v)
			continue
		}
		m[key] = v
	}
	if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
		return nil, p.errf("缩进不对")
	}
	applyMerges(m, merges)
	return m, nil
}

func splitAnchor(s string) (name, rest string) {
	s = s[1:]
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

// applyMerges 实现 YAML 的合并键:显式写出的键优先;多个来源时靠前的优先。
func applyMerges(m map[string]any, merges []any) {
	for _, mv := range merges {
		var srcs []any
		switch t := mv.(type) {
		case map[string]any:
			srcs = []any{t}
		case []any:
			srcs = t
		}
		for _, s := range srcs {
			if sm, ok := s.(map[string]any); ok {
				for k, v := range sm {
					if _, has := m[k]; !has {
						m[k] = v
					}
				}
			}
		}
	}
}

// ---- 值 ----

// value 解析「键:」后面或「- 」后面的一段行内内容。
func (p *yamlParser) value(s string) (any, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "&") {
		name, rest := splitAnchor(s)
		v, err := p.value(rest)
		if err != nil {
			return nil, err
		}
		p.anchors[name] = v
		return v, nil
	}
	switch {
	case s == "":
		return nil, nil
	case s[0] == '{' || s[0] == '[':
		f := &yamlFlow{s: s, p: p}
		v, err := f.value()
		if err != nil {
			return nil, p.errf("%v", err)
		}
		f.skip()
		if f.i != len(f.s) {
			return nil, p.errf("括号后面还有多余内容")
		}
		return v, nil
	case s[0] == '"' || s[0] == '\'':
		str, n, err := yamlQuoted(s, 0)
		if err != nil {
			return nil, p.errf("%v", err)
		}
		if strings.TrimSpace(s[n:]) != "" {
			return nil, p.errf("引号后面还有多余内容")
		}
		return str, nil
	case s[0] == '*':
		v, ok := p.anchors[s[1:]]
		if !ok {
			return nil, p.errf("别名 %s 找不到定义", s)
		}
		return v, nil
	case s[0] == '|' || s[0] == '>':
		return nil, p.errf("不支持多行文本(| 或 >)")
	}
	return yamlPlain(s), nil
}

func yamlPlain(s string) any {
	switch s {
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	case "null", "Null", "NULL", "~":
		return nil
	}
	return strings.TrimPrefix(s, "!!str ")
}

// yamlQuoted 从 s[i] 的引号开始读一个带引号的字符串,返回内容和结束后的下标。
func yamlQuoted(s string, i int) (string, int, error) {
	q := s[i]
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if q == '\'' {
			if c == '\'' {
				if j+1 < len(s) && s[j+1] == '\'' {
					b.WriteByte('\'')
					j++
					continue
				}
				return b.String(), j + 1, nil
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			return b.String(), j + 1, nil
		case '\\':
			j++
			if j >= len(s) {
				return "", 0, fmt.Errorf("引号没有闭合")
			}
			switch e := s[j]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			case '"', '\\', '/', ' ':
				b.WriteByte(e)
			case 'x', 'u', 'U':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if j+n >= len(s) {
					return "", 0, fmt.Errorf("转义序列不完整")
				}
				r, err := strconv.ParseUint(s[j+1:j+1+n], 16, 32)
				if err != nil || !utf8.ValidRune(rune(r)) {
					return "", 0, fmt.Errorf("转义序列不合法")
				}
				b.WriteRune(rune(r))
				j += n
			default:
				return "", 0, fmt.Errorf("不认识的转义 \\%c", e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, fmt.Errorf("引号没有闭合")
}

// ---- 流式 { } [ ] ----

type yamlFlow struct {
	s     string
	i     int
	depth int
	p     *yamlParser
}

func (f *yamlFlow) skip() {
	for f.i < len(f.s) && (f.s[f.i] == ' ' || f.s[f.i] == '\t') {
		f.i++
	}
}

func (f *yamlFlow) value() (any, error) {
	f.skip()
	if f.i >= len(f.s) {
		return nil, fmt.Errorf("内容不完整")
	}
	switch c := f.s[f.i]; c {
	case '{', '[':
		f.depth++
		defer func() { f.depth-- }()
		if f.depth > yamlMaxFlow {
			return nil, fmt.Errorf("嵌套太深")
		}
		if c == '{' {
			return f.mapping()
		}
		return f.seq()
	case '"', '\'':
		s, n, err := yamlQuoted(f.s, f.i)
		f.i = n
		return s, err
	case '&':
		f.i++
		start := f.i
		for f.i < len(f.s) && f.s[f.i] != ' ' && f.s[f.i] != ',' {
			f.i++
		}
		name := f.s[start:f.i]
		v, err := f.value()
		if err == nil {
			f.p.anchors[name] = v
		}
		return v, err
	case '*':
		f.i++
		start := f.i
		for f.i < len(f.s) && !strings.ContainsRune(" ,]}", rune(f.s[f.i])) {
			f.i++
		}
		v, ok := f.p.anchors[f.s[start:f.i]]
		if !ok {
			return nil, fmt.Errorf("别名 *%s 找不到定义", f.s[start:f.i])
		}
		return v, nil
	}
	return yamlPlain(f.plain(false)), nil
}

// plain 读一个不带引号的标量,到 , ] } 为止;asKey 时到「冒号+空格」为止。
func (f *yamlFlow) plain(asKey bool) string {
	start := f.i
	for f.i < len(f.s) {
		c := f.s[f.i]
		if c == ',' || c == ']' || c == '}' {
			break
		}
		if asKey && c == ':' && (f.i+1 == len(f.s) || strings.IndexByte(" ,}", f.s[f.i+1]) >= 0) {
			break
		}
		f.i++
	}
	return strings.TrimSpace(f.s[start:f.i])
}

func (f *yamlFlow) seq() (any, error) {
	f.i++ // [
	out := []any{}
	for {
		f.skip()
		if f.i >= len(f.s) {
			return nil, fmt.Errorf("方括号没有闭合")
		}
		if f.s[f.i] == ']' {
			f.i++
			return out, nil
		}
		before := f.i
		v, err := f.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		f.skip()
		if f.i < len(f.s) && f.s[f.i] == ',' {
			f.i++
		}
		if f.i == before {
			return nil, fmt.Errorf("无法识别的内容")
		}
	}
}

func (f *yamlFlow) mapping() (any, error) {
	f.i++ // {
	m := map[string]any{}
	var merges []any
	for {
		f.skip()
		if f.i >= len(f.s) {
			return nil, fmt.Errorf("花括号没有闭合")
		}
		if f.s[f.i] == '}' {
			f.i++
			applyMerges(m, merges)
			return m, nil
		}
		before := f.i
		var key string
		if c := f.s[f.i]; c == '"' || c == '\'' {
			k, n, err := yamlQuoted(f.s, f.i)
			if err != nil {
				return nil, err
			}
			key, f.i = k, n
		} else {
			key = f.plain(true)
		}
		f.skip()
		var v any
		if f.i < len(f.s) && f.s[f.i] == ':' {
			f.i++
			var err error
			if v, err = f.value(); err != nil {
				return nil, err
			}
		}
		if key == "<<" {
			merges = append(merges, v)
		} else {
			m[key] = v
		}
		f.skip()
		if f.i < len(f.s) && f.s[f.i] == ',' {
			f.i++
		}
		if f.i == before {
			return nil, fmt.Errorf("无法识别的内容")
		}
	}
}
