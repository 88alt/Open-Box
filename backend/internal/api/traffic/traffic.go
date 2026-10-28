// Package traffic 实现 /api/openbox/traffic/*(每日流量,只读):month、day、drill、usage。
// 数据由 internal/traffic 的采集器记,这里只解析参数、调用、补终端名。
package traffic

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/traffic"
)

// Source 是流量库的读接口(*traffic.Store 实现它)。
type Source interface {
	Month(month string, countDirect bool) (traffic.MonthResult, error)
	DayView(day string, limit int, hour *int, countDirect bool) (traffic.DayResult, error)
	Drill(day string, hour *int, kind, key, by string, limit int, countDirect bool) (traffic.DrillResult, error)
	Usage() traffic.UsageResult
}

// Deps 是接口的依赖。Names 返回「终端 IP → 主机名」(DHCP 租约),为空或没有就不带 name。
type Deps struct {
	Source Source
	Names  func() map[string]string
}

// Register 挂载 /api/openbox/traffic/*。
func Register(mux *http.ServeMux, d Deps) {
	get := func(h http.HandlerFunc) http.HandlerFunc {
		return httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			httpx.NoStore(w)
			if d.Source == nil {
				httpx.Error(w, http.StatusServiceUnavailable, "流量统计不可用")
				return
			}
			h(w, r)
		}})
	}
	mux.HandleFunc("/api/openbox/traffic/usage", get(func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, d.Source.Usage())
	}))
	mux.HandleFunc("/api/openbox/traffic/month", get(func(w http.ResponseWriter, r *http.Request) {
		res, err := d.Source.Month(r.URL.Query().Get("month"), countDirect(r))
		reply(w, res, err)
	}))
	mux.HandleFunc("/api/openbox/traffic/day", get(func(w http.ResponseWriter, r *http.Request) {
		day, limit, hour, ok := dayParams(w, r)
		if !ok {
			return
		}
		res, err := d.Source.DayView(day, limit, hour, countDirect(r))
		if err == nil {
			d.name(res.Clients)
		}
		reply(w, res, err)
	}))
	mux.HandleFunc("/api/openbox/traffic/drill", get(func(w http.ResponseWriter, r *http.Request) {
		day, limit, hour, ok := dayParams(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		res, err := d.Source.Drill(day, hour, q.Get("kind"), q.Get("key"), q.Get("by"), limit, countDirect(r))
		if err == nil && res.By == "client" {
			d.name(res.Rows)
		}
		reply(w, res, err)
	}))
}

// countDirect:direct=0 表示不统计直连,其余(含不带)都统计。
func countDirect(r *http.Request) bool { return r.URL.Query().Get("direct") != "0" }

// dayParams 解析 day(必填)、limit(缺省交给库用 500)、hour(可选 0 到 23)。
func dayParams(w http.ResponseWriter, r *http.Request) (day string, limit int, hour *int, ok bool) {
	q := r.URL.Query()
	day = q.Get("day")
	if day == "" {
		httpx.Error(w, http.StatusBadRequest, "缺少 day(YYYY-MM-DD)")
		return
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "limit 要是整数")
			return
		}
		limit = n
	}
	if s := q.Get("hour"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "hour 要是 0 到 23 的整数")
			return
		}
		hour = &n
	}
	return day, limit, hour, true
}

func (d Deps) name(rows []traffic.Row) {
	if d.Names == nil || len(rows) == 0 {
		return
	}
	names := d.Names()
	for i := range rows {
		rows[i].Name = names[rows[i].Key]
	}
}

func reply(w http.ResponseWriter, v any, err error) {
	var ie *traffic.InputError
	switch {
	case errors.As(err, &ie):
		httpx.Error(w, http.StatusBadRequest, ie.Msg)
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "读取流量统计失败: "+err.Error())
	default:
		httpx.JSON(w, http.StatusOK, v)
	}
}
