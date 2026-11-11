package terminaltest

import (
	"context"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsforward"
)

// FirstLayer 对应前端 OpenboxTerminalTest.firstLayer:终端的流量到了本机,「进内核之前」那一层是怎么处理的。
// 自己的设计,依据前端的字段类型 { bypassEnabled, bypassSets, dnsMode, dnsForward },没有参照上游。
//   - bypassEnabled / bypassSets:入口原生旁路(目的地址落在这些 IP 集合里的流量不进内核)。取自**已部署的** config.json
//     里 tun 入站的 route_exclude_address_set:写的是集合原名就原样给,写的是 core 为热切换裁剪出来的 obflip-byp-<集合名>
//     就还原成 <集合名>。这样给的就是正在跑的那份配置的事实,不是「现在重新生成会怎样」;
//   - dnsMode:档案的 dns.mode;
//   - dnsForward:dns.mode 不是 dnsmasq 时 dnsmasq 不参与转发,给 "none";是 dnsmasq 且接管生效时给转发计划(本后端的接管
//     把全部查询交给内核,即 "all");是 dnsmasq 但接管没生效时 dnsmasq 按它自己的配置转,我们不知道,这个字段不带。
//
// 读不到已部署配置或档案模式时整个 firstLayer 不带,不猜。
type FirstLayer struct {
	BypassEnabled bool     `json:"bypassEnabled"`
	BypassSets    []string `json:"bypassSets"`
	DNSMode       string   `json:"dnsMode"`
	DNSForward    string   `json:"dnsForward,omitempty"`
}

// flipBypassPrefix 是 core 给热切换裁剪过的旁路集合起的名字前缀(core 的 routing.FlipBypassTag)。
const flipBypassPrefix = "obflip-byp-"

// NativeBypassFromConfig 从已部署配置的第一个 tun 入站读入口原生旁路:route_exclude_address_set 里的字符串,
// obflip-byp- 前缀还原成集合原名;非字符串项跳过;没有 tun 入站或没有这个键就是未开、名单为空数组。
func NativeBypassFromConfig(cfg map[string]any) (bool, []string) {
	sets := []string{}
	ins, _ := cfg["inbounds"].([]any)
	for _, in := range ins {
		m, _ := in.(map[string]any)
		if m == nil || m["type"] != "tun" {
			continue
		}
		list, _ := m["route_exclude_address_set"].([]any)
		for _, v := range list {
			s, ok := v.(string)
			if !ok || s == "" {
				continue
			}
			if rest, cut := strings.CutPrefix(s, flipBypassPrefix); cut && rest != "" {
				s = rest
			}
			sets = append(sets, s)
		}
		break
	}
	return len(sets) > 0, sets
}

// NewFirstLayer 把三个来源接成 Deps.FirstLayer:config 读已部署配置,mode 读档案的 dns.mode,forward 是 dnsforward.Service.Describe。
func NewFirstLayer(config func() (map[string]any, error), mode func(context.Context) (string, error),
	forward func(context.Context) *dnsforward.Result) func(context.Context) *FirstLayer {
	return func(ctx context.Context) *FirstLayer {
		cfg, err := config()
		if err != nil || cfg == nil {
			return nil
		}
		m, err := mode(ctx)
		if err != nil || m == "" {
			return nil
		}
		on, sets := NativeBypassFromConfig(cfg)
		fl := &FirstLayer{BypassEnabled: on, BypassSets: sets, DNSMode: m}
		if m != "dnsmasq" {
			fl.DNSForward = "none"
		} else if forward != nil {
			if f := forward(ctx); f != nil {
				fl.DNSForward = f.Plan
			}
		}
		return fl
	}
}
