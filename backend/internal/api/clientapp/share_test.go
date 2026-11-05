package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
)

type fakeShare struct {
	gotLinks              map[string]string
	gotSID, gotGen, gotIP string
	out                   map[string]clientapp.ShareSeal
	res                   clientapp.ShareResult
	rotated               clientapp.ShareRotated
	gotRotate             []string
	err                   error
}

func (f *fakeShare) ShareCodes(_ context.Context, links map[string]string) (map[string]clientapp.ShareSeal, error) {
	f.gotLinks = links
	return f.out, f.err
}

func (f *fakeShare) Share(_ context.Context, sid, gen, ip string) (clientapp.ShareResult, error) {
	f.gotSID, f.gotGen, f.gotIP = sid, gen, ip
	return f.res, f.err
}

func (f *fakeShare) RotateShare(_ context.Context, sid string) (clientapp.ShareRotated, error) {
	f.gotRotate = append(f.gotRotate, sid)
	return f.rotated, f.err
}

func TestShareCodesOK(t *testing.T) {
	f := &fakeShare{out: map[string]clientapp.ShareSeal{"a": {PairCode: "AB2D", Gen: "0000000000000001", Sealed: `{"v":1}`}}}
	rec := callPath(t, Deps{Share: f}, http.MethodPost, "/api/openbox/client-app/share-codes", `{"links":{"a":"ss://x","b":""}}`)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if f.gotLinks["a"] != "ss://x" || len(f.gotLinks) != 2 {
		t.Fatalf("链接原样交给服务层: %+v", f.gotLinks)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["a"]["pairCode"] != "AB2D" || got["a"]["gen"] != "0000000000000001" || got["a"]["sealed"] != `{"v":1}` || len(got) != 1 {
		t.Fatalf("形状应与前端 OpenboxShareCodeSeal 一致: %s", rec.Body)
	}
	// 一台都没出码时是 {} 而不是 null
	rec = callPath(t, Deps{Share: &fakeShare{out: map[string]clientapp.ShareSeal{}}}, http.MethodPost, "/api/openbox/client-app/share-codes", `{"links":{}}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("空结果: %d %s", rec.Code, rec.Body)
	}
}

func TestShareCodesErrors(t *testing.T) {
	post := func(d Deps, body string) *httptest.ResponseRecorder {
		return callPath(t, d, http.MethodPost, "/api/openbox/client-app/share-codes", body)
	}
	f := &fakeShare{}
	for name, body := range map[string]string{
		"不是 JSON":      `nope`,
		"没有 links":     `{}`,
		"links 是 null": `{"links":null}`,
		"links 不是对象":   `{"links":[1]}`,
		"值不是字符串":       `{"links":{"a":5}}`,
	} {
		if rec := post(Deps{Share: f}, body); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if f.gotLinks != nil {
		t.Error("请求体不合格不该碰服务层")
	}
	if rec := post(Deps{Share: &fakeShare{err: &clientapp.InputError{Msg: "一次最多 64 台服务器"}}}, `{"links":{}}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "最多 64") {
		t.Errorf("输入错误: %d %s", rec.Code, rec.Body)
	}
	if rec := post(Deps{Share: &fakeShare{err: errors.New("磁盘满了")}}, `{"links":{}}`); rec.Code != 500 {
		t.Errorf("内部错误: %d %s", rec.Code, rec.Body)
	}
	if rec := post(Deps{}, `{"links":{}}`); rec.Code != 503 {
		t.Errorf("没接服务: %d", rec.Code)
	}
	if rec := callPath(t, Deps{Share: f}, http.MethodGet, "/api/openbox/client-app/share-codes", ""); rec.Code != 405 {
		t.Errorf("GET: %d", rec.Code)
	}
	// 请求体超过上限:413 或 400,不能走到服务层
	big := `{"links":{"a":"` + strings.Repeat("x", maxShareCodesBody) + `"}}`
	f2 := &fakeShare{}
	if rec := post(Deps{Share: f2}, big); rec.Code < 400 || rec.Code >= 500 || f2.gotLinks != nil {
		t.Errorf("超大请求体: %d", rec.Code)
	}
}

func TestShareRotateOK(t *testing.T) {
	f := &fakeShare{rotated: clientapp.ShareRotated{PairCode: "AB2D", Gen: "0000000000000002",
		Server: map[string]any{"id": "a", "name": "A", "protocol": "hysteria2", "port": 7001, "password": "new"}, Applied: "restarted"}}
	rec := callPath(t, Deps{Share: f}, http.MethodPost, "/api/openbox/client-app/share-codes/a/rotate", "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if len(f.gotRotate) != 1 || f.gotRotate[0] != "a" {
		t.Fatalf("路径里的 id 交给服务层: %v", f.gotRotate)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// 形状与前端 OpenboxShareCodeRotated 一致:pairCode / gen / server / applied;没有 message 时不出现这个键
	if got["pairCode"] != "AB2D" || got["gen"] != "0000000000000002" || got["applied"] != "restarted" || got["server"].(map[string]any)["password"] != "new" {
		t.Fatalf("形状: %s", rec.Body)
	}
	if _, has := got["message"]; has {
		t.Fatalf("没有 message 时不该出现: %s", rec.Body)
	}
	// 部署失败不是 HTTP 错误:200,applied=failed,message 写原因
	f2 := &fakeShare{rotated: clientapp.ShareRotated{PairCode: "AB2D", Gen: "0000000000000002", Server: map[string]any{"id": "a"}, Applied: "failed", Message: "sing-box check 没通过"}}
	rec = callPath(t, Deps{Share: f2}, http.MethodPost, "/api/openbox/client-app/share-codes/a/rotate", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"applied":"failed"`) || !strings.Contains(rec.Body.String(), "sing-box check") {
		t.Fatalf("部署失败也是 200: %d %s", rec.Code, rec.Body)
	}
	// id 在路径里是转义的:%2D 解码后交给服务层
	f3 := &fakeShare{}
	callPath(t, Deps{Share: f3}, http.MethodPost, "/api/openbox/client-app/share-codes/srv%2D1/rotate", "")
	if len(f3.gotRotate) != 1 || f3.gotRotate[0] != "srv-1" {
		t.Fatalf("id 应已解码: %v", f3.gotRotate)
	}
}

func TestShareRotateErrors(t *testing.T) {
	path := "/api/openbox/client-app/share-codes/a/rotate"
	var ie error = &clientapp.InputError{Msg: "第 2 台服务器: 端口必须是 1–65535 的整数"}
	for _, c := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{"服务器不存在", clientapp.ErrServerNotFound, 404, "没有这台共享服务器"},
		{"包了一层的不存在", fmt.Errorf("x: %w", clientapp.ErrServerNotFound), 404, "没有这台共享服务器"},
		{"没接档案修改", clientapp.ErrUnavailable, 503, "不可用"},
		{"别的服务器不合格", ie, 400, "第 2 台服务器"},
		{"内部错误", errors.New("磁盘满了"), 500, "更换配对码失败"},
	} {
		rec := callPath(t, Deps{Share: &fakeShare{err: c.err}}, http.MethodPost, path, "")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Errorf("%s: 错误响应也要 no-store", c.name)
		}
	}
	if rec := callPath(t, Deps{}, http.MethodPost, path, ""); rec.Code != 503 {
		t.Errorf("没接服务: %d", rec.Code)
	}
	f := &fakeShare{}
	if rec := callPath(t, Deps{Share: f}, http.MethodGet, path, ""); rec.Code != 405 || len(f.gotRotate) != 0 {
		t.Errorf("GET: %d", rec.Code)
	}
	// 别的子路径回 JSON 404,不落到前端页面,也不碰服务层
	for _, p := range []string{
		"/api/openbox/client-app/share-codes/a",
		"/api/openbox/client-app/share-codes/a/rotate/x",
		"/api/openbox/client-app/share-codes/a/other",
		"/api/openbox/client-app/share-codes/",
	} {
		rec := callPath(t, Deps{Share: f}, http.MethodPost, p, "")
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	if len(f.gotRotate) != 0 {
		t.Errorf("别的路径不该走到服务层: %v", f.gotRotate)
	}
}

func shareCall(t *testing.T, f *fakeShare, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Share: f})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.168.5.20:51234"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestShareGetOK(t *testing.T) {
	f := &fakeShare{res: clientapp.ShareResult{Format: "open-box-share", V: 1, RouterID: "r", SID: "a", Gen: "0000000000000001", ExportedAt: 9,
		Seal: clientapp.Envelope{KDF: clientapp.KDFParams{Alg: "pbkdf2-sha256", Iterations: 200000, Salt: "s"}, Data: "c"}}}
	rec := shareCall(t, f, "/client/v1/share?sid=a&g=0000000000000001")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if f.gotSID != "a" || f.gotGen != "0000000000000001" || f.gotIP != "192.168.5.20" {
		t.Fatalf("参数和来源 IP(取连接对端,不信 X-Forwarded-For): %q %q %q", f.gotSID, f.gotGen, f.gotIP)
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["format"] != "open-box-share" || got["sid"] != "a" || got["routerId"] != "r" || got["seal"].(map[string]any)["data"] != "c" {
		t.Fatalf("形状: %s", rec.Body)
	}
}

func TestShareGetErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{"关着", clientapp.ErrDisabled, 403, `{"error":"disabled"}`},
		{"找不到", clientapp.ErrSyncNotFound, 404, `"error":"not found"`},
		{"限速", &clientapp.LimitedError{After: 61500 * time.Millisecond}, 429, ""},
		{"内部错误", errors.New("db 路径 /secret 坏了"), 500, `"error":"取回失败"`},
	} {
		rec := shareCall(t, &fakeShare{err: c.err}, "/client/v1/share?sid=a&g=x")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "/secret") {
			t.Errorf("%s: 不能把内部细节回给匿名调用方", c.name)
		}
	}
	rec := shareCall(t, &fakeShare{err: &clientapp.LimitedError{After: 61500 * time.Millisecond}}, "/client/v1/share?sid=a&g=x")
	if rec.Header().Get("Retry-After") != "62" {
		t.Errorf("Retry-After 应向上取整: %q", rec.Header().Get("Retry-After"))
	}
	mux := http.NewServeMux()
	Register(mux, Deps{})
	for _, c := range []struct {
		method, path string
		code         int
	}{{http.MethodGet, "/client/v1/share", 503}, {http.MethodPost, "/client/v1/share", 405}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
}
