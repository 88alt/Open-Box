package sub

import (
	"bytes"
	"encoding/base64"
	"strings"
)

// MaxNodes 是一份订阅最多导入的节点数,防止异常内容撑爆内存。
const MaxNodes = 5000

// Parse 识别订阅内容的格式并解析。顺序:sing-box JSON → Clash YAML → 分享链接 → base64(解码后再按链接解析)。
func Parse(content []byte) (*Result, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	text := strings.TrimSpace(string(content))
	if text == "" {
		return nil, ErrEmpty
	}

	if text[0] == '{' || text[0] == '[' {
		res, err := parseSingBox([]byte(text))
		if err != nil {
			return nil, err
		}
		return finish(res)
	}
	if looksLikeClash(text) {
		res, err := parseClash(text)
		if err != nil {
			return nil, err
		}
		return finish(res)
	}
	if strings.Contains(text, "://") {
		return finish(parseLinks(text, FormatLinks))
	}
	if decoded, ok := decodeBase64(text); ok {
		dt := strings.TrimSpace(decoded)
		if strings.Contains(dt, "://") {
			return finish(parseLinks(dt, FormatBase64))
		}
		if looksLikeClash(dt) {
			res, err := parseClash(dt)
			if err != nil {
				return nil, err
			}
			return finish(res)
		}
	}
	return nil, ErrUnrecognized
}

func finish(res *Result) (*Result, error) {
	if res == nil || (len(res.Nodes) == 0 && len(res.Skipped) == 0) {
		return nil, ErrNoNodes
	}
	if len(res.Nodes) > MaxNodes {
		res.Nodes = res.Nodes[:MaxNodes]
	}
	return res, nil
}

// decodeBase64 依次尝试标准 / URL 编码、带或不带填充;内容里的空白(换行)先去掉。
func decodeBase64(s string) (string, bool) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
	if s == "" {
		return "", false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// parseLinks 按行解析分享链接;空行和以 # 开头的注释行忽略。
func parseLinks(text, format string) *Result {
	res := &Result{Format: format, Nodes: []Node{}, Skipped: []Skipped{}}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "://") {
			continue
		}
		node, skip := parseLink(line)
		if skip != nil {
			res.Skipped = append(res.Skipped, *skip)
			continue
		}
		res.Nodes = append(res.Nodes, *node)
		if len(res.Nodes) >= MaxNodes {
			break
		}
	}
	return res
}
