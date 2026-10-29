package controllerproxy

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type seen struct {
	Method, EscapedPath, RawQuery string
	Auth, Cookie, Origin, Host    string
}

// fakeKernel 是假的内核控制接口:secret 对不上就 401。
type fakeKernel struct {
	*httptest.Server
	mu   sync.Mutex
	last seen
}

func newKernel(t *testing.T, secret string) *fakeKernel {
	t.Helper()
	k := &fakeKernel{}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.last = seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Header.Get("Origin"), r.Host}
		k.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+secret && r.URL.Query().Get("token") != secret {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		switch {
		case r.URL.Path == "/stream":
			w.Header().Set("Content-Type", "application/json")
			fl := w.(http.Flusher)
			for i := 0; i < 3; i++ {
				_, _ = w.Write([]byte(`{"up":1}` + "\n"))
				fl.Flush()
				time.Sleep(150 * time.Millisecond)
			}
		case r.Header.Get("Upgrade") == "websocket":
			wsEcho(t, w, r)
		default:
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "body": string(body), "method": r.Method})
		}
	}))
	t.Cleanup(k.Close)
	return k
}

func (k *fakeKernel) seen() seen { k.mu.Lock(); defer k.mu.Unlock(); return k.last }

// wsEcho 是最小的 WebSocket 服务端:握手后先发一帧 "hello",再把收到的(带掩码的)文本帧以 "echo:" 开头回一帧。
func wsEcho(t *testing.T, w http.ResponseWriter, r *http.Request) {
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("假内核劫持失败: %v", err)
		return
	}
	defer conn.Close()
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	_, _ = rw.Write(frame("hello"))
	_ = rw.Flush()
	hdr := make([]byte, 6) // 客户端帧:2 字节头 + 4 字节掩码(负载短于 126)
	if _, err := io.ReadFull(rw, hdr); err != nil {
		return
	}
	pl := make([]byte, hdr[1]&0x7f)
	if _, err := io.ReadFull(rw, pl); err != nil {
		return
	}
	for i := range pl {
		pl[i] ^= hdr[2+i%4]
	}
	_, _ = rw.Write(frame("echo:" + string(pl)))
	_ = rw.Flush()
}

func frame(s string) []byte { return append([]byte{0x81, byte(len(s))}, s...) }

