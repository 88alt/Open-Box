package profile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// j 把 JSON 文本解成 map,便于写用例。
func j(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("用例 JSON 无效: %v\n%s", err, s)
	}
	return m
}

func TestMerge(t *testing.T) {
	base := j(t, `{"a":1,"o":{"x":1,"y":2,"deep":{"k":1}},"arr":[1,2,3],"gone":true}`)
	patch := j(t, `{"o":{"y":9,"deep":{"z":2}},"arr":[7],"gone":null,"new":"v"}`)
	baseCopy, patchCopy := Clone(base), Clone(patch)

	got := Merge(base, patch)
	want := j(t, `{"a":1,"o":{"x":1,"y":9,"deep":{"k":1,"z":2}},"arr":[7],"new":"v"}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("合并结果不对\n得到 %v\n期望 %v", got, want)
	}
	if !reflect.DeepEqual(base, baseCopy) || !reflect.DeepEqual(patch, patchCopy) {
		t.Fatal("Merge 不应改动入参")
	}
	// 结果与入参不共享底层数据
	got["o"].(map[string]any)["x"] = 100.0
	if base["o"].(map[string]any)["x"] != 1.0 {
		t.Fatal("结果和 base 共享了 map")
	}
}

func TestDefaultPassesCheck(t *testing.T) {
	for _, region := range []string{RegionCN, RegionIntl} {
		p := Default()
		p["dns"] = DefaultDNS(region)
		if err := Check(p); err != nil {
			t.Fatalf("默认档案(%s)没通过校验: %v", region, err)
		}
	}
}

func TestDefaultDNSByRegion(t *testing.T) {
	cn, intl := DefaultDNS(RegionCN), DefaultDNS(RegionIntl)
	if cn["proxy"] != "1.1.1.1" || cn["proxyProtocol"] != "tcp" {
		t.Errorf("cn 代理上游应是 TCP 1.1.1.1: %v %v", cn["proxy"], cn["proxyProtocol"])
	}
	if intl["proxy"] != WAN || intl["proxyProtocol"] != "udp" {
		t.Errorf("intl 代理上游应是 wan/udp: %v %v", intl["proxy"], intl["proxyProtocol"])
	}
	for _, d := range []map[string]any{cn, intl} {
		if d["direct"] != WAN || d["directProtocol"] != "udp" {
			t.Errorf("直连上游应是 wan/udp: %v", d)
		}
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name    string
		patch   string
		wantErr string // 空 = 应通过
	}{
		{"ipv6 类型错", `{"ipv6":"yes"}`, "ipv6"},
		{"ipv6Proxy 非法", `{"ipv6Proxy":"x"}`, "ipv6Proxy"},
		{"ipv6Proxy bypass 合法", `{"ipv6Proxy":"bypass"}`, ""},
		{"端口模式非法", `{"bypassPortsMode":"x"}`, "bypassPortsMode"},
		{"端口写法合法", `{"bypassPorts":"21114-21119, 2233"}`, ""},
		{"端口中文逗号合法", `{"bypassPorts":"80，443"}`, ""},
		{"端口越界", `{"bypassPorts":"70000"}`, "bypassPorts"},
		{"端口范围反了", `{"bypassPorts":"200-100"}`, "端口范围"},
		{"端口含字母", `{"bypassPortsWhitelist":"80,abc"}`, "bypassPortsWhitelist"},
		{"端口为 0", `{"bypassPorts":"0"}`, "bypassPorts"},
		{"端口过长", `{"bypassPorts":"` + strings.Repeat("1,", 300) + `"}`, "太长"},
		{"测速地址非 http", `{"testUrl":"ftp://a.b"}`, "testUrl"},
		{"测速地址无主机", `{"testUrl":"http://"}`, "testUrl"},
		{"测速地址为空合法", `{"directTestUrl":""}`, ""},
		{"状态码合法", `{"testExpectedStatus":"200-399/204"}`, ""},
		{"状态码星号合法", `{"testExpectedStatus":"*"}`, ""},
		{"状态码非法", `{"testExpectedStatus":"abc"}`, "testExpectedStatus"},
		{"状态码范围反", `{"testExpectedStatus":"399-200"}`, "起点大于终点"},
		{"tun.stack 非法", `{"tun":{"stack":"x"}}`, "tun.stack"},
		{"tun.mtu 过小", `{"tun":{"mtu":100}}`, "tun.mtu"},
		{"tun.mtu 为 0 合法", `{"tun":{"mtu":0}}`, ""},
		{"tun.mtu 小数", `{"tun":{"mtu":1500.5}}`, "整数"},
		{"tun.tcpMss 过小", `{"tun":{"tcpMss":100}}`, "tun.tcpMss"},
		{"tun 不是对象", `{"tun":5}`, "tun 必须是对象"},
		{"dns.mode 非法", `{"dns":{"mode":"x"}}`, "dns.mode"},
		{"dns.region 非法", `{"dns":{"region":"us"}}`, "dns.region"},
		{"region 非法", `{"region":"us"}`, "region"},
		{"上游填域名", `{"dns":{"direct":"dns.example.com"}}`, "IP"},
		{"上游填 IPv6 合法", `{"dns":{"proxy":"2606:4700:4700::1111"}}`, ""},
		{"上游协议非法", `{"dns":{"directProtocol":"tls"}}`, "directProtocol"},
		{"上游端口 0", `{"dns":{"proxyPort":0}}`, "proxyPort"},
		{"备用上游 4 个", `{"dns":{"directExtras":[{"server":"1.1.1.1"},{"server":"1.0.0.1"},{"server":"8.8.8.8"},{"server":"8.8.4.4"}]}}`, "最多"},
		{"备用上游缺 server", `{"dns":{"directExtras":[{"port":53}]}}`, "缺少 server"},
		{"备用上游合法", `{"dns":{"proxyExtras":[{"server":"8.8.8.8","protocol":"tcp","port":53}]}}`, ""},
		{"备用上游端口越界", `{"dns":{"proxyExtras":[{"server":"8.8.8.8","port":70000}]}}`, "port"},
		{"更新通道非法", `{"updates":{"openbox":{"channel":"x"}}}`, "channel"},
		{"更新时刻越界", `{"updates":{"openbox":{"hour":24}}}`, "hour"},
		{"更新天数为 0", `{"updates":{"openbox":{"days":0}}}`, "days"},
		{"未知字段放行", `{"shareRegions":[{"a":1}],"serverInfo":{"x":1}}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(Merge(Default(), j(t, c.patch)))
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("应通过,却报错: %v", err)
			case c.wantErr != "" && err == nil:
				t.Fatalf("应报错(含 %q),却通过了", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("错误信息 %q 不含 %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestCheckClampsKeepMonths(t *testing.T) {
	for in, want := range map[float64]float64{0: 1, -5: 1, 12: 12, 36: 36, 99: 36} {
		p := Default()
		p["traffic"] = map[string]any{"keepMonths": in}
		if err := Check(p); err != nil {
			t.Fatal(err)
		}
		if got := p["traffic"].(map[string]any)["keepMonths"]; got != want {
			t.Errorf("keepMonths %v → %v,期望 %v", in, got, want)
		}
	}
}

// fakeCore 记录调用次数,迁移时把 regions 翻成固定的站点集。
type fakeCore struct {
	migrateCalls atomic.Int32
	migrateErr   error
}

func (f *fakeCore) MigrateRouting(_ context.Context, r map[string]any) (map[string]any, bool, error) {
	f.migrateCalls.Add(1)
	if f.migrateErr != nil {
		return nil, false, f.migrateErr
	}
	return map[string]any{"policies": []any{map[string]any{"id": "legacy-1", "name": "迁移来的"}}, "fallbackName": "其他"}, true, nil
}

func (f *fakeCore) NormalizeGroups(_ context.Context, g []any) ([]any, error) {
	out := []any{map[string]any{"id": "builtin-direct"}}
	out = append(out, g...)
	return append(out, map[string]any{"id": "builtin-block"}), nil
}

func newSvc(t *testing.T, c Core) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, c, nil), st
}

func TestGetFreshIsDefault(t *testing.T) {
	svc, st := newSvc(t, nil)
	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("全新档案应等于默认值\n%v", got)
	}
	// 读取不落盘
	if ok, _ := st.Load(storeName, &map[string]any{}); ok {
		t.Fatal("GET 不应该写文件")
	}
}

