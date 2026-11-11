package nodequality

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	nq "github.com/timeflysoon/Open-Box/backend/internal/nodequality"
)

type fake struct{ got nq.Query }

func (f *fake) Query(_ context.Context, q nq.Query) (nq.Result, error) {
	f.got = q
	return nq.Result{Subscriptions: []nq.SubscriptionQuality{{ID: "s1", Grade: "pending"}}}, nil
}

func serve(t *testing.T, f *fake, method, url string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, url, nil))
	return rec
}

func TestParams(t *testing.T) {
	f := &fake{}
	rec := serve(t, f, http.MethodGet, "/api/openbox/node-quality?allSubscriptions=true&search=hk&details=s1&nodePageSize=10&allNodes=true&node=a%20b&page=2&pageSize=5&nodePage=3")
	if rec.Code != 200 {
		t.Fatalf("code=%d %s", rec.Code, rec.Body)
	}
	want := nq.Query{Page: 2, PageSize: 5, AllSubscriptions: true, Search: "hk", Details: "s1", NodePage: 3, NodePageSize: 10, AllNodes: true, Node: "a b"}
	if f.got != want {
		t.Fatalf("got %+v", f.got)
	}
	var body struct {
		Subscriptions []map[string]any `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Subscriptions) != 1 {
		t.Fatalf("%v %s", err, rec.Body)
	}
	if v, ok := body.Subscriptions[0]["score"]; !ok || v != nil {
		t.Fatalf("score 要是 null 而不是缺键:%v %v", v, ok)
	}
}

// 上游对不认得的数字参数不报错,当没传,由服务按默认值和范围夹取。
func TestBadNumbersAreIgnoredAndMethod(t *testing.T) {
	f := &fake{}
	if rec := serve(t, f, http.MethodGet, "/api/openbox/node-quality?nodePageSize=x&pageSize=&page=-3&allNodes=yes"); rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if want := (nq.Query{Page: -3}); f.got != want {
		t.Fatalf("got %+v", f.got)
	}
	if rec := serve(t, &fake{}, http.MethodPost, "/api/openbox/node-quality"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST → %d", rec.Code)
	}
}
