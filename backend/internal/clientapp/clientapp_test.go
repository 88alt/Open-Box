package clientapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func newSvc(t *testing.T, prof map[string]any) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		Store:     st,
		Profile:   func(context.Context) (map[string]any, error) { return prof, nil },
		LAN:       func() ([]string, []string) { return []string{"192.168.5.1"}, []string{"192.168.5.0/24"} },
		PanelPort: 3036,
	}
}

func TestPortOf(t *testing.T) {
	for in, want := range map[string]int{
		"0.0.0.0:3036": 3036, " 127.0.0.1:80 ": 80, "[::]:8080": 8080,
		"": 0, "3036": 0, "host:abc": 0, "host:0": 0, "host:70000": 0,
	} {
		if got := PortOf(in); got != want {
			t.Errorf("PortOf(%q) = %d,想要 %d", in, got, want)
		}
	}
}

func TestRouterIDStable(t *testing.T) {
	s := newSvc(t, nil)
	ctx := context.Background()
	a, err := s.RouterID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("格式不对: %q", a)
	}
	b, _ := s.RouterID(ctx)
	if a != b {
		t.Errorf("同一个服务两次不一样: %s %s", a, b)
	}
	// 换一个服务实例(模拟重启),同一个数据目录仍是同一个 ID
	s2 := &Service{Store: s.Store}
	if c, _ := s2.RouterID(ctx); c != a {
		t.Errorf("重启后变了: %s %s", a, c)
	}
}

func TestRouterIDConcurrent(t *testing.T) {
	s := newSvc(t, nil)
	var wg sync.WaitGroup
	ids := make([]string, 20)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _ = s.RouterID(context.Background())
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("并发读到不同的 ID: %v", ids)
		}
	}
}

