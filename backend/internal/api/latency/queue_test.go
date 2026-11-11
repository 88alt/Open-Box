package latency

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/controllerproxy"
)

// queueGet 用给定依赖挂上路由,发一次 GET,返回状态码和解出的响应。
func queueGet(t *testing.T, d Deps, method string) (int, QueueRes, string) {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/nodes/latency/queue", nil))
	var res QueueRes
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("响应不是 JSON: %v: %s", err, rec.Body.String())
		}
	}
	return rec.Code, res, rec.Body.String()
}

// fakeKernel 起一个假内核,body 是 /openbox/probes 的回应;返回它的 Resolver 和取已收到请求的函数。
func fakeKernel(t *testing.T, status int, body string) (controllerproxy.Resolver, func() []*http.Request) {
	t.Helper()
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(r.Context()))
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	get := func() []*http.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]*http.Request(nil), seen...)
	}
	return func() (controllerproxy.Target, error) {
		return controllerproxy.Target{Addr: addr, Secret: "s3cret"}, nil
	}, get
}

func TestQueue(t *testing.T) {
	cases := []struct {
		name string
		body string
		want QueueRes
	}{
		{"有人排队", `{"running":1,"interactive":2,"background":1,"limit":4}`, QueueRes{Busy: true, Running: 1, Waiting: 3}},
		{"只有后台在排", `{"running":0,"interactive":0,"background":1,"limit":4}`, QueueRes{Busy: true, Running: 0, Waiting: 1}},
		{"没人排但跑满了", `{"running":4,"interactive":0,"background":0,"limit":4}`, QueueRes{Busy: true, Running: 4}},
		{"没人排、没跑满", `{"running":3,"interactive":0,"background":0,"limit":4}`, QueueRes{Running: 3}},
		{"limit 为 0 不算跑满", `{"running":5,"interactive":0,"background":0,"limit":0}`, QueueRes{Running: 5}},
		{"缺 limit 不算跑满", `{"running":5}`, QueueRes{Running: 5}},
		{"空对象当不忙", `{}`, QueueRes{}},
		{"数字写成字符串也认", `{"running":"2","interactive":"1","limit":"2"}`, QueueRes{Busy: true, Running: 2, Waiting: 1}},
		{"读不出的值记 0", `{"running":"abc","interactive":null,"background":[1],"limit":{}}`, QueueRes{}},
		{"JSON null 当没有内核", `null`, QueueRes{}},
		{"不是 JSON", `<html>`, QueueRes{}},
		{"JSON 数组", `[1,2]`, QueueRes{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kernel, _ := fakeKernel(t, 200, c.body)
			code, got, raw := queueGet(t, Deps{Kernel: kernel}, http.MethodGet)
			if code != http.StatusOK {
				t.Fatalf("状态码 %d: %s", code, raw)
			}
			if got != c.want {
				t.Fatalf("得到 %+v,想要 %+v", got, c.want)
			}
		})
	}
}

func TestQueueRequestToKernel(t *testing.T) {
	kernel, seen := fakeKernel(t, 200, `{}`)
	if code, _, raw := queueGet(t, Deps{Kernel: kernel}, http.MethodGet); code != 200 {
		t.Fatalf("状态码 %d: %s", code, raw)
	}
	reqs := seen()
	if len(reqs) != 1 {
		t.Fatalf("内核收到 %d 次请求,想要 1 次", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodGet || r.URL.Path != "/openbox/probes" {
		t.Fatalf("请求不对: %s %s", r.Method, r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestQueueNoSecretSendsNoAuth(t *testing.T) {
	kernel, seen := fakeKernel(t, 200, `{}`)
	noSecret := func() (controllerproxy.Target, error) {
		t, err := kernel()
		t.Secret = ""
		return t, err
	}
	queueGet(t, Deps{Kernel: noSecret}, http.MethodGet)
	reqs := seen()
	if len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "" {
		t.Fatalf("没有 secret 时不该带 Authorization: %+v", reqs)
	}
}

func TestQueueNotBusyWhenKernelUnavailable(t *testing.T) {
	t.Run("没接内核解析", func(t *testing.T) {
		if code, got, _ := queueGet(t, Deps{}, http.MethodGet); code != 200 || got != (QueueRes{}) {
			t.Fatalf("code=%d got=%+v", code, got)
		}
	})
	t.Run("读不到控制接口", func(t *testing.T) {
		d := Deps{Kernel: func() (controllerproxy.Target, error) { return controllerproxy.Target{}, errors.New("没部署过") }}
		if code, got, _ := queueGet(t, d, http.MethodGet); code != 200 || got != (QueueRes{}) {
			t.Fatalf("code=%d got=%+v", code, got)
		}
	})
	t.Run("地址为空", func(t *testing.T) {
		d := Deps{Kernel: func() (controllerproxy.Target, error) { return controllerproxy.Target{}, nil }}
		if code, got, _ := queueGet(t, d, http.MethodGet); code != 200 || got != (QueueRes{}) {
			t.Fatalf("code=%d got=%+v", code, got)
		}
	})
	t.Run("老内核没有这个接口(404)", func(t *testing.T) {
		kernel, _ := fakeKernel(t, 404, `{"running":9,"interactive":9,"limit":1}`)
		if code, got, _ := queueGet(t, Deps{Kernel: kernel}, http.MethodGet); code != 200 || got != (QueueRes{}) {
			t.Fatalf("code=%d got=%+v", code, got)
		}
	})
	t.Run("内核 500", func(t *testing.T) {
		kernel, _ := fakeKernel(t, 500, `{"running":9,"interactive":9}`)
		if code, got, _ := queueGet(t, Deps{Kernel: kernel}, http.MethodGet); code != 200 || got != (QueueRes{}) {
			t.Fatalf("code=%d got=%+v", code, got)
		}
	})
	t.Run("内核没在监听", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		addr := strings.TrimPrefix(srv.URL, "http://")
		srv.Close()
		d := Deps{Kernel: func() (controllerproxy.Target, error) { return controllerproxy.Target{Addr: addr}, nil }}
		if code, got, _ := queueGet(t, d, http.MethodGet); code != 200 || got != (QueueRes{}) {
			t.Fatalf("code=%d got=%+v", code, got)
		}
	})
}

func TestQueueTimeoutIsNotBusy(t *testing.T) {
	old := kernelProbesTimeout
	kernelProbesTimeout = 80 * time.Millisecond
	defer func() { kernelProbesTimeout = old }()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"running":9,"interactive":9,"limit":1}`))
	}))
	defer srv.Close()
	defer close(release)
	addr := strings.TrimPrefix(srv.URL, "http://")
	d := Deps{Kernel: func() (controllerproxy.Target, error) { return controllerproxy.Target{Addr: addr}, nil }}

	start := time.Now()
	code, got, _ := queueGet(t, d, http.MethodGet)
	if code != 200 || got != (QueueRes{}) {
		t.Fatalf("超时应当不忙: code=%d got=%+v", code, got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("超时没生效,等了 %v", time.Since(start))
	}
}

func TestQueueMethods(t *testing.T) {
	kernel, _ := fakeKernel(t, 200, `{}`)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if code, _, _ := queueGet(t, Deps{Kernel: kernel}, m); code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应 405,得到 %d", m, code)
		}
	}
	if code, _, _ := queueGet(t, Deps{Kernel: kernel}, http.MethodHead); code != http.StatusOK {
		t.Fatalf("HEAD 应兼容 GET,得到 %d", code)
	}
}

func TestQueueNoStore(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Deps{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/nodes/latency/queue", nil))
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q", cc)
	}
}
