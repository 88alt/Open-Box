package clientapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type egressFake struct {
	mu    sync.Mutex
	ip    string
	cc    string
	err   error
	calls int
}

func (f *egressFake) detect(context.Context) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.ip, f.cc, f.err
}

func newEgressSvc(t *testing.T, f *egressFake, running bool, now *time.Time) *Service {
	t.Helper()
	s := newSvc(t, nil)
	s.DetectEgress = f.detect
	s.Running = func(context.Context) (bool, error) { return running, nil }
	s.Now = func() time.Time { return *now }
	return s
}

func TestEgressDetectAndCache(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	f := &egressFake{ip: "203.0.113.7", cc: "jp"}
	s := newEgressSvc(t, f, true, &now)
	ctx := context.Background()

	if info, _ := s.Info(ctx); info.EgressCountry != nil {
		t.Fatalf("没判过应为 null: %+v", info.EgressCountry)
	}
	a, err := s.DetectEgressCountry(ctx)
	if err != nil || a.IP != "203.0.113.7" || a.Country != "JP" || a.At != 1_700_000_000_000 {
		t.Fatalf("%+v %v", a, err)
	}
	// Info 带回缓存;换实例(模拟重启)还在
	if info, _ := s.Info(ctx); info.EgressCountry == nil || *info.EgressCountry != a {
		t.Fatalf("info=%+v", info.EgressCountry)
	}
	s2 := &Service{Store: s.Store, Profile: s.Profile}
	if info, _ := s2.Info(ctx); info.EgressCountry == nil || *info.EgressCountry != a {
		t.Fatalf("重启后 info=%+v", info.EgressCountry)
	}

	// 出口 IP 没变:沿用国家(即使接口这次给了别的国家),时刻更新成现在
	now = now.Add(26 * time.Hour)
	f.cc = "US"
	b, _ := s.DetectEgressCountry(ctx)
	if b.Country != "JP" || b.IP != a.IP || b.At != now.UnixMilli() {
		t.Fatalf("IP 没变应沿用国家并刷新时刻: %+v", b)
	}
	if info, _ := s.Info(ctx); info.EgressCountry.At != now.UnixMilli() {
		t.Fatalf("刷新没落盘: %+v", info.EgressCountry)
	}
	// 时钟倒退:不把时刻改小
	now = now.Add(-48 * time.Hour)
	c, _ := s.DetectEgressCountry(ctx)
	if c.At != b.At {
		t.Fatalf("时刻不该倒退: %+v → %+v", b, c)
	}
	// 出口 IP 变了:换国家
	now = now.Add(72 * time.Hour)
	f.ip, f.cc = "2001:db8::1", "de"
	d, _ := s.DetectEgressCountry(ctx)
	if d.Country != "DE" || d.IP != "2001:db8::1" || d.At != now.UnixMilli() {
		t.Fatalf("%+v", d)
	}
}

func TestEgressNotRunningOrUnavailable(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	f := &egressFake{ip: "203.0.113.7", cc: "JP"}
	ctx := context.Background()

	if _, err := newEgressSvc(t, f, false, &now).DetectEgressCountry(ctx); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("内核没起: %v", err)
	}
	if f.calls != 0 {
		t.Fatal("内核没起不该联网判断")
	}
	if _, err := newSvc(t, nil).DetectEgressCountry(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("没接判断: %v", err)
	}
	s := newEgressSvc(t, f, true, &now)
	s.Running = nil
	if _, err := s.DetectEgressCountry(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("没接服务状态: %v", err)
	}
	// 没有服务管理器(面板不在路由器上):Running 报 ErrUnavailable,按「不可用」处理,也不联网
	s = newEgressSvc(t, f, true, &now)
	s.Running = func(context.Context) (bool, error) { return false, ErrUnavailable }
	if _, err := s.DetectEgressCountry(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("没有服务管理器: %v", err)
	}
	// 读不出服务状态:报错,不猜它在不在跑
	s = newEgressSvc(t, f, true, &now)
	s.Running = func(context.Context) (bool, error) { return false, errors.New("ubus 不可用") }
	if _, err := s.DetectEgressCountry(ctx); err == nil || errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "ubus 不可用") {
		t.Fatalf("读不出状态: %v", err)
	}
	if f.calls != 0 {
		t.Fatal("没确认内核在跑,不该联网判断")
	}
}

func TestEgressDetectFailures(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	ctx := context.Background()
	f := &egressFake{err: errors.New("判断地区失败:api.ip.sb: 超时")}
	s := newEgressSvc(t, f, true, &now)
	var de *DetectError
	if _, err := s.DetectEgressCountry(ctx); !errors.As(err, &de) || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("%v", err)
	}
	// 失败不能留缓存
	if info, _ := s.Info(ctx); info.EgressCountry != nil {
		t.Fatalf("失败后不该有缓存: %+v", info.EgressCountry)
	}
	// 接口给了不合格的东西:当失败
	for _, bad := range []struct{ ip, cc string }{{"not-ip", "JP"}, {"203.0.113.7", ""}, {"203.0.113.7", "JPN"}, {"203.0.113.7", "1A"}, {"", ""}} {
		f2 := &egressFake{ip: bad.ip, cc: bad.cc}
		if _, err := newEgressSvc(t, f2, true, &now).DetectEgressCountry(ctx); !errors.As(err, &de) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// 取消:带取消原因,不包成 502
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	f3 := &egressFake{err: errors.New("已取消")}
	if _, err := newEgressSvc(t, f3, true, &now).DetectEgressCountry(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消: %v", err)
	}
}

// 手工改坏的缓存:Info 当作没有(null),下次判断会重新写好;路由器 ID 等别的字段不受影响。
func TestEgressBadCacheIgnored(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	f := &egressFake{ip: "203.0.113.7", cc: "JP"}
	s := newEgressSvc(t, f, true, &now)
	ctx := context.Background()
	id, _ := s.RouterID(ctx)
	if err := s.Store.Save(stateFile, map[string]any{"routerId": id, "egressCountry": map[string]any{"ip": "x", "country": "jp", "at": -1}}); err != nil {
		t.Fatal(err)
	}
	if info, err := s.Info(ctx); err != nil || info.EgressCountry != nil || info.RouterID != id {
		t.Fatalf("%+v %v", info.EgressCountry, err)
	}
	if got, err := s.DetectEgressCountry(ctx); err != nil || got.Country != "JP" {
		t.Fatalf("%+v %v", got, err)
	}
	if id2, _ := s.RouterID(ctx); id2 != id {
		t.Fatal("路由器 ID 不该变")
	}
}

// 出口判断与配对码 / 开关共用 client-app.json,互不覆盖。
func TestEgressCoexistsWithClientConfig(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	f := &egressFake{ip: "203.0.113.7", cc: "JP"}
	s := newEgressSvc(t, f, true, &now)
	ctx := context.Background()
	cfg, _ := s.ClientConfig(ctx)
	s.SetClientConfigEnabled(ctx, true)
	if _, err := s.DetectEgressCountry(ctx); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.ClientConfig(ctx); !after.Enabled || after.PairCode != cfg.PairCode {
		t.Fatalf("配置被冲掉: %+v", after)
	}
	if _, err := s.RotateClientConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if info, _ := s.Info(ctx); info.EgressCountry == nil || info.EgressCountry.Country != "JP" {
		t.Fatalf("出口国家被冲掉: %+v", info.EgressCountry)
	}
}