func TestPatchPersistsFullProfileAndKeepsUnknownKeys(t *testing.T) {
	svc, st := newSvc(t, nil)
	ctx := context.Background()
	got, err := svc.Patch(ctx, j(t, `{"ipv6":true,"dns":{"mode":"hijack"},"shareRegions":[{"id":"a"}],"serverInfo":{"k":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got["ipv6"] != true || got["dns"].(map[string]any)["mode"] != "hijack" {
		t.Fatalf("补丁没生效: %v", got)
	}
	// 没动的 dns 字段仍在
	if got["dns"].(map[string]any)["direct"] != WAN {
		t.Fatalf("深度合并丢了 dns.direct: %v", got["dns"])
	}
	// 再来一个补丁,未知键保留
	got2, err := svc.Patch(ctx, j(t, `{"rejectQuic":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if got2["serverInfo"] == nil || got2["shareRegions"] == nil || got2["ipv6"] != true {
		t.Fatalf("后续补丁丢了旧内容: %v", got2)
	}
	// 落盘的是完整档案(默认值变化不会悄悄改变已保存的设置)
	disk := map[string]any{}
	if _, err := st.Load(storeName, &disk); err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["routing"]; !ok {
		t.Fatal("落盘的档案应包含完整的 routing")
	}
	if !reflect.DeepEqual(disk, got2) {
		t.Fatalf("落盘内容与返回值不一致")
	}
}

func TestPatchInvalidDoesNotSave(t *testing.T) {
	svc, st := newSvc(t, nil)
	_, err := svc.Patch(context.Background(), j(t, `{"tun":{"stack":"bogus"}}`))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("应返回 *ValidationError,得到 %v", err)
	}
	if ok, _ := st.Load(storeName, &map[string]any{}); ok {
		t.Fatal("校验失败不应写文件")
	}
}

func TestPatchNullRestoresDefault(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"testUrl":"https://example.com/204"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Patch(ctx, j(t, `{"testUrl":null}`))
	if err != nil {
		t.Fatal(err)
	}
	// null 删键,下一次读取由默认值补上
	again, _ := svc.Get(ctx)
	if again["testUrl"] != Default()["testUrl"] {
		t.Fatalf("删除后应回到默认值,得到 %v(Patch 返回 %v)", again["testUrl"], got["testUrl"])
	}
}

func TestPatchIgnoresRulesetDir(t *testing.T) {
	svc, _ := newSvc(t, nil)
	got, err := svc.Patch(context.Background(), j(t, `{"rulesetDir":"/etc/passwd","ipv6":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["rulesetDir"]; has {
		t.Fatal("rulesetDir 由服务端决定,不能被客户端写入")
	}
}

func TestChainSummaries(t *testing.T) {
	svc, st := newSvc(t, nil)
	ctx := context.Background()
	// 先放一份带摘要的旧档案(摘要将来由订阅解析写入,这里直接落盘模拟)
	seed := map[string]any{"chainProxies": []any{
		map[string]any{"id": "c1", "link": "ss://a", "name": "A", "node": map[string]any{"type": "shadowsocks", "server": "1.2.3.4", "port": 8388.0}},
		map[string]any{"id": "c2", "link": "ss://b", "name": "B", "node": map[string]any{"type": "shadowsocks", "server": "5.6.7.8", "port": 1.0}},
	}}
	if err := st.Save(storeName, seed); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Patch(ctx, j(t, `{"chainProxies":[
		{"id":"c1","link":"ss://a","name":"A改名","node":{"type":"伪造","server":"x","port":1}},
		{"id":"c2","link":"ss://changed","name":"B","node":{"type":"伪造","server":"x","port":1}},
		{"id":"c3","link":"ss://new","name":"C","node":{"type":"伪造","server":"x","port":1}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	list := got["chainProxies"].([]any)
	n1 := list[0].(map[string]any)["node"].(map[string]any)
	if n1["server"] != "1.2.3.4" {
		t.Errorf("link 没变应沿用旧摘要,得到 %v", n1)
	}
	if _, has := list[1].(map[string]any)["node"]; has {
		t.Error("link 变了,旧摘要应丢弃")
	}
	if _, has := list[2].(map[string]any)["node"]; has {
		t.Error("新条目不能带客户端传来的摘要")
	}
}

func TestLegacyRoutingMigratedOnceAndPersisted(t *testing.T) {
	fc := &fakeCore{}
	svc, st := newSvc(t, fc)
	legacy := map[string]any{"routing": map[string]any{"regions": []any{map[string]any{"id": "r1"}}, "regionId": "r1"}}
	if err := st.Save(storeName, legacy); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pol := got["routing"].(map[string]any)["policies"].([]any)
	if len(pol) != 1 || pol[0].(map[string]any)["id"] != "legacy-1" {
		t.Fatalf("旧路由应被迁移成站点集,得到 %v", got["routing"])
	}
	if _, has := got["routing"].(map[string]any)["regions"]; has {
		t.Fatal("迁移后不应再有 regions")
	}
	disk := map[string]any{}
	if _, err := st.Load(storeName, &disk); err != nil {
		t.Fatal(err)
	}
	if _, has := disk["routing"].(map[string]any)["regions"]; has {
		t.Fatal("迁移结果应写回档案")
	}
	before := fc.migrateCalls.Load()
	if _, err := svc.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if fc.migrateCalls.Load() != before {
		t.Fatal("已迁移的档案再读取不应再调用核心")
	}
}

func TestLegacyRoutingWithoutCoreOrOnCoreError(t *testing.T) {
	legacy := map[string]any{"routing": map[string]any{"regions": []any{}}}
	for name, c := range map[string]Core{"无核心": nil, "核心报错": &fakeCore{migrateErr: errors.New("boom")}} {
		t.Run(name, func(t *testing.T) {
			svc, st := newSvc(t, c)
			if err := st.Save(storeName, legacy); err != nil {
				t.Fatal(err)
			}
			got, err := svc.Get(context.Background())
			if err != nil {
				t.Fatalf("迁移不可用时读取不应失败: %v", err)
			}
			if _, has := got["routing"].(map[string]any)["regions"]; !has {
				t.Fatal("迁移失败时应保持原样")
			}
		})
	}
}

func TestModernRoutingNeverCallsCore(t *testing.T) {
	fc := &fakeCore{}
	svc, _ := newSvc(t, fc)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"ipv6":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if fc.migrateCalls.Load() != 0 {
		t.Fatal("新版分流不该启动核心进程(路由器上每次 GET 都起子进程太贵)")
	}
}

func TestStoredRoutingNotMixedWithDefaultPolicies(t *testing.T) {
	svc, st := newSvc(t, nil)
	if err := st.Save(storeName, map[string]any{"routing": map[string]any{"policies": []any{}}}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(context.Background())
	if n := len(got["routing"].(map[string]any)["policies"].([]any)); n != 0 {
		t.Fatalf("用户清空的站点集不能被默认值补回来,得到 %d 个", n)
	}
}

func TestDefaultGroups(t *testing.T) {
	svc, _ := newSvc(t, nil)
	if _, err := svc.DefaultGroups(context.Background()); !errors.Is(err, ErrCoreUnavailable) {
		t.Fatalf("无核心应返回 ErrCoreUnavailable,得到 %v", err)
	}
	svc, _ = newSvc(t, &fakeCore{})
	g, err := svc.DefaultGroups(context.Background())
	if err != nil || len(g) != 3 {
		t.Fatalf("默认节点组 = %v, %v", g, err)
	}
}

func TestPatchConcurrent(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			_, err := svc.Patch(ctx, map[string]any{"k" + string(rune('a'+i)): float64(i)})
			done <- err
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	got, _ := svc.Get(ctx)
	for i := 0; i < 20; i++ {
		if got["k"+string(rune('a'+i))] != float64(i) {
			t.Fatalf("并发补丁丢了第 %d 个", i)
		}
	}
}
