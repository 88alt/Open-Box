package traffic

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/traffic"
)

func newReal(t *testing.T) *traffic.Store {
	t.Helper()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	s, err := traffic.Open(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	err = s.Record(now.Add(-2*time.Hour), traffic.Delta{Up: 15, Down: 110, Conns: 2, DirectTags: []string{"直连"}, Items: []traffic.Item{
		{Client: "192.168.1.5", Node: "x", Host: "a.com", Up: 10, Down: 100, Conns: 1},
		{Client: "192.168.1.6", Node: "直连", Host: "c.com", Up: 5, Down: 10, Conns: 1, Direct: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(d Deps, method, target string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestEndpoints(t *testing.T) {
	d := Deps{Source: newReal(t), Names: func() map[string]string { return map[string]string{"192.168.1.5": "我的手机"} }}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	m := decode(get(d, "GET", "/api/openbox/traffic/month"))
	if m["month"] != "2026-10" || m["today"] != "2026-10-28" || m["avgDays"] != float64(1) || len(m["days"].([]any)) != 1 {
		t.Errorf("month: %v", m)
	}
	if tot := m["total"].(map[string]any); tot["up"] != float64(15) {
		t.Errorf("month total: %v", tot)
	}
	m = decode(get(d, "GET", "/api/openbox/traffic/month?month=2026-10&direct=0"))
	if tot := m["total"].(map[string]any); tot["up"] != float64(10) || m["direct"].(map[string]any)["excluded"] != true {
		t.Errorf("month direct=0: %v", m)
	}

	m = decode(get(d, "GET", "/api/openbox/traffic/day?day=2026-10-28&limit=500&hour=10"))
	clients := m["clients"].([]any)
	first := clients[0].(map[string]any)
	if first["key"] != "192.168.1.5" || first["name"] != "我的手机" || m["hour"] != float64(10) {
		t.Errorf("day: %v", m)
	}
	if second := clients[1].(map[string]any); second["name"] != nil { // 租约里没有的不带 name
		t.Errorf("没有主机名的不该带 name: %v", second)
	}
	for _, k := range []string{"nodes", "hosts", "other", "hours", "nowHour", "hostsCount", "clientsCount", "hourDetailKeepDays", "direct"} {
		if _, ok := m[k]; !ok {
			t.Errorf("day 缺字段 %s", k)
		}
	}
	m = decode(get(d, "GET", "/api/openbox/traffic/day?day=2026-10-28"))
	if m["hour"] != nil {
		t.Errorf("整天 hour 应为 null: %v", m["hour"])
	}

	m = decode(get(d, "GET", "/api/openbox/traffic/drill?day=2026-10-28&kind=host&key=a.com&by=client"))
	rows := m["rows"].([]any)
	if m["count"] != float64(1) || rows[0].(map[string]any)["name"] != "我的手机" || m["sum"].(map[string]any)["up"] != float64(10) {
		t.Errorf("drill: %v", m)
	}
	m = decode(get(d, "GET", "/api/openbox/traffic/drill?day=2026-10-28&kind=client&key=192.168.1.5&by=host"))
	if rows := m["rows"].([]any); rows[0].(map[string]any)["name"] != nil { // 不是按终端拆,不补名字
		t.Errorf("按访问目标拆不该带 name: %v", rows[0])
	}

	m = decode(get(d, "GET", "/api/openbox/traffic/usage"))
	if m["days"] != float64(1) || m["hourKeepDays"] != float64(traffic.HourKeepDays) || m["newestDay"] != "2026-10-28" {
		t.Errorf("usage: %v", m)
	}
}

func TestBadRequests(t *testing.T) {
	d := Deps{Source: newReal(t)}
	for _, u := range []string{
		"/api/openbox/traffic/month?month=2026-13",
		"/api/openbox/traffic/day",
		"/api/openbox/traffic/day?day=abc",
		"/api/openbox/traffic/day?day=2026-10-28&limit=x",
		"/api/openbox/traffic/day?day=2026-10-28&limit=99999",
		"/api/openbox/traffic/day?day=2026-10-28&hour=x",
		"/api/openbox/traffic/day?day=2026-10-28&hour=24",
		"/api/openbox/traffic/drill?day=2026-10-28&kind=host&key=a.com&by=host",
		"/api/openbox/traffic/drill?day=2026-10-28&kind=host&by=client",
		"/api/openbox/traffic/drill?kind=host&key=a.com&by=client",
	} {
		rec := get(d, "GET", u)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s", u, rec.Code, rec.Body.String())
		}
	}
}

type failing struct{ Source }

func (failing) Month(string, bool) (traffic.MonthResult, error) {
	return traffic.MonthResult{}, errors.New("磁盘坏了")
}

func TestOtherCases(t *testing.T) {
	if rec := get(Deps{Source: failing{}}, "GET", "/api/openbox/traffic/month"); rec.Code != 500 || !strings.Contains(rec.Body.String(), "磁盘坏了") {
		t.Errorf("内部错误: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(Deps{}, "GET", "/api/openbox/traffic/usage"); rec.Code != 503 {
		t.Errorf("没接库: %d", rec.Code)
	}
	for _, p := range []string{"month", "day", "drill", "usage"} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if rec := get(Deps{Source: newReal(t)}, m, "/api/openbox/traffic/"+p); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d", m, p, rec.Code)
			}
		}
	}
}
