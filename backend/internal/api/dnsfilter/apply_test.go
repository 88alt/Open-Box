package dnsfilter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
)

type fakeApplier struct {
	st     dnsfilter.Status
	err    error
	update bool
	listID string
	calls  int
}

func (f *fakeApplier) Status(context.Context) (dnsfilter.Status, error) { return f.st, f.err }
func (f *fakeApplier) Apply(_ context.Context, update bool, id string) (dnsfilter.Status, error) {
	f.calls++
	f.update, f.listID = update, id
	return f.st, f.err
}

func post(t *testing.T, a Applier, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Profiles: &fakeProfiles{p: map[string]any{}}, Filter: a})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/dns-filter/apply", strings.NewReader(body)))
	return rec
}

func TestApplyPassesBodyAndReturnsStatus(t *testing.T) {
	f := &fakeApplier{st: dnsfilter.BuildStatus(dnsfilter.Default())}
	rec := post(t, f, `{"update":true,"listId":" a "}`)
	if rec.Code != 200 || !f.update || f.listID != "a" {
		t.Fatalf("%d %s update=%v listID=%q", rec.Code, rec.Body, f.update, f.listID)
	}
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st["pending"] != false {
		t.Fatalf("应返回 DnsFilterStatus: %v %s", err, rec.Body)
	}
	if rec := post(t, f, ``); rec.Code != 200 || f.update || f.listID != "" {
		t.Fatalf("空请求体等于不更新: %d", rec.Code)
	}
}

func TestApplyErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{dnsfilter.ErrBusy, 409},
		{dnsfilter.ErrNotRunning, 409},
		{dnsfilter.ErrNoUsableList, 400},
		{&dnsfilter.InputError{Msg: "没有这份名单: x"}, 400},
		{&dnsfilter.ApplyError{Msg: "健康检查没通过,已回滚"}, 500},
		{errors.New("磁盘满了"), 500},
	}
	for _, c := range cases {
		rec := post(t, &fakeApplier{err: c.err}, `{}`)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.err.Error()) {
			t.Errorf("%v: 得到 %d %s,期望 %d 且带原因", c.err, rec.Code, rec.Body, c.code)
		}
	}
}

func TestApplyBadRequestAndUnavailable(t *testing.T) {
	f := &fakeApplier{}
	if rec := post(t, f, `{bad`); rec.Code != 400 || f.calls != 0 {
		t.Errorf("坏 JSON 应 400 且不触发应用: %d", rec.Code)
	}
	if rec := post(t, f, `{"x":"`+strings.Repeat("a", 5000)+`"}`); rec.Code != 413 || f.calls != 0 {
		t.Errorf("过大请求体应 413: %d", rec.Code)
	}
	if rec := post(t, nil, `{}`); rec.Code != 503 {
		t.Errorf("没接应用服务应 503: %d", rec.Code)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Profiles: &fakeProfiles{p: map[string]any{}}, Filter: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/dns-filter/apply", nil))
	if rec.Code != 405 {
		t.Errorf("GET apply 应 405: %d", rec.Code)
	}
}

func TestGetUsesFilterStatusWhenWired(t *testing.T) {
	st := dnsfilter.BuildStatus(dnsfilter.Default())
	st.Pending = true
	mux := http.NewServeMux()
	Register(mux, Deps{Profiles: &fakeProfiles{p: map[string]any{}}, Filter: &fakeApplier{st: st}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/dns-filter", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
