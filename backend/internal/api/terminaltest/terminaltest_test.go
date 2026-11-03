package terminaltest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func serve(t *testing.T, d Deps, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func fixed(c system.TerminalCapability) Deps {
	return Deps{Capability: func(context.Context) system.TerminalCapability { return c }}
}

func TestCapabilityEndpoint(t *testing.T) {
	m := 24
	rec := serve(t, fixed(system.TerminalCapability{OK: true, Missing: []string{}, LAN: &system.TerminalLAN{Device: "br-lan", Address: "192.168.5.1", Mask: &m}}), "GET", "/api/openbox/terminal-test/capability", "")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("应当不缓存")
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true {
		t.Errorf("ok: %v", got["ok"])
	}
	if ms, ok := got["missing"].([]any); !ok || len(ms) != 0 {
		t.Errorf("missing 要是空数组: %v", got["missing"])
	}
	lan := got["lan"].(map[string]any)
	if lan["device"] != "br-lan" || lan["address"] != "192.168.5.1" || lan["mask"] != float64(24) {
		t.Errorf("lan: %v", lan)
	}
}

func TestCapabilityMissingAndNullLAN(t *testing.T) {
	rec := serve(t, fixed(system.TerminalCapability{Missing: []string{"root", "lan"}}), "GET", "/api/openbox/terminal-test/capability", "")
	var got map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if string(got["ok"]) != "false" || string(got["missing"]) != `["root","lan"]` || string(got["lan"]) != "null" {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestCapabilityNilMissingBecomesArray(t *testing.T) {
	rec := serve(t, fixed(system.TerminalCapability{OK: true}), "GET", "/api/openbox/terminal-test/capability", "")
	if !strings.Contains(rec.Body.String(), `"missing":[]`) {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestNoDepsIsNotCapable(t *testing.T) {
	rec := serve(t, Deps{}, "GET", "/api/openbox/terminal-test/capability", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMethods(t *testing.T) {
	d := fixed(system.TerminalCapability{})
	if rec := serve(t, d, "POST", "/api/openbox/terminal-test/capability", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("capability POST: %d", rec.Code)
	}
	if rec := serve(t, d, "GET", "/api/openbox/terminal-test", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("terminal-test GET: %d", rec.Code)
	}
}

func TestRunInputValidation(t *testing.T) {
	d := fixed(system.TerminalCapability{OK: true, Missing: []string{}})
	for _, body := range []string{`{}`, `{"target":""}`, `{"target":"a b"}`, `{"target":"-x"}`, `{"target":"a.com","port":70000}`, `{"target":"a.com","method":"POST"}`} {
		if rec := serve(t, d, "POST", "/api/openbox/terminal-test", body); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, d, "POST", "/api/openbox/terminal-test", `not json`); rec.Code != 400 {
		t.Errorf("坏 JSON: %d", rec.Code)
	}
}

func TestRunNotCapableReportsMissing(t *testing.T) {
	d := fixed(system.TerminalCapability{Missing: []string{"root", "netns"}})
	rec := serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":" example.com ","method":"tcp"}`)
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["target"] != "example.com" || got["mode"] != "lan" || got["capable"] != false {
		t.Errorf("%v", got)
	}
	if ms, _ := got["missing"].([]any); len(ms) != 2 || ms[0] != "root" || ms[1] != "netns" {
		t.Errorf("missing: %v", got["missing"])
	}
}

func TestRunCapableIsNotImplementedYet(t *testing.T) {
	d := fixed(system.TerminalCapability{OK: true, Missing: []string{}})
	rec := serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("要带 error 字段: %s", rec.Body.String())
	}
}
