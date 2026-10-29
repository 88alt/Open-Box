// Package dnsfilter 是 DNS 域名过滤的设置与状态(对应前端 DnsFilterSettings / DnsFilterStatus)。
//
// 自己的设计,依据前端 api/openbox.ts 的类型和 DnsFilterCard.vue 的用法,没有参照上游。
// 本包目前只管「设置」:校验、规整、从档案里读出来、拼出状态。名单的下载 / 解析 / 应用、
// 查询记录和统计是 STATUS.md 里 dns-filter* 的后续几条,做的时候再往这里加。
package dnsfilter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// 上限:设置存在档案里,整份档案的请求体上限是 1 MiB,这里把各项压在远低于它的范围内。
const (
	MaxLists        = 20
	MaxAllowDomains = 2000
	maxIDLen        = 64
	maxNameLen      = 80 // 前端输入框 maxlength=80(按字符数)
	maxURLLen       = 2048
	maxDomainLen    = 253
	maxLabelLen     = 63
	MaxAutoDays     = 30
)

// List 是一条名单(订阅链接)。
type List struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// AutoUpdate 是名单自动更新计划:每 Days 天、在本地时间 Hour 点更新一次。
type AutoUpdate struct {
	Enabled bool `json:"enabled"`
	Days    int  `json:"days"`
	Hour    int  `json:"hour"`
}

// Settings 是用户保存的过滤设置。
type Settings struct {
	Enabled      bool       `json:"enabled"`
	Lists        []List     `json:"lists"`
	AllowDomains []string   `json:"allowDomains"`
	AutoUpdate   AutoUpdate `json:"autoUpdate"`
}

// Default 返回关闭状态的默认设置(与 profile.DefaultDNS 里的 filter 一致)。
func Default() Settings {
	return Settings{
		Lists:        []List{},
		AllowDomains: []string{},
		AutoUpdate:   AutoUpdate{Days: 1, Hour: 4},
	}
}

