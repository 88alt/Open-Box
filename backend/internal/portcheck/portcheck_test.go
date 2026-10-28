package portcheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func listeners(tcp, udp []int) func() (*system.Listeners, error) {
	return func() (*system.Listeners, error) {
		l := &system.Listeners{TCP: map[int]bool{}, UDP: map[int]bool{}}
		for _, p := range tcp {
			l.TCP[p] = true
		}
		for _, p := range udp {
			l.UDP[p] = true
		}
		return l, nil
	}
}

func fixed(ss ...Server) func(context.Context) ([]Server, error) {
	return func(context.Context) ([]Server, error) { return ss, nil }
}

func TestCheck(t *testing.T) {
	c := &Checker{
		Servers:   fixed(Server{ID: "a", Name: "客厅 SS", Port: 8388}, Server{ID: "b", Name: "TUIC", Port: 8444}),
		Reserved:  []int{3036},
		Listeners: listeners([]int{22, 8443}, []int{53, 51820}),
	}
	cases := []struct {
		name string
		port int
		id   string
		want Result
	}{
		{"可用", 9000, "new", Result{OK: true}},
		{"0 无效", 0, "new", Result{Reason: "invalid"}},
		{"负数无效", -5, "new", Result{Reason: "invalid"}},
		{"65536 无效", 65536, "new", Result{Reason: "invalid"}},
		{"65535 有效", 65535, "new", Result{OK: true}},
		{"1 有效", 1, "new", Result{OK: true}},
		{"面板自己的端口", 3036, "new", Result{Reason: "reserved"}},
		{"内核固定端口 7891", 7891, "new", Result{Reason: "reserved"}},
		{"内核固定端口 9095", 9095, "new", Result{Reason: "reserved"}},
		{"别的服务器占着", 8388, "b", Result{Reason: "server", Name: "客厅 SS"}},
		{"新建时端口被已有服务器占着", 8444, "new", Result{Reason: "server", Name: "TUIC"}},
		{"自己占的端口不算冲突", 8388, "a", Result{OK: true}},
		{"别的服务在监听 TCP", 22, "new", Result{Reason: "listening"}},
		{"别的服务在监听 UDP", 53, "new", Result{Reason: "listening"}},
		{"编辑已有服务器改到被占的端口", 8443, "a", Result{Reason: "listening"}},
		{"自用优先于别的服务器", 7853, "a", Result{Reason: "reserved"}},
	}
	for _, tc := range cases {
		got, err := c.Check(context.Background(), tc.port, tc.id)
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v %v,想要 %+v", tc.name, got, err, tc.want)
		}
	}
}

// 自己的端口不查监听:已保存且端口没变的服务器,端口是内核替它占着的。
func TestCheckOwnPortSkipsListening(t *testing.T) {
	c := &Checker{Servers: fixed(Server{ID: "a", Name: "SS", Port: 8388}), Listeners: listeners([]int{8388}, nil)}
	if got, err := c.Check(context.Background(), 8388, "a"); err != nil || !got.OK {
		t.Fatalf("%+v %v", got, err)
	}
	// 别的 id 用同一个端口 → 是「别的服务器」,不是「在监听」
	if got, _ := c.Check(context.Background(), 8388, "other"); got.Reason != "server" {
		t.Fatalf("%+v", got)
	}
}

// 读不到端口占用情况、读不到档案:返回错误,不猜。已经能判定的结果(无效 / 自用 / 别的服务器)不需要读占用情况。
func TestCheckErrors(t *testing.T) {
	boom := errors.New("boom")
	c := &Checker{Servers: fixed(), Listeners: func() (*system.Listeners, error) { return nil, boom }}
	if _, err := c.Check(context.Background(), 9000, "n"); !errors.Is(err, boom) {
		t.Fatalf("占用情况读不到应报错: %v", err)
	}
	for _, p := range []int{0, 7891} {
		if got, err := c.Check(context.Background(), p, "n"); err != nil || got.OK {
			t.Fatalf("无效 / 自用不依赖占用情况: %+v %v", got, err)
		}
	}
	c = &Checker{Servers: func(context.Context) ([]Server, error) { return nil, boom }, Listeners: listeners(nil, nil)}
	if _, err := c.Check(context.Background(), 9000, "n"); !errors.Is(err, boom) {
		t.Fatalf("档案读不到应报错: %v", err)
	}
	// 没接档案来源:当作没有服务器
	c = &Checker{Listeners: listeners(nil, nil)}
	if got, err := c.Check(context.Background(), 9000, "n"); err != nil || !got.OK {
		t.Fatalf("%+v %v", got, err)
	}
}

// 已部署的配置补充内核实际占用的端口:控制接口、非共享入站算自用;共享服务器自己的入站(share-*)不算。
func TestCheckReadsDeployedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"inbounds":[
	  {"tag":"panel-in","type":"mixed","listen_port":7891},
	  {"tag":"my-extra","type":"direct","listen_port":17000},
	  {"tag":"share-a","type":"shadowsocks","listen_port":8388},
	  {"tag":"tun-in","type":"tun"}],
	 "experimental":{"clash_api":{"external_controller":"0.0.0.0:19095"}}}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Checker{ConfigPath: path, Servers: fixed(Server{ID: "a", Name: "SS", Port: 8388}), Listeners: listeners(nil, nil)}
	for _, p := range []int{17000, 19095} {
		if got, _ := c.Check(context.Background(), p, "n"); got.Reason != "reserved" {
			t.Errorf("端口 %d 应是自用: %+v", p, got)
		}
	}
	if got, _ := c.Check(context.Background(), 8388, "a"); !got.OK {
		t.Errorf("共享服务器自己的入站不该被当成自用: %+v", got)
	}
	if got, _ := c.Check(context.Background(), 8388, "n"); got.Reason != "server" {
		t.Errorf("%+v", got)
	}
	// 配置读不到 / 不是 JSON:退回固定列表,不报错
	for _, bad := range []string{filepath.Join(t.TempDir(), "没有"), func() string {
		p := filepath.Join(t.TempDir(), "x.json")
		_ = os.WriteFile(p, []byte("不是 JSON"), 0o600)
		return p
	}()} {
		c := &Checker{ConfigPath: bad, Listeners: listeners(nil, nil)}
		if got, err := c.Check(context.Background(), 7891, "n"); err != nil || got.Reason != "reserved" {
			t.Errorf("%s: %+v %v", bad, got, err)
		}
		if got, err := c.Check(context.Background(), 17000, "n"); err != nil || !got.OK {
			t.Errorf("%s: %+v %v", bad, got, err)
		}
	}
}

func TestServersFromProfile(t *testing.T) {
	p := map[string]any{"servers": []any{
		map[string]any{"id": "a", "name": "SS", "port": float64(8388)},
		map[string]any{"id": "b", "name": "T", "port": 8444},
		map[string]any{"id": "c", "name": "无端口"},
		map[string]any{"id": "d", "name": "坏端口", "port": "abc"},
		"垃圾",
		map[string]any{"id": "e", "name": "零", "port": float64(0)},
	}}
	got := ServersFromProfile(p)
	if len(got) != 2 || got[0] != (Server{ID: "a", Name: "SS", Port: 8388}) || got[1].Port != 8444 {
		t.Fatalf("%+v", got)
	}
	if len(ServersFromProfile(map[string]any{})) != 0 || len(ServersFromProfile(nil)) != 0 {
		t.Fatal("没有 servers 应是空")
	}
}
