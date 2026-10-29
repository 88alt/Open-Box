package latency

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

var (
	buildOnce sync.Once
	corePath  string
	buildErr  error
)

// realCore 编译仓库里的 core/ 当真核心用;没有 Go 工具链或编译失败时跳过。
func realCore(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "obcore")
		if err != nil {
			buildErr = err
			return
		}
		corePath = filepath.Join(dir, "openbox-core")
		cmd := exec.Command("go", "build", "-o", corePath, "./cmd/openbox-core")
		cmd.Dir = filepath.Join("..", "..", "..", "..", "core")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("%w: %s", err, out)
		}
	})
	if buildErr != nil {
		t.Skipf("编译不了 core,跳过: %v", buildErr)
	}
	return corePath
}

// fwdProxy 是测试用的前向代理:CONNECT 逐层回 200(链式时隧道里还有一层),最后的普通请求回 204。
func fwdProxy(t *testing.T) (port int, hits func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	bump := func() { mu.Lock(); n++; mu.Unlock() }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bump()
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		_ = rw.Flush()
		br := bufio.NewReader(rw)
		for {
			in, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			bump()
			if in.Method == http.MethodConnect {
				_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				continue
			}
			_, _ = c.Write([]byte("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n"))
			return
		}
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

type env struct {
	mux  *http.ServeMux
	subs *subs.Service
	prof *profile.Service
	hist *latencyhistory.Service
}

func newEnv(t *testing.T, singbox_ string) *env {
	t.Helper()
	cc := core.New(realCore(t))
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{subs: subs.New(st, nil), mux: http.NewServeMux()}
	e.prof = profile.New(st, profile.CoreAdapter{C: cc}, nil)
	groups := nodegroups.New(st, nodegroups.CoreAdapter{C: cc}, e.subs, nodegroups.ChainsFromProfile(e.prof))
	gen := singbox.NewGenerator(cc, st, e.prof, groups, e.subs, singbox.System{}, singbox.Options{
		RulesetDir: "/data/rules", CacheFile: "/data/cache.db", CertPath: "/etc/c.crt", KeyPath: "/etc/c.key",
	})
	e.hist = &latencyhistory.Service{Store: st}
	Register(e.mux, Deps{Gen: gen, Subs: e.subs, Profiles: e.prof, Runner: &probe.Runner{Bin: singbox_, Dir: t.TempDir()}, History: e.hist})
	return e
}

func needSingBox(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过(云端 Actions 里装好后跑)")
	}
	return p
}

