package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorFormats(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, 400, "坏了")
	if rec.Code != 400 || strings.TrimSpace(rec.Body.String()) != `{"error":"坏了"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	CodeError(rec, 401, "X", "信息")
	if !strings.Contains(rec.Body.String(), `"code":"X"`) || !strings.Contains(rec.Body.String(), `"error":"信息"`) {
		t.Fatal(rec.Body)
	}
	rec = httptest.NewRecorder()
	ErrorCode(rec, 409, "PASSWORD_ALREADY_SET", "已设置")
	if !strings.Contains(rec.Body.String(), `"error":"PASSWORD_ALREADY_SET"`) {
		t.Fatal(rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatal(ct)
	}
}

func TestMethods(t *testing.T) {
	h := Methods(map[string]http.HandlerFunc{
		http.MethodGet:  func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]string{"m": r.Method}) },
		http.MethodPost: func(w http.ResponseWriter, r *http.Request) { JSON(w, 201, nil) },
	})
	for method, want := range map[string]int{"GET": 200, "HEAD": 200, "POST": 201, "DELETE": 405, "PUT": 405} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, "/x", nil))
		if rec.Code != want {
			t.Errorf("%s: 得到 %d, 期望 %d", method, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("DELETE", "/x", nil))
	if got := rec.Header().Get("Allow"); got != "GET, HEAD, POST" {
		t.Fatalf("Allow = %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatal("405 应是 JSON 错误")
	}
}

func TestDecodeJSON(t *testing.T) {
	type body struct{ A string }
	cases := []struct {
		name, in string
		limit    int64
		ok       bool
		status   int
	}{
		{"正常", `{"A":"x"}`, 100, true, 200},
		{"非 JSON", `nope`, 100, false, 400},
		{"尾随内容", `{"A":"x"} {"A":"y"}`, 100, false, 400},
		{"过大", `{"A":"` + strings.Repeat("x", 200) + `"}`, 50, false, 413},
		{"空体", ``, 100, false, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/x", strings.NewReader(c.in))
			var b body
			if got := DecodeJSON(rec, req, &b, c.limit); got != c.ok {
				t.Fatalf("ok = %v", got)
			}
			if !c.ok && rec.Code != c.status {
				t.Fatalf("状态 %d, 期望 %d", rec.Code, c.status)
			}
		})
	}
}
