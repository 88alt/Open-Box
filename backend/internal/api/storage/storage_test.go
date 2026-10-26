package storage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func newHandler(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Store: st})
	return mux, st
}

func call(h http.Handler, method, path, body string) (int, map[string]any) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestSettingsRoundTrip(t *testing.T) {
	h, _ := newHandler(t)

	code, body := call(h, "GET", "/api/storage", "")
	if code != 200 {
		t.Fatal(code)
	}
	if e, ok := body["entries"].(map[string]any); !ok || len(e) != 0 {
		t.Fatalf("初始应返回空 entries 对象(前端按对象读取): %v", body)
	}

	put := `{"entries":{"config/language":"zh-CN","config/theme":"dark"}}`
	if code, body = call(h, "PUT", "/api/storage", put); code != 200 || body["count"] != float64(2) {
		t.Fatalf("%d %v", code, body)
	}
	_, body = call(h, "GET", "/api/storage", "")
	e := body["entries"].(map[string]any)
	if e["config/language"] != "zh-CN" || e["config/theme"] != "dark" || len(e) != 2 {
		t.Fatalf("读回不一致: %v", e)
	}

	// 整体替换:没出现的键被删除
	call(h, "PUT", "/api/storage", `{"entries":{"config/theme":"light"}}`)
	_, body = call(h, "GET", "/api/storage", "")
	if e = body["entries"].(map[string]any); len(e) != 1 || e["config/theme"] != "light" {
		t.Fatalf("PUT 应整体替换: %v", e)
	}

	// 清空
	call(h, "PUT", "/api/storage", `{"entries":{}}`)
	_, body = call(h, "GET", "/api/storage", "")
	if e = body["entries"].(map[string]any); len(e) != 0 {
		t.Fatalf("应已清空: %v", e)
	}
}

func TestSettingsDropsInvalidEntries(t *testing.T) {
	h, _ := newHandler(t)
	put := `{"entries":{
		"config/ok":"1",
		"other/key":"x",
		"config/number":5,
		"config/null":null,
		"config/access-password-authenticated":"true",
		"` + "config/" + strings.Repeat("k", 300) + `":"long"
	}}`
	if code, body := call(h, "PUT", "/api/storage", put); code != 200 || body["count"] != float64(1) {
		t.Fatalf("%d %v", code, body)
	}
	_, body := call(h, "GET", "/api/storage", "")
	if e := body["entries"].(map[string]any); len(e) != 1 || e["config/ok"] != "1" {
		t.Fatalf("只应保留合规条目: %v", e)
	}
}

func TestSettingsLimitsAndBadInput(t *testing.T) {
	h, _ := newHandler(t)
	if code, _ := call(h, "PUT", "/api/storage", `nope`); code != 400 {
		t.Fatalf("非 JSON 应 400: %d", code)
	}
	var sb strings.Builder
	sb.WriteString(`{"entries":{`)
	for i := 0; i <= maxEntries; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"config/k` + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)) + `":"v"`)
	}
	sb.WriteString(`}}`)
	if code, _ := call(h, "PUT", "/api/storage", sb.String()); code != 413 {
		t.Fatalf("条目过多应 413: %d", code)
	}
	huge := `{"entries":{"config/a":"` + strings.Repeat("x", maxSettingsBody) + `"}}`
	if code, _ := call(h, "PUT", "/api/storage", huge); code != 413 {
		t.Fatalf("请求体过大应 413: %d", code)
	}
	if code, _ := call(h, "POST", "/api/storage", `{}`); code != 405 {
		t.Fatalf("POST 应 405: %d", code)
	}
}

func TestBackgroundImage(t *testing.T) {
	h, _ := newHandler(t)

	if code, body := call(h, "GET", "/api/background-image", ""); code != 200 || body["image"] != "" {
		t.Fatalf("初始应返回空字符串: %d %v", code, body)
	}
	img := "data:image/png;base64,iVBORw0KGgo="
	if code, _ := call(h, "PUT", "/api/background-image", `{"image":"`+img+`"}`); code != 200 {
		t.Fatal(code)
	}
	if _, body := call(h, "GET", "/api/background-image", ""); body["image"] != img {
		t.Fatalf("读回不一致: %v", body)
	}
	if code, _ := call(h, "DELETE", "/api/background-image", ""); code != 200 {
		t.Fatal(code)
	}
	if _, body := call(h, "GET", "/api/background-image", ""); body["image"] != "" {
		t.Fatalf("删除后应为空: %v", body)
	}
	if code, _ := call(h, "DELETE", "/api/background-image", ""); code != 200 {
		t.Fatalf("重复删除不应报错: %d", code)
	}
}

func TestBackgroundImageValidation(t *testing.T) {
	h, _ := newHandler(t)
	for name, body := range map[string]string{
		"空":        `{"image":""}`,
		"不是图片":     `{"image":"hello"}`,
		"脚本":       `{"image":"javascript:alert(1)"}`,
		"data 非图片": `{"image":"data:text/html;base64,PGI+"}`,
		"缺字段":      `{}`,
	} {
		if code, _ := call(h, "PUT", "/api/background-image", body); code != 400 {
			t.Errorf("%s: 应 400, 得到 %d", name, code)
		}
	}
	huge := `{"image":"data:image/png;base64,` + strings.Repeat("A", maxImageBody) + `"}`
	if code, _ := call(h, "PUT", "/api/background-image", huge); code != 413 {
		t.Fatalf("过大应 413: %d", code)
	}
}
