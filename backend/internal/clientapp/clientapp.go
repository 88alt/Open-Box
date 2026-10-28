// Package clientapp 是「客户端 App」页面(手机 / 电脑 App 与路由器配合)在路由器这一侧的服务层。
//
// 本包只做前端类型(panel/src/api/clientApp.ts)能推出来的部分:路由器标识、局域网信息、地区分流、路由器名称。
// App 自己的私有约定(配对码封包、App 用的公开同步接口)前端没有规定,这里不涉及,见 STATUS.md 对应条目。
// 自己的设计,没有参照上游。
package clientapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// DefaultRouterName 是路由器没起名字时用的名字(前端 serverInfoDefaults.name 注释写的是「Open-Box」)。
const DefaultRouterName = "Open-Box"

// stateFile 是本包的数据文件(<状态目录>/client-app.json)。后面的条目(配对码、同步开关等)也存在这里,
// 所以读改写一律走 store.Update,不覆盖别的字段。
const stateFile = "client-app"

type state struct {
	RouterID string `json:"routerId,omitempty"`
	// Config 是本地分流(客户端配置)的同步状态,见 clientconfig.go。
	Config *configState `json:"clientConfig,omitempty"`
	// Egress 是上一次按出口 IP 判出的国家,见 egress.go。
	Egress *EgressCountry `json:"egressCountry,omitempty"`
}

// ServerInfo 是档案里的路由器标识(serverInfo),四个字段都可以空。原样回给前端。
type ServerInfo struct {
	Name    string `json:"name,omitempty"`
	Icon    string `json:"icon,omitempty"`
	IconSVG string `json:"iconSvg,omitempty"`
	Region  string `json:"region,omitempty"`
}

// EgressCountry 是按出口 IP 判出的国家;没判过时 Info 里是 null。
type EgressCountry struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
	At      int64  `json:"at"`
}

// Info 是 GET /api/openbox/client-app/info 的响应,字段与前端 OpenboxClientAppInfo 一一对应。
type Info struct {
	RouterID               string         `json:"routerId"`
	RouterName             string         `json:"routerName"`
	LanAddresses           []string       `json:"lanAddresses"`
	LanSubnets             []string       `json:"lanSubnets"`
	PanelPort              int            `json:"panelPort"`
	ShareRegions           []Group        `json:"shareRegions"`
	ShareRegionsCustomized bool           `json:"shareRegionsCustomized"`
	DefaultShareRegions    []Group        `json:"defaultShareRegions"`
	ServerInfo             ServerInfo     `json:"serverInfo"`
	ServerInfoDefaults     serverDefault  `json:"serverInfoDefaults"`
	EgressCountry          *EgressCountry `json:"egressCountry"`
}

type serverDefault struct {
	Name string `json:"name"`
}

// Service 组装 Info。字段都是依赖,测试里换假的。
type Service struct {
	Store *store.Store
	// Profile 读完整档案(*profile.Service.Get 的包装)。
	Profile func(ctx context.Context) (map[string]any, error)
	// LAN 返回局域网地址和 IPv4 网段(system.LAN)。为空时两项都回空数组。
	LAN func() (addrs, subnets []string)
	// PanelPort 是面板(本后端)监听的端口,拼进 App 码;0 表示不知道。
	PanelPort int
	// Rand 生成路由器 ID 的随机源,默认 crypto/rand。
	Rand io.Reader
	// DetectEgress 查一次出口公网 IP 和它所在国家(两位大写代码);nil = 不支持,POST egress-country 回 503。
	DetectEgress func(ctx context.Context) (ip, country string, err error)
	// Running 报告内核服务在不在运行;nil = 不知道,POST egress-country 回 503。
	Running func(ctx context.Context) (bool, error)
	// Now 取当前时间,默认 time.Now(测试里换假的)。
	Now func() time.Time
}