func writeConfig(t *testing.T, addr, secret string) string {
	t.Helper()
	cfg := map[string]any{"experimental": map[string]any{"clash_api": map[string]any{"external_controller": addr, "secret": secret}}}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newProxy(t *testing.T, resolve Resolver) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	h := New(resolve)
	mux.Handle(PrefixREST+"/", h.REST())
	mux.Handle(PrefixREST, h.REST())
	mux.Handle(PrefixWS+"/", h.WS())
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func fixed(addr, secret string) Resolver {
	return func() (Target, error) { return Target{Addr: addr, Secret: secret}, nil }
}

func TestRESTInjectsSecretAndStripsBrowserCredentials(t *testing.T) {
	k := newKernel(t, "s3")
	p := newProxy(t, fixed(strings.TrimPrefix(k.URL, "http://"), "s3"))
	req, _ := http.NewRequest(http.MethodPut, p.URL+"/api/controller/proxies/Group?x=1&token=evil", strings.NewReader(`{"name":"HK"}`))
	req.Header.Set("Cookie", "openbox_session=PANELSECRET")
	req.Header.Set("Authorization", "Bearer panel-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"path":"/proxies/Group"`) || !strings.Contains(string(body), `"method":"PUT"`) || !strings.Contains(string(body), `{\"name\":\"HK\"}`) {
		t.Fatalf("方法、路径和请求体应原样转发: %d %s", resp.StatusCode, body)
	}
	s := k.seen()
	if s.Auth != "Bearer s3" || s.Cookie != "" || strings.Contains(s.RawQuery, "evil") || s.RawQuery != "x=1" {
		t.Errorf("应只带内核的 secret,不带面板的 Cookie / Authorization / 自带的 token: %+v", s)
	}
	if s.Host != strings.TrimPrefix(k.URL, "http://") {
		t.Errorf("Host 应是内核的地址: %q", s.Host)
	}
}

func TestRESTKeepsEncodedPathAndRoot(t *testing.T) {
	k := newKernel(t, "")
	p := newProxy(t, fixed(strings.TrimPrefix(k.URL, "http://"), ""))
	// 节点名里的空格、斜杠、中文都按编码转过去(前端用 encodeURIComponent)
	enc := "/api/controller/proxies/HK%2001%2F%E5%A4%87%E7%94%A8/delay?timeout=5000&url=http%3A%2F%2Fa.com"
	resp, err := http.Get(p.URL + enc)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if s := k.seen(); s.EscapedPath != "/proxies/HK%2001%2F%E5%A4%87%E7%94%A8/delay" || s.RawQuery != "timeout=5000&url=http%3A%2F%2Fa.com" {
		t.Errorf("编码要原样保持: %+v", k.seen())
	}
	for _, u := range []string{"/api/controller", "/api/controller/"} {
		if resp, err := http.Get(p.URL + u); err != nil || resp.StatusCode != 200 || k.seen().EscapedPath != "/" {
			t.Errorf("%s 应转到内核的 /: %v %+v", u, err, k.seen())
		}
	}
	if resp, _ := http.Get(p.URL + "/api/controllerx"); resp.StatusCode != 404 {
		t.Errorf("前缀要按路径段匹配: %d", resp.StatusCode)
	}
	// 没设 secret 时不加 Authorization
	if k.seen().Auth != "" {
		t.Errorf("没设 secret 不该加认证头: %q", k.seen().Auth)
	}
}

func TestRESTStreamsResponses(t *testing.T) {
	k := newKernel(t, "s3")
	p := newProxy(t, fixed(strings.TrimPrefix(k.URL, "http://"), "s3"))
	resp, err := http.Get(p.URL + "/api/controller/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, `"up":1`) {
		t.Fatalf("%q %v", line, err)
	}
	if d := time.Since(start); d > 300*time.Millisecond { // 全部 3 块要 450ms 才发完,第一块应当马上到
		t.Errorf("流式响应应边到边转,第一块等了 %v", d)
	}
}

func TestUnavailableKernelIs502JSON(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(gone.URL, "http://")
	gone.Close()
	for name, res := range map[string]Resolver{
		"连不上": fixed(addr, ""),
		"没配置": func() (Target, error) { return Target{}, os.ErrNotExist },
	} {
		p := newProxy(t, res)
		resp, err := http.Get(p.URL + "/api/controller/proxies")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&m)
		resp.Body.Close()
		if resp.StatusCode != 502 || m["message"] == "" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("%s: 应 502 + JSON message: %d %v", name, resp.StatusCode, m)
		}
	}
}