// ParseSettings 解析并校验客户端发来的设置,返回规整后的副本:
// 名称和链接去首尾空白,白名单域名小写、去末尾的点并去重(保序)。
// 缺失的 autoUpdate 取默认值;其余字段缺失按零值处理(enabled 为假、列表为空)。
func ParseSettings(raw []byte) (Settings, error) {
	var in struct {
		Enabled      bool        `json:"enabled"`
		Lists        []List      `json:"lists"`
		AllowDomains []string    `json:"allowDomains"`
		AutoUpdate   *AutoUpdate `json:"autoUpdate"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Settings{}, errors.New("设置格式不对:" + shortJSONError(err))
	}
	out := Default()
	out.Enabled = in.Enabled

	if len(in.Lists) > MaxLists {
		return Settings{}, fmt.Errorf("名单最多 %d 条", MaxLists)
	}
	seenID := map[string]bool{}
	seenURL := map[string]bool{}
	for i, l := range in.Lists {
		n := i + 1
		l.ID = strings.TrimSpace(l.ID)
		l.Name = strings.TrimSpace(l.Name)
		l.URL = strings.TrimSpace(l.URL)
		if l.ID == "" || len(l.ID) > maxIDLen || !validID(l.ID) {
			return Settings{}, fmt.Errorf("第 %d 条名单的 id 不合法(只能是字母、数字、_ . -,最长 %d 个字符)", n, maxIDLen)
		}
		if seenID[l.ID] {
			return Settings{}, fmt.Errorf("名单 id 重复:%s", l.ID)
		}
		seenID[l.ID] = true
		if l.Name == "" {
			return Settings{}, fmt.Errorf("第 %d 条名单没有名称", n)
		}
		if len([]rune(l.Name)) > maxNameLen {
			return Settings{}, fmt.Errorf("第 %d 条名单的名称太长(最多 %d 个字符)", n, maxNameLen)
		}
		if err := checkListURL(l.URL); err != nil {
			return Settings{}, fmt.Errorf("第 %d 条名单的链接不合法:%w", n, err)
		}
		if seenURL[l.URL] {
			return Settings{}, fmt.Errorf("名单链接重复:%s", l.URL)
		}
		seenURL[l.URL] = true
		out.Lists = append(out.Lists, l)
	}

	if len(in.AllowDomains) > MaxAllowDomains {
		return Settings{}, fmt.Errorf("白名单域名最多 %d 条", MaxAllowDomains)
	}
	seenDom := map[string]bool{}
	for _, d := range in.AllowDomains {
		dom, err := NormalizeDomain(d)
		if err != nil {
			return Settings{}, fmt.Errorf("白名单里的 %q 不合法:%w", clip(d, 60), err)
		}
		if seenDom[dom] {
			continue
		}
		seenDom[dom] = true
		out.AllowDomains = append(out.AllowDomains, dom)
	}

	if a := in.AutoUpdate; a != nil {
		if a.Days < 1 || a.Days > MaxAutoDays {
			return Settings{}, fmt.Errorf("自动更新的间隔要在 1 到 %d 天之间", MaxAutoDays)
		}
		if a.Hour < 0 || a.Hour > 23 {
			return Settings{}, errors.New("自动更新的时间要在 0 到 23 点之间")
		}
		out.AutoUpdate = *a
	}
	return out, nil
}

// FromProfile 从档案的 dns.filter 取出设置。读不出来(缺失、类型不对)的部分取默认值,
// 不报错:这是读路径,档案里的内容在写入时已经校验过。
func FromProfile(p map[string]any) Settings {
	dns, _ := p["dns"].(map[string]any)
	f, _ := dns["filter"].(map[string]any)
	out := Default()
	if f == nil {
		return out
	}
	b, err := json.Marshal(f)
	if err != nil {
		return out
	}
	var s Settings
	if json.Unmarshal(b, &s) != nil {
		return out
	}
	if s.Lists == nil {
		s.Lists = []List{}
	}
	if s.AllowDomains == nil {
		s.AllowDomains = []string{}
	}
	if s.AutoUpdate.Days < 1 {
		s.AutoUpdate.Days = 1
	}
	return s
}

// ToMap 转成能直接放进档案补丁的形状(数字用 float64,与 JSON 解码一致)。
func (s Settings) ToMap() map[string]any {
	lists := make([]any, 0, len(s.Lists))
	for _, l := range s.Lists {
		lists = append(lists, map[string]any{"id": l.ID, "name": l.Name, "url": l.URL, "enabled": l.Enabled})
	}
	allow := make([]any, 0, len(s.AllowDomains))
	for _, d := range s.AllowDomains {
		allow = append(allow, d)
	}
	return map[string]any{
		"enabled":      s.Enabled,
		"lists":        lists,
		"allowDomains": allow,
		"autoUpdate": map[string]any{
			"enabled": s.AutoUpdate.Enabled,
			"days":    float64(s.AutoUpdate.Days),
			"hour":    float64(s.AutoUpdate.Hour),
		},
	}
}

// ListStat 对应前端 DnsFilterStatus.lists 里的一项(名单下载解析后的统计)。
type ListStat struct {
	Count               int      `json:"count"`
	Unsupported         int      `json:"unsupported"`
	UnsupportedExamples []string `json:"unsupportedExamples,omitempty"`
	UpdatedAt           int64    `json:"updatedAt"`
	Error               string   `json:"error,omitempty"`
}

// Applied 对应前端 DnsFilterStatus.applied:上次应用时的开关和设置指纹。
type Applied struct {
	Enabled bool   `json:"enabled"`
	Key     string `json:"key"`
}

// Status 对应前端 DnsFilterStatus。
type Status struct {
	Settings  Settings            `json:"settings"`
	Lists     map[string]ListStat `json:"lists"`
	Pending   bool                `json:"pending"`
	Applied   *Applied            `json:"applied"`
	Busy      bool                `json:"busy"`
	Connected bool                `json:"connected"`
	// QueryLog 说明查询日志为什么没按预期接上:没有原因(正常,或根本没接管)就不带这个字段。
	QueryLog *QueryLog `json:"queryLog,omitempty"`
}

// QueryLog 对应前端 DnsFilterQueryLog:Reason 为 user 表示日志去向被用户另行设置(后端不覆盖),
// failed 表示加日志后 dnsmasq 起不来、已自动去掉。Syslog 前端用来表示「用户的日志去了系统日志、后端读得到」,
// 后端目前不读系统日志,所以恒不带。
type QueryLog struct {
	Reason string `json:"reason,omitempty"`
	Syslog bool   `json:"syslog,omitempty"`
}

// BuildStatus 拼出状态。名单还不会下载(lists 恒为空)、还没有应用过(applied 为 null)、
// 查询日志要由 Service 接上才有 connected(这里恒为假);所以「待应用」只看开关:开着就是还没生效。
func BuildStatus(s Settings) Status {
	return Status{
		Settings: s,
		Lists:    map[string]ListStat{},
		Pending:  s.Enabled,
	}
}

func validID(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	return true
}

func checkListURL(s string) error {
	if s == "" {
		return errors.New("不能为空")
	}
	if len(s) > maxURLLen {
		return fmt.Errorf("太长(最多 %d 个字符)", maxURLLen)
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("不是有效的网址")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("只支持 http / https")
	}
	if u.User != nil {
		return errors.New("不要在网址里带账号密码")
	}
	return nil
}

// NormalizeDomain 规整一条白名单域名:去首尾空白、转小写、去末尾的点;
// 允许开头的 "*."(表示它和所有子域名),其余每一段只能是字母、数字、连字符和下划线。
// 纯 IP、带端口、带路径的都不接受。
func NormalizeDomain(s string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(s))
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", errors.New("不能为空")
	}
	if len(d) > maxDomainLen {
		return "", fmt.Errorf("太长(最多 %d 个字符)", maxDomainLen)
	}
	host := strings.TrimPrefix(d, "*.")
	if host == "" {
		return "", errors.New("通配符后面要有域名")
	}
	labels := strings.Split(host, ".")
	allNum := true
	for _, l := range labels {
		if l == "" || len(l) > maxLabelLen {
			return "", errors.New("域名格式不对")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return "", errors.New("域名每一段不能以连字符开头或结尾")
		}
		for _, c := range l {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return "", errors.New("域名里有不能用的字符")
			}
			if c < '0' || c > '9' {
				allNum = false
			}
		}
	}
	if allNum && len(labels) > 1 {
		return "", errors.New("这看起来是 IP 地址,白名单只收域名")
	}
	return d, nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func shortJSONError(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return "字段 " + te.Field + " 的类型不对"
	}
	return "不是合法的 JSON"
}
