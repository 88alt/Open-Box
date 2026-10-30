package failover

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/failover"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

func planOne() failover.Plan {
	r := "HK-01"
	return failover.Plan{{ID: "g1", Tag: "故障转移", RejectTag: "__fo:reject", Lanes: []failover.Lane{
		{ID: "l1", Name: "主线", Index: 0, Members: []string{"HK-01"}, Valid: []string{"HK-01"}, Mode: "single", Ref: &r},
	}}}
}

func do(t *testing.T, d Deps, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestStatusEndpoint(t *testing.T) {
	d := Deps{
		Plan: func() (failover.Plan, error) { return planOne(), nil },
		Kernel: func(context.Context) ([]singbox.KernelProxy, error) {
			return []singbox.KernelProxy{{Name: "故障转移", Type: "Selector", Now: "HK-01"}}, nil
		},
	}
	rec := do(t, d, http.MethodGet, "/api/openbox/failover/status", "")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") == "" {
		t.Fatal(rec.Code, rec.Header(), rec.Body)
	}
	var st struct {
		Running bool
		Groups  []struct {
			ID, Tag, Status string
			CurrentLaneID   *string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Running || len(st.Groups) != 1 || st.Groups[0].Status != "ok" || st.Groups[0].CurrentLaneID == nil || *st.Groups[0].CurrentLaneID != "l1" {
		t.Errorf("%+v", st)
	}
	// 没接计划来源:空组列表,不报错
	rec = do(t, Deps{}, http.MethodGet, "/api/openbox/failover/status", "")
	var raw map[string]any
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if rec.Code != 200 || len(raw["groups"].([]any)) != 0 {
		t.Errorf("%d %v", rec.Code, raw)
	}
	// 计划读失败 500
	bad := Deps{Plan: func() (failover.Plan, error) { return nil, errors.New("坏了") }}
	if rec := do(t, bad, http.MethodGet, "/api/openbox/failover/status", ""); rec.Code != 500 {
		t.Error(rec.Code)
	}
}

func TestRefreshAndRecheckAreHonest(t *testing.T) {
	d := Deps{Plan: func() (failover.Plan, error) { return planOne(), nil }}
	if rec := do(t, d, http.MethodPost, "/api/openbox/failover/refresh", "{}"); rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "还没实现") {
		t.Errorf("refresh: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, d, http.MethodPost, "/api/openbox/failover/recheck", `{"id":"g1"}`); rec.Code != http.StatusNotImplemented {
		t.Errorf("存在的组:管理器没做,如实回 501: %d", rec.Code)
	}
	if rec := do(t, d, http.MethodPost, "/api/openbox/failover/recheck", `{"id":"nope"}`); rec.Code != http.StatusNotFound {
		t.Errorf("不存在的组回 404: %d", rec.Code)
	}
	if rec := do(t, d, http.MethodPost, "/api/openbox/failover/recheck", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("坏请求体回 400: %d", rec.Code)
	}
	for _, c := range [][2]string{{http.MethodPost, "/api/openbox/failover/status"}, {http.MethodGet, "/api/openbox/failover/refresh"}} {
		if rec := do(t, d, c[0], c[1], ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%v: %d", c, rec.Code)
		}
	}
}
