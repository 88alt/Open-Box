package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
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
		{Tag: "直连", URL: ok.URL}, {Tag: "拒绝", URL: ok.URL}, {Tag: "链尾", URL: "http://chain-target.invalid/"}, {Tag: "直连", URL: slow.URL},
		{Tag: "直连", URL: closedURL}, {Tag: "组", URL: ok.URL}, {Tag: "没有", URL: ok.URL}, {Tag: "直连", URL: "ftp://x"},
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
	res, err := r.Run(context.Background(), m, []Job{{Tag: "坏", URL: "http://127.0.0.1:1/"}}, Options{Timeout: time.Second})
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
	if _, err := r.Run(context.Background(), m, []Job{{Tag: "直连", URL: "http://127.0.0.1:1/"}}, Options{}); err != ErrNoSingBox {
		t.Fatalf("got %v", err)
	}
	// 全是不可测任务时不需要 sing-box
	res, err := r.Run(context.Background(), m, []Job{{Tag: "没有", URL: "http://x/"}}, Options{})
	if err != nil || res[0].Reason != "not-found" {
		t.Fatalf("%v %+v", err, res)
	}
}

// TestCapture:取回正文的任务——2xx 才算数,3xx / 4xx / 空正文按失败,按顺序回退,正文截到上限,全部失败时逐家列原因。
func TestCapture(t *testing.T) {
	bin := needSingBox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			_, _ = w.Write([]byte(`{"ip":"203.0.113.9","country":"Testland"}`))
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		case "/bad-utf8":
			_, _ = w.Write([]byte("ok\xff\xfe"))
		case "/empty":
			w.WriteHeader(200)
		case "/redirect":
			http.Redirect(w, r, "/json", http.StatusFound)
		case "/limited":
			http.Error(w, "slow down", http.StatusTooManyRequests)
		case "/slow":
			time.Sleep(3 * time.Second)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	// 同一个服务用两个主机名(127.0.0.1 / localhost)访问,好让错误信息里能分出是哪一家
	u := srv.URL
	l := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)

	cfg := testCfg()
	r := &Runner{Bin: bin, Dir: t.TempDir()}
	jobs := []Job{
		0: {Tag: "直连", URL: u + "/json", Capture: 1024},
		1: {Tag: "直连", URL: u + "/limited", Capture: 1024, Fallback: []string{l + "/json"}},
		2: {Tag: "直连", URL: u + "/big", Capture: 16},
		3: {Tag: "直连", URL: u + "/bad-utf8", Capture: 1024},
		4: {Tag: "直连", URL: u + "/empty", Capture: 1024},
		5: {Tag: "直连", URL: u + "/redirect", Capture: 1024},
		6: {Tag: "直连", URL: u + "/limited", Capture: 1024, Fallback: []string{l + "/missing", "ftp://bad/", u + "/slow"}},
		7: {Tag: "直连", URL: u + "/limited"}, // 普通测速:4xx 也算线路通
		8: {Tag: "直连", URL: u + "/json", Capture: 1024, Fallback: []string{l + "/limited"}},
	}
	res, err := r.Run(context.Background(), Subset(cfg, []string{"直连"}), jobs, Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].OK || res[0].Body != `{"ip":"203.0.113.9","country":"Testland"}` || res[0].URL != u+"/json" {
		t.Fatalf("应取回正文: %+v", res[0])
	}
	if !res[1].OK || res[1].URL != l+"/json" || !strings.Contains(res[1].Body, "Testland") {
		t.Fatalf("第一个 429 后应回退到第二个: %+v", res[1])
	}
	if !res[2].OK || res[2].Body != strings.Repeat("x", 16) {
		t.Fatalf("正文应截到上限: %+v", res[2])
	}
	if !res[3].OK || res[3].Body != "ok\uFFFD" {
		t.Fatalf("非法 UTF-8 应被替换: %q", res[3].Body)
	}
	if res[4].OK || res[4].Error != "响应是空的" {
		t.Fatalf("空正文应失败: %+v", res[4])
	}
	if res[5].OK || res[5].Error != "HTTP 302" {
		t.Fatalf("重定向不跟随、按失败算: %+v", res[5])
	}
	want := "127.0.0.1: HTTP 429;localhost: HTTP 404;127.0.0.1: 超时"
	if res[6].OK || res[6].Error != want {
		t.Fatalf("全部失败应逐家列原因(不合法的回退地址跳过): %q 想要 %q", res[6].Error, want)
	}
	if !res[7].OK || res[7].Body != "" || res[7].URL != "" {
		t.Fatalf("普通测速不取正文: %+v", res[7])
	}
	if !res[8].OK || res[8].URL != u+"/json" {
		t.Fatalf("第一个成功就不再试后面的: %+v", res[8])
	}
	b, _ := json.Marshal(res[0])
	if strings.Contains(string(b), "Testland") || strings.Contains(string(b), "json") {
		t.Fatalf("正文和地址不进 JSON: %s", b)
	}
}