func TestRouterIDRepairsBadStoredValue(t *testing.T) {
	s := newSvc(t, nil)
	if err := s.Store.Save(stateFile, map[string]any{"routerId": "not-hex"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.RouterID(context.Background())
	if err != nil || !validRouterID(id) {
		t.Fatalf("%q %v", id, err)
	}
}

func TestRouterIDRandFailure(t *testing.T) {
	s := newSvc(t, nil)
	s.Rand = bytes.NewReader([]byte("short"))
	if _, err := s.RouterID(context.Background()); err == nil {
		t.Fatal("随机源不够应报错")
	}
	// 失败后不能留下半个 ID
	s.Rand = nil
	if id, err := s.RouterID(context.Background()); err != nil || !validRouterID(id) {
		t.Fatalf("%q %v", id, err)
	}
}

func TestRouterIDCorruptStore(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	if err := store.WriteFileAtomic(dir+"/client-app.json", []byte("{坏"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: st}
	if _, err := s.RouterID(context.Background()); err == nil {
		t.Fatal("数据文件损坏应报错,不能覆盖")
	}
}

func TestInfoDefaults(t *testing.T) {
	s := newSvc(t, map[string]any{"region": "cn"}) // 档案里没有 shareRegions / serverInfo
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !validRouterID(info.RouterID) || info.RouterName != "Open-Box" || info.ServerInfoDefaults.Name != "Open-Box" {
		t.Errorf("%+v", info)
	}
	if info.PanelPort != 3036 || len(info.LanAddresses) != 1 || info.LanSubnets[0] != "192.168.5.0/24" {
		t.Errorf("局域网信息: %+v", info)
	}
	if info.ShareRegionsCustomized || len(info.ShareRegions) != 3 || len(info.DefaultShareRegions) != 3 {
		t.Errorf("地区分流: customized=%v n=%d", info.ShareRegionsCustomized, len(info.ShareRegions))
	}
	if info.EgressCountry != nil {
		t.Errorf("还没判过出口国家应为 nil: %+v", info.EgressCountry)
	}
}

// 编成 JSON 后字段名和 null / [] 要和前端类型对得上。
func TestInfoJSONShape(t *testing.T) {
	s := newSvc(t, map[string]any{})
	s.LAN = nil // 不接网卡信息:数组字段仍是 [],不是 null
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(info)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"routerId", "routerName", "lanAddresses", "lanSubnets", "panelPort", "shareRegions",
		"shareRegionsCustomized", "defaultShareRegions", "serverInfo", "serverInfoDefaults", "egressCountry"} {
		if _, ok := m[k]; !ok {
			t.Errorf("缺字段 %s", k)
		}
	}
	if m["egressCountry"] != nil {
		t.Errorf("egressCountry 应为 null: %v", m["egressCountry"])
	}
	if a, ok := m["lanAddresses"].([]any); !ok || len(a) != 0 {
		t.Errorf("lanAddresses = %v", m["lanAddresses"])
	}
	if si, ok := m["serverInfo"].(map[string]any); !ok || len(si) != 0 {
		t.Errorf("没设置时 serverInfo 应是 {}: %v", m["serverInfo"])
	}
	groups := m["shareRegions"].([]any)
	g := groups[0].(map[string]any)
	for _, k := range []string{"id", "name", "regions", "rules", "catchAll", "dns"} {
		if _, ok := g[k]; !ok {
			t.Errorf("地区组缺字段 %s", k)
		}
	}
	dns := g["dns"].(map[string]any)
	for _, k := range []string{"direct", "directProtocol", "directPort", "directExtras", "proxy", "proxyProtocol", "proxyPort", "proxyExtras"} {
		if _, ok := dns[k]; !ok {
			t.Errorf("dns 缺字段 %s", k)
		}
	}
	if _, ok := dns["directExtras"].([]any); !ok {
		t.Errorf("directExtras 应是数组: %v", dns["directExtras"])
	}
}

func TestInfoServerInfoAndName(t *testing.T) {
	tests := []struct {
		name     string
		si       any
		wantName string
	}{
		{"没设置", nil, "Open-Box"},
		{"空对象", map[string]any{}, "Open-Box"},
		{"只有空白", map[string]any{"name": "   "}, "Open-Box"},
		{"设了名字", map[string]any{"name": " 家里 ", "icon": "US", "region": "CN"}, "家里"},
		{"类型不对当作没设", "oops", "Open-Box"},
		{"字段类型不对忽略", map[string]any{"name": 5, "icon": []any{}}, "Open-Box"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSvc(t, map[string]any{"serverInfo": tt.si})
			info, err := s.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info.RouterName != tt.wantName {
				t.Errorf("routerName = %q,想要 %q", info.RouterName, tt.wantName)
			}
		})
	}
	// serverInfo 原样回给前端(名字保留用户写的空格,由前端决定怎么显示)
	s := newSvc(t, map[string]any{"serverInfo": map[string]any{"name": " 家里 ", "icon": "US", "iconSvg": "<svg/>", "region": "CN", "x": 1}})
	info, _ := s.Info(context.Background())
	if info.ServerInfo != (ServerInfo{Name: " 家里 ", Icon: "US", IconSVG: "<svg/>", Region: "CN"}) {
		t.Errorf("%+v", info.ServerInfo)
	}
}

func TestInfoCustomRegions(t *testing.T) {
	stored := []any{
		map[string]any{
			"id": "jp", "name": "日本", "regions": []any{"JP"}, "catchAll": "direct",
			"rules": []any{map[string]any{"type": "domainSuffix", "value": "example.jp", "action": "proxy"}},
			"dns": map[string]any{"direct": "wan", "directProtocol": "udp", "directPort": float64(53),
				"proxy": "1.1.1.1", "proxyProtocol": "tcp", "proxyPort": float64(53),
				"proxyExtras": []any{map[string]any{"server": "8.8.8.8", "protocol": "tcp", "port": float64(53)}}},
		},
		map[string]any{"id": "x", "name": "其他", "regions": []any{"CN"}, "regionMatch": "outside", "default": true, "catchAll": "proxy"},
	}
	s := newSvc(t, map[string]any{"shareRegions": stored})
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.ShareRegionsCustomized || len(info.ShareRegions) != 2 {
		t.Fatalf("%+v", info.ShareRegions)
	}
	jp := info.ShareRegions[0]
	if jp.ID != "jp" || jp.Rules[0].Value != "example.jp" || jp.DNS.ProxyExtras[0].Server != "8.8.8.8" {
		t.Errorf("%+v", jp)
	}
	x := info.ShareRegions[1]
	if !x.Default || x.RegionMatch != "outside" {
		t.Errorf("%+v", x)
	}
	// 缺省的数组补成空数组
	if jp.DNS.DirectExtras == nil || x.Rules == nil || x.DNS.DirectExtras == nil {
		t.Errorf("缺省数组应补成 []: %+v %+v", jp, x)
	}
	// 默认值不受影响
	if len(info.DefaultShareRegions) != 3 {
		t.Errorf("默认值: %d", len(info.DefaultShareRegions))
	}
}

func TestInfoRegionsEmptyOrBroken(t *testing.T) {
	tests := []struct {
		name       string
		v          any
		customized bool
	}{
		{"空数组 = 默认", []any{}, false},
		{"不是数组", "x", true},
		{"数组里不是对象", []any{1, 2}, true},
		{"字段类型不对", []any{map[string]any{"id": 5}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSvc(t, map[string]any{"shareRegions": tt.v})
			info, err := s.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info.ShareRegionsCustomized != tt.customized {
				t.Errorf("customized = %v", info.ShareRegionsCustomized)
			}
			// 坏数据也给出默认三组,前端「恢复默认」才点得动
			if len(info.ShareRegions) != 3 {
				t.Errorf("应回默认三组: %d", len(info.ShareRegions))
			}
		})
	}
}

func TestInfoProfileError(t *testing.T) {
	s := newSvc(t, nil)
	s.Profile = func(context.Context) (map[string]any, error) { return nil, errors.New("档案坏了") }
	if _, err := s.Info(context.Background()); err == nil || !strings.Contains(err.Error(), "档案坏了") {
		t.Fatalf("读不到档案应报错: %v", err)
	}
}

// 默认三组要满足前端 ShareRegionsList.validate 的规则,并和前端 regionDnsDefaults 一致。
func TestDefaultShareRegions(t *testing.T) {
	gs := DefaultShareRegions()
	if len(gs) != 3 {
		t.Fatalf("应有三组: %d", len(gs))
	}
	idRe := regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	ids := map[string]bool{}
	claimed := map[string]string{}
	defaults := 0
	for _, g := range gs {
		if !idRe.MatchString(g.ID) || ids[g.ID] || g.Name == "" || len([]rune(g.Name)) > 30 || len([]rune(g.Description)) > 120 {
			t.Errorf("组不合格: %+v", g)
		}
		ids[g.ID] = true
		if g.CatchAll != "direct" && g.CatchAll != "proxy" {
			t.Errorf("%s catchAll = %q", g.ID, g.CatchAll)
		}
		for _, r := range g.Rules {
			if r.Type == "" || r.Value == "" || (r.Action != "direct" && r.Action != "proxy") {
				t.Errorf("%s 规则不合格: %+v", g.ID, r)
			}
		}
		if g.Default {
			defaults++
		}
		if g.RegionMatch == "outside" {
			if len(g.Regions) == 0 {
				t.Errorf("%s 选了「之外」就至少要有一个地区", g.ID)
			}
			continue
		}
		for _, r := range g.Regions {
			if other, ok := claimed[r]; ok {
				t.Errorf("地区 %s 同时在 %s 和 %s", r, other, g.ID)
			}
			claimed[r] = g.ID
		}
	}
	if defaults != 1 {
		t.Errorf("默认组应有且只有一个: %d", defaults)
	}
	// 含大陆的组:直连系统 DNS、代理 TCP 1.1.1.1;其余两侧都是系统 DNS
	cn := gs[0].DNS
	if cn.Direct != "wan" || cn.DirectProtocol != "udp" || cn.DirectPort != 53 ||
		cn.Proxy != "1.1.1.1" || cn.ProxyProtocol != "tcp" || cn.ProxyPort != 53 {
		t.Errorf("大陆组 DNS: %+v", cn)
	}
	for _, g := range gs[1:] {
		if g.DNS.Direct != "wan" || g.DNS.Proxy != "wan" || g.DNS.ProxyProtocol != "udp" {
			t.Errorf("%s DNS: %+v", g.ID, g.DNS)
		}
	}
	// 每次返回新的切片,改了不影响下次
	gs[0].Rules[0].Value = "changed"
	if DefaultShareRegions()[0].Rules[0].Value != "cn" {
		t.Error("默认值被共享了")
	}
}
