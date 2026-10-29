package dnsfilter

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsqlog"
)

var logNow = time.Date(2026, 10, 28, 10, 15, 0, 0, time.UTC)

func sampleLog() *dnsqlog.Log {
	l := dnsqlog.New()
	l.Classify = func(d string) (bool, string) { return d == "ads.example.com", "AdList" }
	l.SetConnected(true)
	feed := func(at time.Time, msg string) { l.Feed("Oct 28 10:15:00 dnsmasq[1]: "+msg, at) }
	feed(logNow, "query[A] ads.example.com from 192.168.1.5")
	feed(logNow.Add(10*time.Millisecond), "reply ads.example.com is NXDOMAIN")
	feed(logNow, "query[A] ok.example.com from 192.168.1.6")
	feed(logNow.Add(30*time.Millisecond), "reply ok.example.com is 1.2.3.4")
	return l
}

func get(t *testing.T, d Deps, url string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func TestRecordsEndpoint(t *testing.T) {
	d := Deps{Profiles: &fakeProfiles{p: map[string]any{}}, Log: sampleLog()}
	rec := get(t, d, "/api/openbox/dns-filter/records?search=&result=blocked&page=1&pageSize=10")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") == "" {
		t.Fatal(rec.Code, rec.Header(), rec.Body)
	}
	var out struct {
		Rows []struct {
			ID      int64    `json:"id"`
			At      int64    `json:"at"`
			Domain  string   `json:"domain"`
			QType   string   `json:"qtype"`
			Source  string   `json:"source"`
			Result  string   `json:"result"`
			List    string   `json:"list"`
			Elapsed *float64 `json:"elapsed"`
			Via     string   `json:"via"`
		} `json:"rows"`
		Total, Page, PageSize int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || out.Page != 1 || out.PageSize != 10 || len(out.Rows) != 1 {
		t.Fatalf("%+v", out)
	}
	r := out.Rows[0]
	if r.Domain != "ads.example.com" || r.Result != "blocked" || r.List != "AdList" || r.Source != "192.168.1.5" || r.QType != "A" || r.Via != "dnsmasq" || r.At != logNow.UnixMilli() || r.Elapsed == nil {
		t.Errorf("%+v", r)
	}
	// 全部、搜索
	var all struct{ Total int }
	json.Unmarshal(get(t, d, "/api/openbox/dns-filter/records?search=OK.example&result=&page=1&pageSize=20").Body.Bytes(), &all)
	if all.Total != 1 {
		t.Errorf("搜索: %d", all.Total)
	}
	// 非数字的页码按默认
	if rec := get(t, d, "/api/openbox/dns-filter/records?page=x&pageSize=y"); rec.Code != 200 {
		t.Error(rec.Code)
	}
}

func TestRecordsWithoutLogIsEmptyPage(t *testing.T) {
	rec := get(t, Deps{Profiles: &fakeProfiles{p: map[string]any{}}}, "/api/openbox/dns-filter/records?pageSize=10")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if rows, ok := raw["rows"].([]any); !ok || len(rows) != 0 || raw["total"] != float64(0) || raw["pageSize"] != float64(10) || raw["page"] != float64(1) {
		t.Errorf("%v", raw)
	}
}

func TestSummaryEndpoint(t *testing.T) {
	f := &fakeProfiles{p: map[string]any{"dns": map[string]any{"filter": map[string]any{"enabled": true}}}}
	d := Deps{Profiles: f, Log: sampleLog(), Now: func() time.Time { return logNow.Add(5 * time.Minute) }}
	rec := get(t, d, "/api/openbox/dns-filter/summary")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var s struct {
		Enabled, Connected bool
		Queries, Blocked   int
		AverageMs          *float64
		Hourly             []struct {
			Hour    int64
			Queries int
			Blocked int
			Elapsed float64
			Timed   int
		}
		TopDomains []struct {
			Domain string
			Count  int
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Enabled || !s.Connected || s.Queries != 2 || s.Blocked != 1 || s.AverageMs == nil || *s.AverageMs != 20 || len(s.Hourly) != 24 || len(s.TopDomains) != 2 {
		t.Errorf("%+v", s)
	}
	if s.Hourly[23].Queries != 2 || s.Hourly[23].Blocked != 1 || s.Hourly[23].Timed != 2 {
		t.Errorf("%+v", s.Hourly[23])
	}
	// 没有日志来源:数字为 0、connected 为假,averageMs 是 null,曲线仍是 24 项
	rec = get(t, Deps{Profiles: f}, "/api/openbox/dns-filter/summary")
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if rec.Code != 200 || raw["connected"] != false || raw["enabled"] != true || raw["queries"] != float64(0) || raw["averageMs"] != nil || len(raw["hourly"].([]any)) != 24 || len(raw["topDomains"].([]any)) != 0 {
		t.Errorf("%d %v", rec.Code, raw)
	}
	// 读档案失败 500
	if rec := get(t, Deps{Profiles: &fakeProfiles{getErr: errors.New("boom")}}, "/api/openbox/dns-filter/summary"); rec.Code != 500 {
		t.Error(rec.Code)
	}
}

func TestRecordsSummaryMethods(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Deps{Profiles: &fakeProfiles{p: map[string]any{}}})
	for _, p := range []string{"/api/openbox/dns-filter/records", "/api/openbox/dns-filter/summary"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}
