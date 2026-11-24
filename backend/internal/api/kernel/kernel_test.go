package kernel

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/kernelswitch"
)

func newMux(t *testing.T, compatible func(context.Context) []string) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	kf := filepath.Join(dir, "router-kernel")
	if err := os.WriteFile(kf, []byte("mihomo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := kernelswitch.New(kernelswitch.Deps{
		KernelFile: kf,
		SingBox:    filepath.Join(dir, "sing-box"),
		Mihomo:     filepath.Join(dir, "mihomo"),
		DataDir:    dir,
		Version: func(_ context.Context, k string) (string, bool) {
			if k == "sing-box" {
				return "sing-box version 1.14.4", true
			}
			return "Mihomo Meta v1.19.0", true
		},
	})
	mux := http.NewServeMux()
	Register(mux, Deps{
		Engine: engine, Compatible: compatible,
		Switch: func(_ context.Context, k string) kernelswitch.Result {
			if k == "bogus" {
				return kernelswitch.Result{Code: 1, Stderr: "不支持"}
			}
			return kernelswitch.Result{OK: true, Code: 0}
		},
	})
	return mux
}

func TestPreflightJSON(t *testing.T) {
	mux := newMux(t, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/kernel/switch/preflight?kernel=sing-box", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Kernel string               `json:"kernel"`
		Checks []kernelswitch.Check `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Kernel != "sing-box" || len(body.Checks) != 8 {
		t.Fatalf("body = %+v", body)
	}
	if body.Checks[0].ID != "target" || body.Checks[0].Status != "failed" {
		t.Fatalf("第一项 = %+v", body.Checks[0])
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %s", ct)
	}
	if v := rec.Header().Get("Vary"); v != "Accept" {
		t.Errorf("Vary = %q", v)
	}
}

func TestPreflightNDJSON(t *testing.T) {
	mux := newMux(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/openbox/kernel/switch/preflight?kernel=sing-box", nil)
	req.Header.Set("Accept", "application/x-ndjson, application/json;q=0.5")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("Content-Type = %s", ct)
	}
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	var ids []string
	sawDone := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var obj struct {
			Check  *kernelswitch.Check `json:"check"`
			Done   bool                `json:"done"`
			Kernel string              `json:"kernel"`
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("行 %q: %v", line, err)
		}
		if obj.Done {
			sawDone = true
			if obj.Kernel != "sing-box" {
				t.Errorf("done.kernel = %q", obj.Kernel)
			}
			continue
		}
		if obj.Check == nil {
			t.Fatalf("行 %q 既不是 check 也不是 done", line)
		}
		ids = append(ids, obj.Check.ID)
	}
	if !sawDone {
		t.Fatal("没有 done 行")
	}
	if strings.Join(ids, ",") != strings.Join(kernelswitch.Order, ",") {
		t.Fatalf("顺序 = %v", ids)
	}
}

func TestPreflightBadKernel(t *testing.T) {
	mux := newMux(t, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/kernel/switch/preflight?kernel=openbox", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "不支持的路由器内核") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPreflightMethodNotAllowed(t *testing.T) {
	mux := newMux(t, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/kernel/switch/preflight?kernel=sing-box", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestCompatibilityShape(t *testing.T) {
	mux := newMux(t, func(context.Context) []string { return []string{"规则集 geosite-x 不可用(链接拉不到)"} })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/kernel/mihomo/compatibility", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"blockers":[],"problems":["规则集 geosite-x 不可用(链接拉不到)"]}` {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// 没有兼容性问题时 problems 是空数组,不是 null。
	mux = newMux(t, nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/kernel/mihomo/compatibility", nil))
	if strings.TrimSpace(rec.Body.String()) != `{"blockers":[],"problems":[]}` {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestSwitchEndpoint(t *testing.T) {
	mux := newMux(t, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/kernel/switch", strings.NewReader(`{"kernel":"mihomo"}`)))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true,"code":0,"stderr":""}` {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 内核名不合法:400,不会调到切换。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/kernel/switch", strings.NewReader(`{"kernel":"bogus"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	// 坏 JSON:400。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/kernel/switch", strings.NewReader(`{`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", rec.Code)
	}
	// GET 不允许。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/kernel/switch", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAcceptsNDJSON(t *testing.T) {
	cases := map[string]bool{
		"":                                       false,
		"*/*":                                    false,
		"text/html":                              false,
		"application/json":                       false,
		"application/x-ndjson":                   true,
		"application/ndjson":                     true,
		"application/x-ndjson, application/json": true,
		" application/x-ndjson ; q=0.9":          true,
		"text/plain, application/x-ndjson;q=0.1": true,
	}
	for accept, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if got := acceptsNDJSON(req); got != want {
			t.Errorf("Accept %q = %v,想要 %v", accept, got, want)
		}
	}
}
