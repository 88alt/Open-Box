// Package sub 负责订阅的拉取与解析(M1)。
//
// 解析结果是 openbox-core 的 configDetailed 认识的节点形状:
//
//	{ "tag", "type", "server", "server_port", "fields": { ... } }
//
// fields 里的键沿用 sing-box 出站的写法(uuid / password / tls / transport ...),core 的 emit 包按这些键生成出站。
// 只依据公开的分享链接约定(SIP002、v2rayN vmess、v2rayN/Xray 的 VLESS 分享、hysteria2 / tuic / anytls 的 URI 规范)
// 和 sing-box 官方文档写成,没有参考上游的闭源实现。
package sub

import "errors"

// 订阅格式名,原样显示在前端预览里。
const (
	FormatLinks   = "links"    // 一行一个分享链接
	FormatBase64  = "base64"   // 整份内容是 base64 编码的分享链接
	FormatSingBox = "sing-box" // sing-box 配置的 outbounds
	FormatClash   = "clash"    // Clash / Mihomo 的 proxies
)

// 解析失败的原因,与前端 OpenboxSkippedNode.reason 一致。
const (
	ReasonUnsupportedType      = "unsupported-type"
	ReasonUnsupportedPlugin    = "unsupported-plugin"
	ReasonUnsupportedTransport = "unsupported-transport"
	ReasonInvalid              = "invalid"
)

var (
	// ErrEmpty 表示订阅内容是空的。
	ErrEmpty = errors.New("订阅内容为空")
	// ErrNoNodes 表示内容能读,但里面一个可用节点都没有。
	ErrNoNodes = errors.New("订阅里没有可用的节点")
	// ErrUnrecognized 表示内容既不是分享链接、base64、sing-box 配置,也不是 Clash 配置。
	ErrUnrecognized = errors.New("无法识别订阅格式:既不是分享链接 / base64,也不是 sing-box 或 Clash 配置")
)

// Node 是一个解析好的节点。
type Node struct {
	Tag         string `json:"tag"`
	OriginalTag string `json:"originalTag,omitempty"`
	// RegionCode 是改名时命中的地区所绑定的国家代码(ISO 3166-1 alpha-2),没命中为空;界面按它显示国旗。
	RegionCode string `json:"regionCode,omitempty"`
	// RegionName 是命中的地区条目的名字(如「香港」),节点组的动态关键词会同时匹配它;core 不读。
	RegionName string         `json:"regionName,omitempty"`
	Type       string         `json:"type"`
	Server     string         `json:"server"`
	ServerPort int            `json:"server_port"`
	Fields     map[string]any `json:"fields"`
}

// Skipped 是没能导入的条目。
type Skipped struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Result 是一份订阅解析后的结果。
type Result struct {
	Format  string    `json:"format"`
	Nodes   []Node    `json:"nodes"`
	Skipped []Skipped `json:"skipped"`
}
