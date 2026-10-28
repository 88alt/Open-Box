package activation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func call(t *testing.T, d Deps, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestActivationIsFixedForEveryMethod(t *testing.T) {
	d := Deps{Version: "v0.1.369-main.1", KernelVersion: func(context.Context) string { return "1.14.4" }}
	for _, c := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPost, `{"code":"随便","left":"a","right":"b"}`},
		{http.MethodPut, `{"left":"自己的","right":"文字"}`},
		{http.MethodDelete, ""},
	} {
		rec := call(t, d, c.method, "/api/openbox/activation", c.body)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.method, rec.Code, rec.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		def := got["defaults"].(map[string]any)
		if got["activated"] != true || got["left"] != "" || got["right"] != "" ||
			def["left"] != "Open-Box v0.1.369-main.1" || def["right"] != "sing-box 1.14.4" {
			t.Errorf("%s 应回固定状态且不保存请求里的文字: %s", c.method, rec.Body)
		}
	}
	// 写过之后读到的仍然是固定的
	rec := call(t, d, http.MethodGet, "/api/openbox/activation", "")
	if strings.Contains(rec.Body.String(), "自己的") {
		t.Error("不该保存任何东西")
	}
}

func TestActivationDefaultsWithoutVersions(t *testing.T) {
	rec := call(t, Deps{}, http.MethodGet, "/api/openbox/activation", "")
	if !strings.Contains(rec.Body.String(), `"defaults":{"left":"Open-Box","right":"sing-box"}`) {
		t.Errorf("没有版本信息时只显示名字: %s", rec.Body)
	}
	d := Deps{KernelVersion: func(context.Context) string { return "" }}
	if rec := call(t, d, http.MethodGet, "/api/openbox/activation", ""); !strings.Contains(rec.Body.String(), `"right":"sing-box"}`) {
		t.Errorf("读不到内核版本不该带空格或报错: %s", rec.Body)
	}
}

func TestActivationRejectsOtherMethods(t *testing.T) {
	if rec := call(t, Deps{}, http.MethodPatch, "/api/openbox/activation", ""); rec.Code != 405 {
		t.Errorf("PATCH 应 405: %d", rec.Code)
	}
}

func TestTutorialVideosEmptyList(t *testing.T) {
	rec := call(t, Deps{}, http.MethodGet, "/api/openbox/tutorial-videos", "")
	var got struct {
		Videos       []any `json:"videos"`
		UpdatedAt    int   `json:"updatedAt"`
		StaleAfterMs int   `json:"staleAfterMs"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Videos == nil || len(got.Videos) != 0 || got.UpdatedAt != 0 || got.StaleAfterMs != 86400000 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(t, Deps{}, http.MethodPost, "/api/openbox/tutorial-videos", ""); rec.Code != 405 {
		t.Errorf("POST 应 405: %d", rec.Code)
	}
}
