package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestHealthAndNotFound(t *testing.T) {
	s := New(Options{Logger: quietLogger(), Version: "t1"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"t1"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	for _, p := range []string{"/api", "/api/", "/api/nope/deeper"} {
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/health", nil))
	if rec.Code != 405 {
		t.Fatalf("POST /api/health 应为 405, 得到 %d", rec.Code)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	s := New(Options{Logger: quietLogger()})
	s.Mux().HandleFunc("/api/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/boom", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "服务内部错误") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestMiddlewareOrder(t *testing.T) {
	var order []string
	mk := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	s := New(Options{Logger: quietLogger(), Middleware: []Middleware{mk("a"), mk("b")}})
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/health", nil))
	if strings.Join(order, ",") != "a,b" {
		t.Fatalf("顺序 %v", order)
	}
}

func TestServeAndGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("无法监听本地端口:", err)
	}
	s := New(Options{Logger: quietLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("优雅退出应返回 nil: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("退出超时")
	}
}
