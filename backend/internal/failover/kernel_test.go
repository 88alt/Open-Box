package failover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// fakeClash 是一个最小的假内核控制接口:/proxies、/proxies/<名>/delay、PUT /proxies/<组>。
type fakeClash struct {
	mu      sync.Mutex
	now     map[string]string // 组 → 当前选择
	dead    map[string]bool   // 节点 → 测速超时(504)
	missing map[string]bool   // 节点 → 404
	puts    [][2]string       // (组, 选择)
	delays  []string
	secret  string
}

func newFakeClash() *fakeClash {
	return &fakeClash{
		now:  map[string]string{"故障转移": "HK-01", "__fo:g1:l2": "JP-01"},
		dead: map[string]bool{}, missing: map[string]bool{},
	}
}

func (f *fakeClash) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.secret != "" && r.Header.Get("Authorization") != "Bearer "+f.secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := r.URL.Path // 已解码:名字里的中文 / 冒号 / 斜杠转义都还原了
		switch {
		case r.Method == http.MethodGet && path == "/proxies":
			ps := map[string]any{"HK-01": map[string]any{"type": "Trojan"}, "JP-01": map[string]any{"type": "Trojan"}, "JP-02": map[string]any{"type": "Trojan"}}
			for g, n := range f.now {
				ps[g] = map[string]any{"type": "Selector", "now": n}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"proxies": ps})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/proxies/") && strings.HasSuffix(path, "/delay"):
			name := strings.TrimSuffix(strings.TrimPrefix(path, "/proxies/"), "/delay")
			f.delays = append(f.delays, name)
			switch {
			case f.missing[name]:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"resource not found"}`))
			case f.dead[name]:
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = w.Write([]byte(`{"message":"Timeout"}`))
			default:
				_, _ = w.Write([]byte(`{"delay":88}`))
			}
		case r.Method == http.MethodPut && strings.HasPrefix(path, "/proxies/"):
			group := strings.TrimPrefix(path, "/proxies/")
			b, _ := io.ReadAll(r.Body)
			var body struct{ Name string }
			_ = json.Unmarshal(b, &body)
			if _, ok := f.now[group]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.puts = append(f.puts, [2]string{group, body.Name})
			f.now[group] = body.Name
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *fakeClash) selected(g string) string { f.mu.Lock(); defer f.mu.Unlock(); return f.now[g] }

func clashConfig(t *testing.T, srv *httptest.Server, secret string) string {
	t.Helper()
	cfg := map[string]any{"experimental": map[string]any{"clash_api": map[string]any{
		"external_controller": strings.TrimPrefix(srv.URL, "http://"), "secret": secret,
	}}}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClashKernelActions(t *testing.T) {
	fc := newFakeClash()
	fc.secret = "s3"
	fc.dead["JP-02"], fc.missing["gone"] = true, true
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()
	k := ClashKernel{ConfigFile: clashConfig(t, srv, "s3")}
	ctx := context.Background()

	sel, err := k.Selected(ctx)
	if err != nil || sel["故障转移"] != "HK-01" || sel["__fo:g1:l2"] != "JP-01" || sel["HK-01"] != "" {
		t.Fatalf("%v %v", sel, err)
	}
	d, err := k.Delay(ctx, "HK-01", "https://www.gstatic.com/generate_204", 3*time.Second)
	if err != nil || d != 88*time.Millisecond {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := k.Delay(ctx, "JP-02", "https://www.gstatic.com/generate_204", time.Second); !singbox.IsTimeout(err) {
		t.Fatalf("超时要能被认出来(管理器据此记 kind=timeout): %v", err)
	}
	if _, err := k.Delay(ctx, "gone", "https://www.gstatic.com/generate_204", time.Second); !singbox.IsNotFound(err) {
		t.Fatalf("%v", err)
	}
	// 名字里的中文和冒号:经转义发出去、服务端还原后对得上
	if err := k.Select(ctx, "故障转移", "__fo:g1:l2"); err != nil {
		t.Fatal(err)
	}
	if fc.selected("故障转移") != "__fo:g1:l2" || len(fc.puts) != 1 {
		t.Fatalf("%v", fc.puts)
	}
	if err := k.Select(ctx, "不存在的组", "x"); !singbox.IsNotFound(err) {
		t.Fatalf("%v", err)
	}
	// secret 不对:读不到(管理器据此不测不切)
	bad := ClashKernel{ConfigFile: clashConfig(t, srv, "wrong")}
	if _, err := bad.Selected(ctx); err == nil {
		t.Fatal("secret 不对应当报错")
	}
	// 配置读不到 / 内核没起来
	if _, err := (ClashKernel{ConfigFile: filepath.Join(t.TempDir(), "nope.json")}).Selected(ctx); err == nil {
		t.Fatal("配置读不到应当报错")
	}
	srv.Close()
	if _, err := k.Selected(ctx); err == nil {
		t.Fatal("内核没在运行应当报错")
	}
}

// 真实适配器 + 真实管理器 + 假内核:主用节点测不通 → 连续确认后切到备用;不是一次失败就切。
func TestManagerOverClashKernelFailsOverAfterConfirmation(t *testing.T) {
	fc := newFakeClash()
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()
	c := &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	m := NewManager(Options{Kernel: ClashKernel{ConfigFile: clashConfig(t, srv, "")}, Now: c.Now})

	var v any
	if err := json.Unmarshal([]byte(corePlan), &v); err != nil {
		t.Fatal(err)
	}
	plan, err := ParsePlan(v)
	if err != nil {
		t.Fatal(err)
	}
	m.SetPlan(plan)

	round := func() {
		t.Helper()
		m.Step(context.Background())
		m.Wait()
		c.Add(5 * time.Minute) // 下一轮到时间
	}

	round() // 第 1 轮:全都通,主用在用
	if got := fc.selected("故障转移"); got != "HK-01" {
		t.Fatalf("健康时不该动: %s", got)
	}
	fc.mu.Lock()
	fc.dead["HK-01"] = true
	fc.mu.Unlock()
	round() // 第 1 次失败:还没确认,不切
	if got := fc.selected("故障转移"); got != "HK-01" || len(fc.puts) != 0 {
		t.Fatalf("一次失败不该切: %s %v", got, fc.puts)
	}
	round() // 连续第 2 次:确认,切到备用页签
	if got := fc.selected("故障转移"); got != "__fo:g1:l2" || len(fc.puts) != 1 {
		t.Fatalf("连续失败确认后要切到备用: %s %v", got, fc.puts)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].LastSwitch == nil || snap[0].LastSwitch.To.Ref != "__fo:g1:l2" || snap[0].Lanes["l1"].Health != "down" {
		t.Fatalf("%+v", snap)
	}
	if r := snap[0].Lanes["l1"].Nodes["HK-01"]; r.Kind == nil || *r.Kind != KindTimeout {
		t.Fatalf("真实内核回 504 要记成 timeout: %+v", r)
	}
	if got, limit := m.Probes(); got != 0 || limit != 4 {
		t.Fatalf("测完名额应已放回: %d/%d", got, limit)
	}
}

// 内核没在运行:管理器不测、不切,也不 panic;内核起来之后恢复。
func TestManagerOverClashKernelIdlesWhileKernelIsDown(t *testing.T) {
	fc := newFakeClash()
	srv := httptest.NewServer(fc.handler())
	cfg := clashConfig(t, srv, "")
	srv.Close() // 模拟内核没在运行
	c := &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	m := NewManager(Options{Kernel: ClashKernel{ConfigFile: cfg}, Now: c.Now})
	m.SetPlan(planOf(t))
	m.Step(context.Background())
	m.Wait()
	if len(fc.delays) != 0 || len(fc.puts) != 0 {
		t.Fatalf("内核没在运行时不该测也不该切: %v %v", fc.delays, fc.puts)
	}
	snap := m.Snapshot()
	if snap[0].Status == "ok" || snap[0].Status == "backup" || snap[0].Status == "failing" {
		t.Fatalf("读不到内核不能说出一个确定的状态: %s", snap[0].Status)
	}
}
