package factoryreset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/factoryreset"
)

type fakeService struct {
	res     factoryreset.Result
	err     error
	calls   int
	ctxDead bool
}

func (f *fakeService) Run(ctx context.Context) (factoryreset.Result, error) {
	f.calls++
	f.ctxDead = ctx.Err() != nil
	return f.res, f.err
}

func do(t *testing.T, s Service, method string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Service: s})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/factory-reset", nil))
	return rec
}

func TestSuccessShape(t *testing.T) {
	f := &fakeService{res: factoryreset.Result{OK: true, Seeded: 2, ProfileSeeded: true, KernelStopped: true}}
	rec := do(t, f, http.MethodPost)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("应带 no-store")
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// 前端 OpenboxFactoryResetResult 的四个字段,类型要对
	if got["ok"] != true || got["seeded"] != float64(2) || got["profileSeeded"] != true || got["kernelStopped"] != true || len(got) != 4 {
		t.Errorf("响应形状不对: %v", got)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		sub  string
	}{
		{"忙", factoryreset.ErrBusy, http.StatusConflict, "正在部署"},
		{"内核没停下来", &factoryreset.StopError{Msg: "已发出停止命令,内核仍在运行"}, http.StatusInternalServerError, "没有清除任何数据"},
		{"清到一半", &factoryreset.PartialError{Done: []string{"a"}, Failed: []string{"b:只读"}}, http.StatusInternalServerError, "访问密码没有清除"},
		{"别的错误", errors.New("意外"), http.StatusInternalServerError, "意外"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, &fakeService{err: c.err}, http.MethodPost)
			if rec.Code != c.code {
				t.Fatalf("状态码 %d,想要 %d: %s", rec.Code, c.code, rec.Body)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.Contains(body.Error, c.sub) {
				t.Errorf("错误体 %q,应含 %q (%v)", rec.Body, c.sub, err)
			}
		})
	}
}

func TestOnlyPost(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		f := &fakeService{}
		rec := do(t, f, m)
		if rec.Code != http.StatusMethodNotAllowed || f.calls != 0 {
			t.Errorf("%s 应 405 且不执行: %d calls=%d", m, rec.Code, f.calls)
		}
	}
}

// 请求断开不能把清除打断:传给服务的 ctx 不随请求取消。
func TestRequestCancelDoesNotCancelReset(t *testing.T) {
	mux := http.NewServeMux()
	f := &fakeService{res: factoryreset.Result{OK: true}}
	Register(mux, Deps{Service: f})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/openbox/factory-reset", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if f.calls != 1 || f.ctxDead {
		t.Fatalf("calls=%d ctxDead=%v", f.calls, f.ctxDead)
	}
}