// PortOf 取 "host:port" 里的端口;格式不对或不在 1~65535 返回 0。
func PortOf(listen string) int {
	_, p, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// RouterID 返回这台路由器的 ID:32 位小写十六进制随机数,第一次用到时生成并保存,之后不变。
// 它会出现在 App 码里,换了 ID 就等于换了一台路由器,所以不提供更换。
func (s *Service) RouterID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var st state
	err := s.Store.Update(stateFile, &st, func() error {
		if validRouterID(st.RouterID) {
			return errNoChange
		}
		id, err := newRouterID(s.Rand)
		if err != nil {
			return err
		}
		st.RouterID = id
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		return "", fmt.Errorf("读取路由器 ID 失败: %w", err)
	}
	return st.RouterID, nil
}

// errNoChange 让 store.Update 在不需要改动时跳过写盘。
var errNoChange = errors.New("clientapp: no change")

func validRouterID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func newRouterID(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Info 汇总路由器标识、局域网、地区分流和路由器名称。读不到档案时报错,不猜默认值。
func (s *Service) Info(ctx context.Context) (Info, error) {
	id, err := s.RouterID(ctx)
	if err != nil {
		return Info{}, err
	}
	prof, err := s.Profile(ctx)
	if err != nil {
		return Info{}, fmt.Errorf("读取档案失败: %w", err)
	}
	si := serverInfoOf(prof["serverInfo"])
	regions, customized := regionsOf(prof["shareRegions"])
	addrs, subnets := []string{}, []string{}
	if s.LAN != nil {
		a, n := s.LAN()
		if a != nil {
			addrs = a
		}
		if n != nil {
			subnets = n
		}
	}
	name := strings.TrimSpace(si.Name)
	if name == "" {
		name = DefaultRouterName
	}
	return Info{
		RouterID: id, RouterName: name,
		LanAddresses: addrs, LanSubnets: subnets, PanelPort: s.PanelPort,
		ShareRegions: regions, ShareRegionsCustomized: customized, DefaultShareRegions: DefaultShareRegions(),
		ServerInfo: si, ServerInfoDefaults: serverDefault{Name: DefaultRouterName},
		EgressCountry: s.cachedEgress(),
	}, nil
}

// serverInfoOf 取档案里的 serverInfo;不是对象时当作没设置。只认四个字符串字段,别的忽略。
func serverInfoOf(v any) ServerInfo {
	m, ok := v.(map[string]any)
	if !ok {
		return ServerInfo{}
	}
	str := func(k string) string { s, _ := m[k].(string); return s }
	return ServerInfo{Name: str("name"), Icon: str("icon"), IconSVG: str("iconSvg"), Region: str("region")}
}

// regionsOf 取档案里的地区分流。没存、存了空数组 = 默认三组(customized=false)。
// 存了东西但读不出来(保存时会校验,但手工改坏的文件仍可能出现)= 给默认三组并标成「改过」,
// 这样前端的「恢复默认」仍然能点,不会因为一份坏数据让整页打不开。
func regionsOf(v any) (groups []Group, customized bool) {
	arr, ok := v.([]any)
	if v == nil || (ok && len(arr) == 0) {
		return DefaultShareRegions(), false
	}
	if !ok {
		return DefaultShareRegions(), true
	}
	raw, err := json.Marshal(arr)
	if err != nil {
		return DefaultShareRegions(), true
	}
	if err := json.Unmarshal(raw, &groups); err != nil {
		return DefaultShareRegions(), true
	}
	for i := range groups {
		groups[i].fill()
	}
	return groups, true
}

// fill 把 nil 切片换成空切片,编成 JSON 时是 [] 而不是 null(前端直接对它们取长度和遍历)。
func (g *Group) fill() {
	if g.Regions == nil {
		g.Regions = []string{}
	}
	if g.Rules == nil {
		g.Rules = []Rule{}
	}
	if g.DNS.DirectExtras == nil {
		g.DNS.DirectExtras = []DNSUpstream{}
	}
	if g.DNS.ProxyExtras == nil {
		g.DNS.ProxyExtras = []DNSUpstream{}
	}
}
