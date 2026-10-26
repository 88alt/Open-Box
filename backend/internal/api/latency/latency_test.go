package latency

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	Register(e.mux, Deps{Gen: gen, Subs: e.subs, Profiles: e.prof, Runner: &probe.Runner{Bin: singbox_, Dir: t.TempDir()}})
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
	sc := bufio.NewScanner(w.Body)
	for sc.Scan() {
		var msg struct {
			Done    []int    `json:"done"`
			Results []result `json:"results"`
			Error   string   `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			t.Fatalf("不是 JSON 行: %q", sc.Text())
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
