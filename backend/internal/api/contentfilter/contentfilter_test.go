package contentfilter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	cf "github.com/timeflysoon/Open-Box/backend/internal/contentfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &cf.Service{
		Store: st, Dir: filepath.Join(t.TempDir(), "cf"),
		Fetch: func(_ context.Context, u string) ([]byte, error) {
			switch u {
			case "https://x/r":
				return []byte("[Rule]\nDOMAIN,{{{h}}},REJECT\n"), nil
			case "https://x/bad":
				return nil, &rulelist.InputError{Msg: "坏链接"}
			}
			return nil, &rulelist.FetchError{Msg: "无法连接"}
		},
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Service: svc})
	return mux
}

func call(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s %s 响应不是 JSON: %q", method, path, rec.Body.String())
		}
	}
	return rec.Code, m
}

func TestFullFlow(t *testing.T) {
	mux := newMux(t)
	code, m := call(t, mux, "GET", "/api/openbox/content-filter", "")
	if code != 200 || len(m["sets"].([]any)) != 0 || m["sha256"] == "" {
		t.Fatalf("空列表: %d %v", code, m)
	}

	code, m = call(t, mux, "POST", "/api/openbox/content-filter/preview", `{"dialect":"surge","sourceUrl":"https://x/r","arguments":{"h":"a.com"}}`)
	if code != 200 || m["text"] != "[Rule]\nDOMAIN,a.com,REJECT\n" {
		t.Fatalf("预览: %d %v", code, m)
	}

	code, m = call(t, mux, "POST", "/api/openbox/content-filter", `{"name":"甲","dialect":"surge","sourceUrl":"https://x/r","arguments":{"h":"a.com"}}`)
	sets := m["sets"].([]any)
	if code != 200 || len(sets) != 1 {
		t.Fatalf("保存: %d %v", code, m)
	}
	s := sets[0].(map[string]any)
	id := s["id"].(string)
	if s["enabled"] != true || s["ruleCount"] != float64(1) || s["scriptCount"] != float64(0) || s["arguments"].(map[string]any)["values"].(map[string]any)["h"] != "a.com" {
		t.Fatalf("记录: %v", s)
	}
	if _, has := s["skipped"]; has {
		t.Fatal("skipped 为 0 时不输出")
	}

	code, m = call(t, mux, "GET", "/api/openbox/content-filter/"+id+"/source", "")
	if code != 200 || m["text"] != "[Rule]\nDOMAIN,a.com,REJECT\n" {
		t.Fatalf("source: %d %v", code, m)
	}

	_, before := call(t, mux, "GET", "/api/openbox/content-filter", "")
	code, m = call(t, mux, "PATCH", "/api/openbox/content-filter/"+id, `{"enabled":false}`)
	if code != 200 || m["sets"].([]any)[0].(map[string]any)["enabled"] != false || m["sha256"] == before["sha256"] {
		t.Fatalf("停用: %d %v", code, m)
	}

	// 恢复默认(DESIGN 第 8 节):三份随包规则回来并启用,用户自己加的保留原样(这里是刚被停用的「甲」)
	code, m = call(t, mux, "POST", "/api/openbox/content-filter/restore-defaults", "")
	if code != 200 {
		t.Fatalf("恢复默认: %d %v", code, m)
	}
	byID := map[string]map[string]any{}
	for _, e := range m["sets"].([]any) {
		set := e.(map[string]any)
		byID[set["id"].(string)] = set
	}
	if len(byID) != 4 {
		t.Fatalf("应是三份随包规则加用户自己的一份: %v", m)
	}
	for id, want := range map[string][2]string{
		"2aec6f268d71d8f4": {"综合省流", "quantumultx"},
		"7f19db176d8f1d7b": {"Youtube-Surge", "surge"},
		"a08ac43e05eb8590": {"Youtube-补丁", "surge"},
	} {
		set := byID[id]
		if set == nil || set["name"] != want[0] || set["dialect"] != want[1] || set["enabled"] != true {
			t.Fatalf("随包规则 %s 应回来并启用: %v", id, set)
		}
	}
	if mine := byID[id]; mine == nil || mine["name"] != "甲" || mine["enabled"] != false {
		t.Fatalf("用户自己加的要保留原样(仍是停用): %v", mine)
	}

	code, m = call(t, mux, "DELETE", "/api/openbox/content-filter/"+id, "")
	if code != 200 || len(m["sets"].([]any)) != 3 {
		t.Fatalf("删除用户自己加的: %d %v", code, m)
	}
	code, _ = call(t, mux, "DELETE", "/api/openbox/content-filter/"+id, "")
	if code != 404 {
		t.Fatalf("已删除,再删应 404: %d", code)
	}
}

func TestErrorStatuses(t *testing.T) {
	mux := newMux(t)
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"没名字", "POST", "/api/openbox/content-filter", `{"dialect":"surge","text":"DOMAIN,a"}`, 400},
		{"格式不认识", "POST", "/api/openbox/content-filter", `{"name":"a","dialect":"clash","text":"DOMAIN,a"}`, 400},
		{"链接格式错(来自抓取层)", "POST", "/api/openbox/content-filter/preview", `{"dialect":"surge","sourceUrl":"https://x/bad"}`, 400},
		{"抓取失败", "POST", "/api/openbox/content-filter/preview", `{"dialect":"surge","sourceUrl":"https://down/x"}`, 502},
		{"JSON 坏了", "POST", "/api/openbox/content-filter", `{`, 400},
		{"更新不存在的", "POST", "/api/openbox/content-filter", `{"id":"nope","name":"a","dialect":"surge","text":"DOMAIN,a"}`, 404},
		{"缺 enabled", "PATCH", "/api/openbox/content-filter/x", `{}`, 400},
		{"启停不存在的", "PATCH", "/api/openbox/content-filter/x", `{"enabled":true}`, 404},
		{"取不存在的文本", "GET", "/api/openbox/content-filter/x/source", "", 404},
		{"方法不对", "PUT", "/api/openbox/content-filter", "", 405},
		{"preview 不收 GET", "GET", "/api/openbox/content-filter/preview", "", 405},
	}
	for _, c := range cases {
		code, m := call(t, mux, c.method, c.path, c.body)
		if code != c.want {
			t.Errorf("%s: 得到 %d %v,期望 %d", c.name, code, m, c.want)
		}
		if c.want >= 400 && c.want != 405 && m["error"] == nil {
			t.Errorf("%s: 错误响应要带 error 字段: %v", c.name, m)
		}
	}
}
