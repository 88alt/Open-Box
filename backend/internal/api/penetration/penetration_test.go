package penetration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/penetration"
)

type fake struct {
	res *penetration.Result
	err error
	got string
}

func (f *fake) Query(_ context.Context, target string) (*penetration.Result, error) {
	f.got = target
	return f.res, f.err
}

func do(f *fake, method, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/penetration", strings.NewReader(body)))
	return rec
}

func TestQueryShape(t *testing.T) {
	fin := "hk-01"
	f := &fake{res: &penetration.Result{
		Matched: &penetration.Matched{Index: 4, Rule: map[string]any{"rule_set": "x"}, Outbound: "AI"},
		Owner:   &penetration.Owner{Kind: "policy", Name: "AI"},
		Chain:   []string{"AI", "hk-01"}, FinalOutbound: &fin,
	}}
	rec := do(f, http.MethodPost, `{"target":"chat.openai.com"}`)
	if rec.Code != 200 || f.got != "chat.openai.com" || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("%d %q %s", rec.Code, f.got, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	m, _ := out["matched"].(map[string]any)
	o, _ := out["owner"].(map[string]any)
	if m["index"] != float64(4) || m["outbound"] != "AI" || o["kind"] != "policy" || out["finalOutbound"] != "hk-01" {
		t.Fatalf("%s", rec.Body)
	}
	for _, k := range []string{"matchError", "chainError", "routingStale", "preResolve", "assumed"} {
		if _, has := out[k]; has {
			t.Errorf("空的 %s 不该出现: %s", k, rec.Body)
		}
	}
}

func TestErrors(t *testing.T) {
	if rec := do(&fake{err: &penetration.InputError{Msg: "目标不对"}}, http.MethodPost, `{"target":"-x"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "目标不对") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(&fake{err: errors.New("读不到当前配置")}, http.MethodPost, `{"target":"a.com"}`); rec.Code != 500 || !strings.Contains(rec.Body.String(), "读不到当前配置") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(&fake{}, http.MethodGet, ``); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("%d", rec.Code)
	}
	if rec := do(&fake{}, http.MethodPost, `{bad`); rec.Code != 400 {
		t.Fatalf("%d", rec.Code)
	}
}
