// Package bandwidth 实现 /api/openbox/bandwidth*。
package bandwidth

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/bandwidth"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Deps.
type Deps struct {
	Service *bandwidth.Service
}

// Register.
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/bandwidth", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.status,
	}))
	mux.HandleFunc("/api/openbox/bandwidth/subscriptions/{id}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.queueSub,
	}))
	mux.HandleFunc("/api/openbox/bandwidth/nodes/{name}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.queueNode,
	}))
}

func (d Deps) status(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Service == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "带宽服务不可用")
		return
	}
	st, err := d.Service.Status(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (d Deps) queueSub(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Service == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "带宽服务不可用")
		return
	}
	out, err := d.Service.QueueSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (d Deps) queueNode(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Service == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "带宽服务不可用")
		return
	}
	out, err := d.Service.QueueNode(r.Context(), r.PathValue("name"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
