package clientapp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
)

// fakePair 只实现子资源要用的两个方法,记下收到的参数。
type fakePair struct {
	PairService
	path string
	img  *clientapp.BackgroundImage
	err  error

	gotRouter, gotToken, gotTag string
}

func (f *fakePair) RuleSetPath(_ context.Context, routerID, token, tag string) (string, error) {
	f.gotRouter, f.gotToken, f.gotTag = routerID, token, tag
	return f.path, f.err
}

func (f *fakePair) Background(_ context.Context, routerID, token string) (*clientapp.BackgroundImage, error) {
	f.gotRouter, f.gotToken = routerID, token
	return f.img, f.err
}
func (f *fakePair) Home(context.Context, string) error { return f.err }
func (f *fakePair) Regions(context.Context, string) (clientapp.RegionsPayload, error) {
	return clientapp.RegionsPayload{}, f.err
}
func (f *fakePair) PublicBackground(context.Context, string) (*clientapp.BackgroundImage, error) {
	return f.img, f.err
}
func (f *fakePair) PublicGeoPath(context.Context, string, string) (string, error) {
	return f.path, f.err
}

func subGet(t *testing.T, d Deps, path string, hdr map[string]string) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Result()
}

func TestRuleSetRoute(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.srs")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakePair{path: file}
	d := Deps{Pair: f}
	resp := subGet(t, d, "/client/v1/r1/config/tok1/rule-set/geosite-cn.srs", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "0123456789" ||
		resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %q %v", resp.StatusCode, body, resp.Header)
	}
	if f.gotRouter != "r1" || f.gotToken != "tok1" || f.gotTag != "geosite-cn.srs" {
		t.Fatalf("参数: %q %q %q", f.gotRouter, f.gotToken, f.gotTag)
	}
	// 支持 Range(App 断点续传)
	resp = subGet(t, d, "/client/v1/r1/config/tok1/rule-set/geosite-cn", map[string]string{"Range": "bytes=2-4"})
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "234" {
		t.Fatalf("Range: %d %q", resp.StatusCode, body)
	}
	// 文件在校验之后被删:404,不是 500
	f.path = filepath.Join(t.TempDir(), "gone.srs")
	if resp = subGet(t, d, "/client/v1/r1/config/tok1/rule-set/list-0123abcd", nil); resp.StatusCode != 404 {
		t.Fatalf("文件没了: %d", resp.StatusCode)
	}
}

func TestSubresourceErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"设备不认得", clientapp.ErrPairUnpaired, 404, `"error":"unpaired"`},
		{"开关关着", clientapp.ErrPairDisabled, 403, `"error":"disabled"`},
		{"没有这个资源", clientapp.ErrTokenNotFound, 404, `"error":"not found"`},
	} {
		for _, path := range []string{
			"/client/v1/r1/config/tok1/rule-set/geosite-cn",
			"/client/v1/r1/config/tok1/background",
		} {
			resp := subGet(t, Deps{Pair: &fakePair{err: c.err}}, path, nil)
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != c.code || !strings.Contains(string(b), c.body) || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
				t.Errorf("%s %s: %d %s %v", c.name, path, resp.StatusCode, b, resp.Header)
			}
		}
	}
	// Pair 没接:503,不是 404 落到前端
	for _, path := range []string{"/client/v1/r1/config/tok1/rule-set/geosite-cn", "/client/v1/r1/config/tok1/background"} {
		if resp := subGet(t, Deps{}, path, nil); resp.StatusCode != 503 {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	// 只许 GET
	mux := http.NewServeMux()
	Register(mux, Deps{Pair: &fakePair{}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/client/v1/r1/config/tok1/background", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestBackgroundRoute(t *testing.T) {
	f := &fakePair{img: &clientapp.BackgroundImage{Type: "image/png", Bytes: []byte("PNGDATA"), Version: "0123456789abcdef"}}
	resp := subGet(t, Deps{Pair: f}, "/client/v1/r1/config/tok1/background", nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "PNGDATA" || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("X-Openbox-Background-Version") != "0123456789abcdef" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %q %v", resp.StatusCode, b, resp.Header)
	}
	if f.gotRouter != "r1" || f.gotToken != "tok1" {
		t.Fatalf("参数: %q %q", f.gotRouter, f.gotToken)
	}
}
