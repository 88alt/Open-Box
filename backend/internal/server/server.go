// Package server 负责 HTTP 服务本体:路由表、中间件(恢复 panic、访问日志)、优雅退出。
// 各接口分组由 internal/api/<分组> 的 Register 往 Mux() 上挂载,server 自己不依赖它们。
package server

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Middleware 包装一个 http.Handler。
type Middleware func(http.Handler) http.Handler

// Options 是构造 Server 的参数。
type Options struct {
	Logger  *slog.Logger
	Version string
	// Middleware 在恢复 panic 和访问日志「里面」按顺序生效(第一个在最外层),例如鉴权。
	Middleware []Middleware
}

// Server 持有路由表和最终的 Handler。
type Server struct {
	log     *slog.Logger
	mux     *http.ServeMux
	handler http.Handler
}

// New 创建 Server 并注册基础路由(健康检查、/api 下未知路径的 JSON 404)。
func New(opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{log: log, mux: http.NewServeMux()}

	s.mux.HandleFunc("/api/health", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, _ *http.Request) {
			httpx.NoStore(w)
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "version": opts.Version})
		},
	}))
	s.mux.HandleFunc("/api", httpx.NotFound)
	s.mux.HandleFunc("/api/", httpx.NotFound)

	var h http.Handler = s.mux
	for i := len(opts.Middleware) - 1; i >= 0; i-- {
		h = opts.Middleware[i](h)
	}
	s.handler = recoverer(log)(accessLog(log)(h))
	return s
}

// Mux 返回路由表,接口分组往上面注册。必须在开始服务之前注册完。
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Handler 返回带中间件的完整 Handler(测试用)。
func (s *Server) Handler() http.Handler { return s.handler }

// Serve 在 ln 上提供服务,ctx 取消时优雅退出(最多等 10 秒)。正常退出返回 nil。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler: s.handler,
		// 不设 WriteTimeout / ReadTimeout:之后有流式响应和 WebSocket 转发。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	s.log.Info("正在退出,等待进行中的请求完成")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return err
	}
	return nil
}

// statusRecorder 记录状态码,同时保留 Flusher / Hijacker 能力(流式响应、WebSocket 要用)。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status, r.wrote = http.StatusOK, true
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("底层连接不支持 Hijack")
	}
	if !r.wrote {
		r.status, r.wrote = http.StatusSwitchingProtocols, true
	}
	return h.Hijack()
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func accessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rec, r)
			level := slog.LevelDebug
			if rec.status >= 500 {
				level = slog.LevelWarn
			}
			// 只记路径,不记 query:query 里可能带订阅地址、token。
			log.Log(r.Context(), level, "请求", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "ms", time.Since(start).Milliseconds())
		})
	}
}

func recoverer(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler { //nolint:errorlint // 标准库约定直接比较
					panic(v)
				}
				log.Error("处理请求时 panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				httpx.Error(w, http.StatusInternalServerError, "服务内部错误")
			}()
			next.ServeHTTP(w, r)
		})
	}
}
