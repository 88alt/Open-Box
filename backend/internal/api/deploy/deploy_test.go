package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/deploystate"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

type fake struct {
	res    singbox.DeployResult
	st     deploystate.State
	stErr  error
	called int
}

func (f *fake) Run(context.Context) singbox.DeployResult { f.called++; return f.res }
func (f *fake) State() (deploystate.State, error)        { return f.st, f.stErr }

func do(t *testing.T, f *fake, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestStatusMapping(t *testing.T) {
	for _, c := range []struct {
		res  singbox.DeployResult
		code int
	}{
		{singbox.DeployResult{OK: true, Stage: "running", Message: "已应用并正常运行"}, 200},
		{singbox.DeployResult{Stage: "validate", Message: "x"}, 422},
		{singbox.DeployResult{Stage: "conflict", Message: "x"}, 409},
		{singbox.DeployResult{Stage: "error", Busy: true, Message: "x"}, 409},
		{singbox.DeployResult{Stage: "start", Message: "x", RolledBack: true}, 502},
		{singbox.DeployResult{Stage: "verify", Message: "x"}, 502},
		{singbox.DeployResult{Stage: "error", Message: "x"}, 500},
	} {
		f := &fake{res: c.res}
		rec := do(t, f, http.MethodPost, "/api/openbox/deploy")
		if rec.Code != c.code {
			t.Errorf("%+v: 状态码 %d,要 %d", c.res, rec.Code, c.code)
		}
		// 不管状态码,响应体都是前端要的 {ok, stage, message, badTags}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["ok"] != c.res.OK || out["stage"] != c.res.Stage || out["message"] != "x" && !c.res.OK {
			t.Errorf("响应体不对: %s", rec.Body)
		}
		if tags, ok := out["badTags"].([]any); !ok || len(tags) != 0 {
			t.Errorf("badTags 应该是空数组而不是 null: %s", rec.Body)
		}
		if _, has := out["Busy"]; has || out["busy"] != nil {
			t.Errorf("Busy 不进响应: %s", rec.Body)
		}
		if rec.Header().Get("Cache-Control") == "" {
			t.Errorf("应该 no-store")
		}
	}
}

func TestRolledBackInBody(t *testing.T) {
	rec := do(t, &fake{res: singbox.DeployResult{Stage: "verify", Message: "x", RolledBack: true}}, http.MethodPost, "/api/openbox/deploy")
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["rolledBack"] != true {
		t.Errorf("回滚了要告诉前端: %s", rec.Body)
	}
}

func TestState(t *testing.T) {
	f := &fake{st: deploystate.State{Stage: "idle", BadTags: []string{}}}
	rec := do(t, f, http.MethodGet, "/api/openbox/deploy/state")
	var out struct {
		State struct {
			Stage   string   `json:"stage"`
			Message string   `json:"message"`
			At      int64    `json:"at"`
			BadTags []string `json:"badTags"`
		} `json:"state"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.State.Stage != "idle" || out.State.BadTags == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if f.called != 0 {
		t.Fatal("读状态不能触发部署")
	}
	f.stErr = errors.New("坏了")
	if rec := do(t, f, http.MethodGet, "/api/openbox/deploy/state"); rec.Code != 500 {
		t.Fatalf("读不到应该 500: %d", rec.Code)
	}
}

func TestMethods(t *testing.T) {
	f := &fake{}
	if rec := do(t, f, http.MethodGet, "/api/openbox/deploy"); rec.Code != 405 {
		t.Errorf("GET /deploy 应该 405: %d", rec.Code)
	}
	if rec := do(t, f, http.MethodPost, "/api/openbox/deploy/state"); rec.Code != 405 {
		t.Errorf("POST /deploy/state 应该 405: %d", rec.Code)
	}
	if f.called != 0 {
		t.Error("方法不对不能触发部署")
	}
}
