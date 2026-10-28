package servers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/portcheck"
)

type fakePorts struct {
	res  portcheck.Result
	err  error
	port int
	id   string
}

func (f *fakePorts) Check(_ context.Context, port int, id string) (portcheck.Result, error) {
	f.port, f.id = port, id
	return f.res, f.err
}

func get(f PortChecker, method, target string) (int, map[string]any) {
	mux := http.NewServeMux()
	Register(mux, Deps{Ports: f})
	req := httptest.NewRequest(method, target, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestPortCheck(t *testing.T) {
	f := &fakePorts{res: portcheck.Result{OK: true}}
	code, m := get(f, "GET", "/api/openbox/servers/port-check?port=8388&id=srv-1")
	if code != 200 || m["ok"] != true || f.port != 8388 || f.id != "srv-1" {
		t.Fatalf("%d %v %+v", code, m, f)
	}
	if _, has := m["reason"]; has {
		t.Fatalf("ok 时不该带 reason: %v", m)
	}
	// 前端 OpenboxPortCheck:{ ok, reason?, name? }
	f.res = portcheck.Result{Reason: "server", Name: "客厅 SS"}
	code, m = get(f, "GET", "/api/openbox/servers/port-check?port=8388&id=x")
	if code != 200 || m["ok"] != false || m["reason"] != "server" || m["name"] != "客厅 SS" {
		t.Fatalf("%d %v", code, m)
	}
	f.res = portcheck.Result{Reason: "listening"}
	if code, m = get(f, "GET", "/api/openbox/servers/port-check?port=22&id=x"); code != 200 || m["reason"] != "listening" {
		t.Fatalf("%d %v", code, m)
	}
}

// 端口不是整数不是请求错误:交给检测,得到 invalid(前端据此提示);id 带特殊字符原样传。
func TestPortCheckBadPortGoesToChecker(t *testing.T) {
	for _, q := range []string{"port=abc&id=x", "port=&id=x", "id=x", "port=99999999999999999999&id=x", "port=1.5&id=x"} {
		f := &fakePorts{res: portcheck.Result{Reason: "invalid"}}
		code, m := get(f, "GET", "/api/openbox/servers/port-check?"+q)
		if code != 200 || m["reason"] != "invalid" || f.port != 0 {
			t.Errorf("%s: %d %v port=%d", q, code, m, f.port)
		}
	}
	f := &fakePorts{res: portcheck.Result{OK: true}}
	get(f, "GET", "/api/openbox/servers/port-check?port=%208388%20&id=a%2Fb%20c")
	if f.port != 8388 || f.id != "a/b c" {
		t.Fatalf("%d %q", f.port, f.id)
	}
}

func TestPortCheckErrorsAndMethods(t *testing.T) {
	code, m := get(&fakePorts{err: errors.New("读取 tcp 失败")}, "GET", "/api/openbox/servers/port-check?port=9000&id=x")
	if code != 500 || m["error"] == nil {
		t.Fatalf("%d %v", code, m)
	}
	if code, m = get(nil, "GET", "/api/openbox/servers/port-check?port=9000&id=x"); code != 503 || m["error"] == nil {
		t.Fatalf("没接检测应 503: %d %v", code, m)
	}
	if code, _ = get(&fakePorts{}, "POST", "/api/openbox/servers/port-check?port=9000"); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应 405: %d", code)
	}
}