func TestInvalidPathRejected(t *testing.T) {
	called := false
	p := newProxy(t, func() (Target, error) { called = true; return Target{}, nil })
	// 用原始连接发,免得客户端先把 .. 规整掉
	conn, err := net.Dial("tcp", strings.TrimPrefix(p.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET /api/controller/%2e%2e/%2e%2e/etc HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == 200 || called {
		t.Errorf("含 .. 的路径不该转发: %d called=%v", resp.StatusCode, called)
	}
}

func dialWS(t *testing.T, base, path string, extra map[string]string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	host := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"
	for k, v := range extra {
		req += k + ": " + v + "\r\n"
	}
	_, _ = conn.Write([]byte(req + "\r\n"))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn, br, resp
}

func readFrame(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		t.Fatal(err)
	}
	pl := make([]byte, hdr[1]&0x7f)
	if _, err := io.ReadFull(br, pl); err != nil {
		t.Fatal(err)
	}
	return string(pl)
}

func TestWebSocketRelaysBothWays(t *testing.T) {
	k := newKernel(t, "s3")
	p := newProxy(t, fixed(strings.TrimPrefix(k.URL, "http://"), "s3"))
	host := strings.TrimPrefix(p.URL, "http://")
	conn, br, resp := dialWS(t, p.URL, "/api/controller-ws/traffic?token=evil&level=info", map[string]string{
		"Origin": "http://" + host, "Cookie": "openbox_session=PANELSECRET",
	})
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("应升级成功并带正确的 Accept: %d %v", resp.StatusCode, resp.Header)
	}
	if got := readFrame(t, br); got != "hello" {
		t.Errorf("内核 → 浏览器: %q", got)
	}
	mask := []byte{1, 2, 3, 4}
	msg := []byte("ping")
	out := append([]byte{0x81, 0x80 | byte(len(msg))}, mask...)
	for i, b := range msg {
		out = append(out, b^mask[i%4])
	}
	_, _ = conn.Write(out)
	if got := readFrame(t, br); got != "echo:ping" {
		t.Errorf("浏览器 → 内核 → 浏览器: %q", got)
	}
	s := k.seen()
	if s.Auth != "Bearer s3" || s.Cookie != "" || !strings.Contains(s.RawQuery, "token=s3") || strings.Contains(s.RawQuery, "evil") || !strings.Contains(s.RawQuery, "level=info") {
		t.Errorf("握手应带内核的 secret(头和 token 参数),不带面板的 Cookie 和自带的 token: %+v", s)
	}
	if s.EscapedPath != "/traffic" {
		t.Errorf("路径: %q", s.EscapedPath)
	}
}

func TestWebSocketRejectsCrossSiteOrigin(t *testing.T) {
	k := newKernel(t, "s3")
	p := newProxy(t, fixed(strings.TrimPrefix(k.URL, "http://"), "s3"))
	_, _, resp := dialWS(t, p.URL, "/api/controller-ws/traffic", map[string]string{"Origin": "http://evil.example.com"})
	if resp.StatusCode != 403 {
		t.Errorf("跨站的 Origin 应 403: %d", resp.StatusCode)
	}
	if k.seen().Method != "" {
		t.Error("被拒绝的握手不该到达内核")
	}
	// 没有 Origin(非浏览器客户端)放行
	if _, _, resp := dialWS(t, p.URL, "/api/controller-ws/traffic", nil); resp.StatusCode != 101 {
		t.Errorf("没有 Origin 应放行: %d", resp.StatusCode)
	}
}

func TestFileResolverCachesAndPicksUpChanges(t *testing.T) {
	p := writeConfig(t, "127.0.0.1:9090", "a")
	r := FileResolver(p)
	for i := 0; i < 2; i++ {
		if tg, err := r(); err != nil || tg.Addr != "127.0.0.1:9090" || tg.Secret != "a" {
			t.Fatalf("%+v %v", tg, err)
		}
	}
	b, _ := json.Marshal(map[string]any{"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:19090", "secret": "bbbbbbbb"}}})
	if err := os.WriteFile(p, b, 0o600); err != nil { // 大小变了,即使修改时间相同也要重读
		t.Fatal(err)
	}
	if tg, err := r(); err != nil || tg.Addr != "127.0.0.1:19090" || tg.Secret != "bbbbbbbb" {
		t.Fatalf("配置变了应读到新的: %+v %v", tg, err)
	}
	_ = os.WriteFile(p, []byte(`{}`), 0o600)
	if _, err := r(); err == nil || !strings.Contains(err.Error(), "控制接口") {
		t.Errorf("没开控制接口应报错: %v", err)
	}
	if _, err := FileResolver(filepath.Join(t.TempDir(), "nope.json"))(); err == nil {
		t.Error("没有配置文件应报错")
	}
}
