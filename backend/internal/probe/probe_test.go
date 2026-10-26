package probe

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func needSingBox(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过(云端 Actions 里装好后跑)")
	}
	return p
}

func testCfg() map[string]any { return testCfgWith("127.0.0.1", 1) }

// testCfgWith:「链首」直连前向代理 proxyHost:proxyPort,「链尾」再经「链首」去连它(隧道里再套一层)。
func testCfgWith(proxyHost string, proxyPort int) map[string]any {
	return map[string]any{
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "直连"},
			map[string]any{"type": "block", "tag": "拒绝"},
			// 空的 direct 不能当 detour(sing-box 连接时拒绝),所以链首也用 http 出站(直接连前向代理)
			map[string]any{"type": "http", "tag": "链首", "server": proxyHost, "server_port": proxyPort},
			map[string]any{"type": "http", "tag": "链尾", "detour": "链首", "server": proxyHost, "server_port": proxyPort},
			map[string]any{"type": "selector", "tag": "组", "outbounds": []any{"直连", "拒绝"}, "default": "直连"},
			// 配置有毛病的节点:引用不存在的 detour
			map[string]any{"type": "direct", "tag": "坏", "detour": "不存在"},
		},
		"dns": map[string]any{"servers": []any{
			map[string]any{"type": "local", "tag": "dns-a"},
		}},
	}
}

func TestSubset(t *testing.T) {
	cfg := testCfg()
	m := Subset(cfg, []string{"链尾"})
	if len(m.Outbounds) != 2 {
		t.Fatalf("链尾应带上它的 detour: %v", m.Outbounds)
	}
	if !m.Has("链尾") || !m.Has("链首") {
		t.Fatal("Has")
	}
	if m.Has("组") || m.Has("不存在") {
		t.Fatal("组 / 不存在的不能算可测节点")
	}
	g := Subset(cfg, []string{"组"})
	if len(g.Outbounds) != 3 { // 组 + 它的两个成员
		t.Fatalf("组成员要一起摘: %v", g.Outbounds)
	}
	// domain_resolver 指向的 DNS 服务器要带上
	cfg2 := map[string]any{
		"outbounds": []any{map[string]any{"type": "direct", "tag": "n", "domain_resolver": map[string]any{"server": "dns-a"}}},
		"dns":       map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "dns-a"}, map[string]any{"type": "local", "tag": "dns-b"}}},
	}
	if d := Subset(cfg2, []string{"n"}).DNS; len(d) != 1 {
		t.Fatalf("只带被引用的 DNS: %v", d)
	}
}

func TestRunReal(t *testing.T) {
	bin := needSingBox(t)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(204)
	}))
	defer ok.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer slow.Close()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closedURL := "http://" + l.Addr().String() + "/"
	l.Close()

	// 前向代理:不管请求什么,都回 204(验证链式出站确实经它转发)
	var hits int
	var hmu sync.Mutex
	fwd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hmu.Lock()
		hits++
		hmu.Unlock()
		if r.Method != http.MethodConnect {
			w.WriteHeader(204)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		_ = rw.Flush()
		// 隧道里可能还有一层 CONNECT(链首 → 链尾):逐层回 200,最后的普通请求回 204
		br := bufio.NewReader(rw)
		for {
			inner, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			hmu.Lock()
			hits++
			hmu.Unlock()
			if inner.Method == http.MethodConnect {
				_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				continue
			}
			_, _ = c.Write([]byte("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n"))
			return
		}
	}))
	defer fwd.Close()
	fa := fwd.Listener.Addr().(*net.TCPAddr)
	cfg := testCfgWith("127.0.0.1", fa.Port)
	r := &Runner{Bin: bin, Dir: t.TempDir()}
	jobs := []Job{
		{"直连", ok.URL}, {"拒绝", ok.URL}, {"链尾", "http://chain-target.invalid/"}, {"直连", slow.URL},
		{"直连", closedURL}, {"组", ok.URL}, {"没有", ok.URL}, {"直连", "ftp://x"},
	}
	var mu sync.Mutex
	done := map[int]bool{}
	res, err := r.Run(context.Background(), Subset(cfg, []string{"直连", "拒绝", "链尾", "组"}), jobs, Options{
		Timeout: time.Second, OnDone: func(i int) { mu.Lock(); done[i] = true; mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	hmu.Lock()
	if hits == 0 {
		t.Fatal("链式出站应经前向代理转发")
	}
	hmu.Unlock()
	if !res[0].OK || res[0].Ms < 50 || res[0].Ms > 900 {
		t.Fatalf("直连应通且 ≥60ms 附近: %+v", res[0])
	}
	if res[1].OK {
		t.Fatalf("拒绝出站不应通: %+v", res[1])
	}
	if !res[2].OK {
		t.Fatalf("链式(detour)应通: %+v", res[2])
	}
	if res[3].OK || res[3].Reason != "timeout" {
		t.Fatalf("慢服务应超时: %+v", res[3])
	}
	if res[4].OK || res[4].Reason == "" {
		t.Fatalf("关闭的端口应失败: %+v", res[4])
	}
	for i, want := range map[int]string{5: "not-found", 6: "not-found", 7: "invalid"} {
		if res[i].OK || res[i].Reason != want {
			t.Fatalf("jobs[%d] 应为 %s: %+v", i, want, res[i])
		}
	}
	if len(done) != len(jobs) {
		t.Fatalf("OnDone 每个任务都要调一次: %v", done)
	}
}

func TestRunInvalidConfig(t *testing.T) {
	bin := needSingBox(t)
	cfg := testCfg()
	// 「坏」引用了不存在的 detour,sing-box 启动会失败;摘取时 detour 缺失不会被补上
	m := Subset(cfg, []string{"坏"})
	r := &Runner{Bin: bin, Dir: t.TempDir()}
	res, err := r.Run(context.Background(), m, []Job{{"坏", "http://127.0.0.1:1/"}}, Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].OK || res[0].Reason != "invalid" || res[0].Error == "" {
		t.Fatalf("配置不合法应为 invalid 并带原因: %+v", res[0])
	}
}

func TestRunNoSingBox(t *testing.T) {
	r := &Runner{Bin: "/nonexistent/sing-box", Dir: t.TempDir()}
	m := Subset(testCfg(), []string{"直连"})
	if _, err := r.Run(context.Background(), m, []Job{{"直连", "http://127.0.0.1:1/"}}, Options{}); err != ErrNoSingBox {
		t.Fatalf("got %v", err)
	}
	// 全是不可测任务时不需要 sing-box
	res, err := r.Run(context.Background(), m, []Job{{"没有", "http://x/"}}, Options{})
	if err != nil || res[0].Reason != "not-found" {
		t.Fatalf("%v %+v", err, res)
	}
}
