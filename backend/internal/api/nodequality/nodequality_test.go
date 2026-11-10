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
	rec := serve(t, f, http.MethodGet, "/api/openbox/node-quality?allSubscriptions=true&search=hk&details=s1&nodePageSize=10&allNodes=true&node=a%20b")
	if rec.Code != 200 {
		t.Fatalf("code=%d %s", rec.Code, rec.Body)
	}
	want := nq.Query{AllSubscriptions: true, Search: "hk", Details: "s1", NodePageSize: 10, AllNodes: true, Node: "a b"}
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

func TestBadPageSizeAndMethod(t *testing.T) {
	for _, u := range []string{"?nodePageSize=0", "?nodePageSize=x", "?nodePageSize=501"} {
		if rec := serve(t, &fake{}, http.MethodGet, "/api/openbox/node-quality"+u); rec.Code != 400 {
			t.Fatalf("%s → %d", u, rec.Code)
		}
	}
	if rec := serve(t, &fake{}, http.MethodPost, "/api/openbox/node-quality"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST → %d", rec.Code)
	}
}
