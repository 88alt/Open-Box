package sub

import (
	"errors"
	"regexp"
)

// ErrClashUnsupported:Clash / Mihomo 的 YAML 解析还没做(STATUS.md 里单列了条目)。
var ErrClashUnsupported = errors.New("暂不支持 Clash 格式的订阅:请把订阅的 User-Agent 改成 v2rayN(多数机场会改返回分享链接 / base64),或改用 sing-box 格式")

var clashMarker = regexp.MustCompile(`(?m)^proxies\s*:`)

func looksLikeClash(text string) bool { return clashMarker.MatchString(text) }

func parseClash(string) (*Result, error) { return nil, ErrClashUnsupported }
