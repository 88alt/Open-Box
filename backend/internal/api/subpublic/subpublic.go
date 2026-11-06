// Package subpublic 实现公开的 GET /sub/<token>(订阅分享下发)。
package subpublic

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subshare"
)

// Deps.
type Deps struct {
	Public *subshare.Public
}

// Register 挂到 mux;路径在 /api/ 之外。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/sub/", d.serve)
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (d Deps) serve(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if d.Public == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "not available")
		return
	}
	tok := strings.TrimPrefix(r.URL.Path, "/sub/")
	tok = strings.Trim(tok, "/")
	if tok == "" || strings.Contains(tok, "/") {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	format := subshare.DetectFormat(r.URL.Query().Get("format"), r.Header.Get("User-Agent"))
	res, err := d.Public.Serve(r.Context(), tok, format, peerIP(r))
	if err != nil {
		switch {
		case errors.Is(err, subshare.ErrPublicLocked):
			httpx.Error(w, http.StatusTooManyRequests, "locked")
		default:
			httpx.Error(w, http.StatusNotFound, "not found")
		}
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", res.ContentType)
		if res.ETag != "" {
			w.Header().Set("ETag", res.ETag)
		}
		if res.Userinfo != "" {
			w.Header().Set("Subscription-Userinfo", res.Userinfo)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	subshare.WriteHTTP(w, r, res)
}

// NodesAdapter 方便把 NodesFor 接上。
func NodesAdapter(fn func(ctx context.Context, ids []string) ([]sub.Node, *sub.Usage, error)) subshare.NodesFor {
	return fn
}