func (e *env) addSub(t *testing.T, name, content string) {
	t.Helper()
	nm := name
	if _, err := e.subs.Create(context.Background(), subs.Input{Name: &nm, Source: &subs.Source{Content: content}, Rename: map[string]any{"enabled": false}}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) post(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

type result struct {
	OK     bool   `json:"ok"`
	Ms     int    `json:"ms"`
	Error  string `json:"error"`
	Reason string `json:"reason"`
}

func decodeResults(t *testing.T, w *httptest.ResponseRecorder) []result {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("状态 %d: %s", w.Code, w.Body)
	}
	var out struct {
		Results []result `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Results
}

const target = "http://t.invalid/"

func TestStoredJobs(t *testing.T) {
	bin := needSingBox(t)
	port, hits := fwdProxy(t)
	e := newEnv(t, bin)
	e.addSub(t, "A", fmt.Sprintf("http://u:p@127.0.0.1:%d#PX 01\n", port))

	w := e.post(t, "/api/openbox/nodes/latency", map[string]any{"jobs": []any{
		map[string]any{"tag": "PX 01", "url": target},
		map[string]any{"tag": "已删除的节点", "url": target},
		map[string]any{"tag": "自动选择", "url": target}, // 是组
	}, "timeoutMs": 3000})
	res := decodeResults(t, w)
	if len(res) != 3 {
		t.Fatalf("结果数 %d", len(res))
	}
	if !res[0].OK || res[0].Ms < 1 {
		t.Fatalf("节点应通: %+v", res[0])
	}
	if hits() == 0 {
		t.Fatal("应经节点(前向代理)转发")
	}
	for _, i := range []int{1, 2} {
		if res[i].OK || res[i].Reason != "not-found" {
			t.Fatalf("jobs[%d] 应为 not-found: %+v", i, res[i])
		}
	}
	// 通了的节点记进历史并随响应带回整份;没测的(not-found)不记
	var body struct {
		History latencyhistory.History `json:"history"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := body.History["PX 01"]; len(got) != 1 || got[0].Delay < 1 {
		t.Fatalf("响应的 history 里应有 PX 01 的一条样本: %+v", body.History)
	}
	if _, has := body.History["已删除的节点"]; has {
		t.Errorf("not-found 不该记: %+v", body.History)
	}
	if h, _, _ := e.hist.Get(context.Background()); len(h["PX 01"]) != 1 {
		t.Errorf("应已写进服务端的延迟历史: %+v", h)
	}
}

func TestStoredJobsRecordFalse(t *testing.T) {
	bin := needSingBox(t)
	port, _ := fwdProxy(t)
	e := newEnv(t, bin)
	e.addSub(t, "A", fmt.Sprintf("http://u:p@127.0.0.1:%d#PX 01\n", port))
	w := e.post(t, "/api/openbox/nodes/latency", map[string]any{"jobs": []any{
		map[string]any{"tag": "PX 01", "url": target, "record": false},
	}, "timeoutMs": 3000})
	if res := decodeResults(t, w); len(res) != 1 || !res[0].OK {
		t.Fatalf("应通: %+v", res)
	}
	if strings.Contains(w.Body.String(), `"history"`) {
		t.Errorf("全都 record=false 时响应不该带 history: %s", w.Body)
	}
	if h, _, _ := e.hist.Get(context.Background()); len(h) != 0 {
		t.Errorf("record=false 不该记: %+v", h)
	}
}

func TestStoredJobsStream(t *testing.T) {
	bin := needSingBox(t)
	port, _ := fwdProxy(t)
	e := newEnv(t, bin)
	e.addSub(t, "A", fmt.Sprintf("http://u:p@127.0.0.1:%d#PX 01\nhttp://u:p@127.0.0.1:%d#PX 02\n", port, port))

	w := e.post(t, "/api/openbox/nodes/latency", map[string]any{"progress": true, "jobs": []any{
		map[string]any{"tag": "PX 01", "url": target}, map[string]any{"tag": "PX 02", "url": target}, map[string]any{"tag": "nope", "url": target},
	}})
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Content-Type"))
	}
	done := map[int]bool{}
	var final []result
	var finalHist latencyhistory.History
	sc := bufio.NewScanner(w.Body)
	for sc.Scan() {
		var msg struct {
			Done    []int                  `json:"done"`
			Results []result               `json:"results"`
			History latencyhistory.History `json:"history"`
			Error   string                 `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			t.Fatalf("不是 JSON 行: %q", sc.Text())
		}
		if msg.History != nil {
			finalHist = msg.History
		}
		if msg.Error != "" {
			t.Fatal(msg.Error)
		}
		for _, i := range msg.Done {
			done[i] = true
		}
		if msg.Results != nil {
			final = msg.Results
		}
	}
	if len(done) != 3 || len(final) != 3 || !final[0].OK || !final[1].OK || final[2].Reason != "not-found" {
		t.Fatalf("done=%v final=%+v", done, final)
	}
	// 带进度的返回:最后一行除了 results 还带整份 history,没测的 nope 不记
	if len(finalHist["PX 01"]) != 1 || len(finalHist["PX 02"]) != 1 {
		t.Fatalf("最后一行应带 history: %+v", finalHist)
	}
	if _, has := finalHist["nope"]; has {
		t.Errorf("not-found 不该记: %+v", finalHist)
	}
}

func TestPreview(t *testing.T) {
	bin := needSingBox(t)
	port, hits := fwdProxy(t)
	e := newEnv(t, bin)
	// 预览没有逐个任务的地址,用档案里的 testUrl
	if _, err := e.prof.Patch(context.Background(), map[string]any{"testUrl": target}); err != nil {
		t.Fatal(err)
	}
	// 没保存的订阅:内容直接给,按预览出来的节点名测
	w := e.post(t, "/api/openbox/nodes/latency", map[string]any{
		"content": fmt.Sprintf("http://u:p@127.0.0.1:%d#PV 01\n", port), "name": "X", "tags": []string{"PV 01", "没有"},
		"renameOptions": map[string]any{"enabled": false},
	})
	res := decodeResults(t, w)
	if len(res) != 2 || !res[0].OK || res[1].Reason != "not-found" {
		t.Fatalf("%+v", res)
	}
	if hits() == 0 {
		t.Fatal("应经节点转发")
	}
	// 预览不应落盘
	recs, _ := e.subs.List(context.Background())
	if len(recs) != 0 {
		t.Fatalf("预览不该保存订阅: %d", len(recs))
	}
	if w := e.post(t, "/api/openbox/nodes/latency", map[string]any{"content": "x", "tags": []string{}}); w.Code != 400 {
		t.Fatalf("tags 为空应 400: %d", w.Code)
	}
	if w := e.post(t, "/api/openbox/nodes/latency", map[string]any{"loginId": "abc", "tags": []string{"a"}}); w.Code != 501 {
		t.Fatalf("login 来源应 501: %d %s", w.Code, w.Body)
	}
}

func TestChain(t *testing.T) {
	bin := needSingBox(t)
	port, hits := fwdProxy(t)
	e := newEnv(t, bin)
	e.addSub(t, "A", fmt.Sprintf("http://u:p@127.0.0.1:%d#PX 01\n", port))

	w := e.post(t, "/api/openbox/chain-proxies/latency", map[string]any{
		"link": fmt.Sprintf("http://u:p@127.0.0.1:%d#CH", port), "upstream": "PX 01", "testUrl": target, "timeoutMs": 3000,
	})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["ok"] != true || out["via"] != "PX 01" || out["ms"] == nil {
		t.Fatalf("%v", out)
	}
	if hits() < 2 {
		t.Fatalf("链式应嵌套两层: %d", hits())
	}
	// 上游是组:via 取组里默认选中的节点
	w = e.post(t, "/api/openbox/chain-proxies/latency", map[string]any{
		"link": fmt.Sprintf("http://u:p@127.0.0.1:%d#CH", port), "upstream": "自动选择", "testUrl": target, "timeoutMs": 3000,
	})
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["via"] != "PX 01" {
		t.Fatalf("%d %v", w.Code, out)
	}
	for name, body := range map[string]map[string]any{
		"没有链接":   {"upstream": "PX 01"},
		"没有上游":   {"link": "http://u:p@127.0.0.1:1#CH"},
		"链接解析不了": {"link": "not a link", "upstream": "PX 01"},
		"上游不存在":  {"link": "http://u:p@127.0.0.1:1#CH", "upstream": "不存在的节点"},
	} {
		if w := e.post(t, "/api/openbox/chain-proxies/latency", body); w.Code != 400 {
			t.Fatalf("%s 应 400: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestNoSingBox(t *testing.T) {
	port, _ := fwdProxy(t)
	e := newEnv(t, "/nonexistent/sing-box")
	e.addSub(t, "A", fmt.Sprintf("http://u:p@127.0.0.1:%d#PX 01\n", port))
	w := e.post(t, "/api/openbox/nodes/latency", map[string]any{"jobs": []any{map[string]any{"tag": "PX 01", "url": target}}})
	if w.Code != 503 || !strings.Contains(w.Body.String(), "sing-box") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestBadRequests(t *testing.T) {
	e := newEnv(t, "sing-box")
	for _, body := range []map[string]any{{}, {"jobs": []any{}}} {
		if w := e.post(t, "/api/openbox/nodes/latency", body); w.Code != 400 {
			t.Fatalf("%v 应 400: %d", body, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/openbox/nodes/latency", nil)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	if w.Code != 405 {
		t.Fatalf("GET 应 405: %d", w.Code)
	}
}

func TestResolveVia(t *testing.T) {
	cfg := map[string]any{"outbounds": []any{
		map[string]any{"tag": "n1", "type": "trojan"},
		map[string]any{"tag": "n2", "type": "trojan"},
		map[string]any{"tag": "sel", "type": "selector", "outbounds": []any{"n1", "n2"}, "default": "n2"},
		map[string]any{"tag": "sel2", "type": "selector", "outbounds": []any{"n1", "n2"}},
		map[string]any{"tag": "auto", "type": "urltest", "outbounds": []any{"sel", "n1"}},
		map[string]any{"tag": "loop", "type": "selector", "outbounds": []any{"loop"}},
	}}
	for in, want := range map[string]string{"n1": "n1", "sel": "n2", "sel2": "n1", "auto": "n2", "不存在": "不存在"} {
		if got := ResolveVia(cfg, in); got != want {
			t.Errorf("ResolveVia(%q)=%q want %q", in, got, want)
		}
	}
	if got := ResolveVia(cfg, "loop"); got != "loop" {
		t.Errorf("成环应止步: %q", got)
	}
}

// ipProxy 是 fwdProxy 的变体:隧道里发往 443 端口的 CONNECT(出口 IP 接口)桥接到本机的 TLS 服务 tlsAddr,
// 其余照旧(CONNECT 回 200,普通请求回 204)。这样白名单里的真实主机名不用真上网也能走完整条链路。
func ipProxy(t *testing.T, tlsAddr string) (port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		_ = rw.Flush()
		br := bufio.NewReader(rw)
		for {
			in, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			if in.Method != http.MethodConnect {
				_, _ = c.Write([]byte("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n"))
				return
			}
			_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
			if _, p, _ := net.SplitHostPort(in.Host); p == "443" {
				up, err := net.Dial("tcp", tlsAddr)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, br); _ = up.Close() }()
				_, _ = io.Copy(c, up)
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// fakeIPServices 按 Host 扮演三家 IP 接口:api.ip.sb 限流 429,ipwho.is 正常,api.ipapi.is 回空。
func fakeIPServices(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.Split(r.Host, ":")[0] {
		case "api.ip.sb":
			http.Error(w, "limited", http.StatusTooManyRequests)
		case "ipwho.is":
			_, _ = w.Write([]byte(`{"ip":"203.0.113.9","country":"Testland","connection":{"org":"Test Net"}}`))
		case "api.ipapi.is":
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func TestChainIP(t *testing.T) {
	bin := needSingBox(t)
	port := ipProxy(t, fakeIPServices(t))
	e := newEnv(t, bin)
	e.addSub(t, "A", fmt.Sprintf("http://u:p@127.0.0.1:%d#PX 01\n", port))
	link := fmt.Sprintf("http://u:p@127.0.0.1:%d#CH", port)
	call := func(ipUrls any) (int, map[string]any) {
		body := map[string]any{"link": link, "upstream": "PX 01", "testUrl": target, "timeoutMs": 3000}
		if ipUrls != nil {
			body["ipUrls"] = ipUrls
		}
		w := e.post(t, "/api/openbox/chain-proxies/latency", body)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// 第一家被限流,回退到第二家;正文原样交回,测速结果不受影响
	code, out := call([]string{"https://api.ip.sb/geoip", "https://ipwho.is", "https://api.ipapi.is"})
	ip, _ := out["ip"].(map[string]any)
	if code != 200 || out["ok"] != true || out["via"] != "PX 01" || ip == nil || ip["ok"] != true || ip["url"] != "https://ipwho.is" {
		t.Fatalf("%d %v", code, out)
	}
	var data struct{ IP, Country string }
	if err := json.Unmarshal([]byte(ip["body"].(string)), &data); err != nil || data.IP != "203.0.113.9" || data.Country != "Testland" {
		t.Fatalf("正文应原样交回: %v", ip)
	}

	// 全部失败:ip.ok=false 并逐家列原因,不带 url / body;测速本身照常成功
	code, out = call([]string{"https://api.ip.sb/geoip", "https://api.ipapi.is"})
	ip, _ = out["ip"].(map[string]any)
	if code != 200 || out["ok"] != true || ip == nil || ip["ok"] != false || ip["url"] != nil || ip["body"] != nil ||
		ip["error"] != "api.ip.sb: HTTP 429;api.ipapi.is: 响应是空的" {
		t.Fatalf("%d %v", code, out)
	}

	// 没带 / 带了空数组:没有 ip 字段
	for _, v := range []any{nil, []string{}} {
		if code, out = call(v); code != 200 || out["ok"] != true || out["ip"] != nil {
			t.Fatalf("%v → %d %v", v, code, out)
		}
	}

	// 不在白名单里的地址一律 400,文案带「ipUrls」(前端靠它降级重测),而且一个请求都不会发出去
	for _, bad := range [][]string{
		{"http://ipwho.is"}, {"https://example.com/ip"}, {"https://u:p@ipwho.is"}, {"https://ipwho.is:8443"},
		{""}, {"ipwho.is"}, {"https://ipwho.is", "https://evil.test"}, {"https://ipwho.is.evil.test"},
	} {
		code, out = call(bad)
		if msg, _ := out["error"].(string); code != 400 || !strings.Contains(msg, "ipUrls") {
			t.Fatalf("%v 应 400 且带 ipUrls: %d %v", bad, code, out)
		}
	}
}

// 线路本身不通(上游节点的端口是关的):测速失败,IP 这一项也如实失败,不会因此让整个请求报错。
func TestChainIPLineDown(t *testing.T) {
	bin := needSingBox(t)
	port := ipProxy(t, fakeIPServices(t))
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().(*net.TCPAddr).Port
	l.Close()
	e := newEnv(t, bin)
	e.addSub(t, "A", fmt.Sprintf("http://u:p@127.0.0.1:%d#PX 01\n", dead))
	w := e.post(t, "/api/openbox/chain-proxies/latency", map[string]any{
		"link": fmt.Sprintf("http://u:p@127.0.0.1:%d#CH", port), "upstream": "PX 01", "testUrl": target, "timeoutMs": 2000,
		"ipUrls": []string{"https://ipwho.is"},
	})
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	ip, _ := out["ip"].(map[string]any)
	if w.Code != 200 || out["ok"] != false || ip == nil || ip["ok"] != false || ip["error"] == "" {
		t.Fatalf("%d %v", w.Code, out)
	}
}

func TestNormalizeIPURLs(t *testing.T) {
	got, err := normalizeIPURLs([]string{" https://API.ip.sb/geoip ", "https://api.ip.sb/geoip", "https://ipwho.is#x", "https://api.ipapi.is:443"})
	want := []string{"https://api.ip.sb/geoip", "https://ipwho.is", "https://api.ipapi.is"}
	if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
		// 主机名转小写、去掉默认端口 443、去掉片段,再按完整地址去重,保持原顺序
		t.Fatalf("%v %v", got, err)
	}
	var many []string
	for i := 0; i < 6; i++ {
		many = append(many, fmt.Sprintf("https://ipwho.is/%d", i))
	}
	if _, err := normalizeIPURLs(many); err == nil || !strings.Contains(err.Error(), "ipUrls") {
		t.Fatalf("超过 5 个应拒绝: %v", err)
	}
	if got, err := normalizeIPURLs(nil); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}
