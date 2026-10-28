package subshares

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subshare"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Service: &subshare.Service{
		Store:           st,
		SubscriptionIDs: func(context.Context) ([]string, error) { return []string{"s1", "s2"}, nil },
	}})
	return mux
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if cc := rec.Header().Get("Cache-Control"); rec.Code != http.StatusMethodNotAllowed && !strings.Contains(cc, "no-store") {
		t.Errorf("%s %s: Cache-Control = %q", method, path, cc)
	}
	return rec.Code, m
}

func shareOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	sh, ok := m["share"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 share: %v", m)
	}
	return sh
}

func TestFullFlowLikeTheFrontend(t *testing.T) {
	h := newHandler(t)
	// 空列表是 []
	code, m := do(t, h, "GET", "/api/openbox/subscription-shares", "")
	if code != 200 || m["shares"] == nil || len(m["shares"].([]any)) != 0 {
		t.Fatalf("%d %v", code, m)
	}
	// 新建:前端带草稿 token
	draft := strings.Repeat("0a", 24)
	code, m = do(t, h, "POST", "/api/openbox/subscription-shares",
		`{"name":"家里","host":"192.168.1.1:3036","protocol":"http","subscriptionIds":["s1","s2"],"token":"`+draft+`"}`)
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	sh := shareOf(t, m)
	id := sh["id"].(string)
	if sh["token"] != draft || sh["enabled"] != true || sh["protocol"] != "http" || sh["host"] != "192.168.1.1:3036" {
		t.Fatalf("%v", sh)
	}
	if len(sh["subscriptionIds"].([]any)) != 2 || sh["createdAt"].(float64) <= 0 {
		t.Fatalf("%v", sh)
	}
	// 启停:前端把 name / host / protocol / subscriptionIds 原样带回,加 enabled
	code, m = do(t, h, "PATCH", "/api/openbox/subscription-shares/"+id,
		`{"name":"家里","host":"192.168.1.1:3036","protocol":"http","subscriptionIds":["s1","s2"],"enabled":false}`)
	if code != 200 || shareOf(t, m)["enabled"] != false || shareOf(t, m)["token"] != draft {
		t.Fatalf("%d %v", code, m)
	}
	// 编辑但不换 token(regenerate:false)
	code, m = do(t, h, "PATCH", "/api/openbox/subscription-shares/"+id,
		`{"name":"客厅","host":"","protocol":"https","subscriptionIds":["s2"],"regenerate":false}`)
	if code != 200 || shareOf(t, m)["name"] != "客厅" || shareOf(t, m)["token"] != draft || shareOf(t, m)["enabled"] != false {
		t.Fatalf("%d %v", code, m)
	}
	// 编辑时勾「换新 token」
	code, m = do(t, h, "PATCH", "/api/openbox/subscription-shares/"+id,
		`{"name":"客厅","host":"","protocol":"https","subscriptionIds":["s2"],"regenerate":true}`)
	t2 := shareOf(t, m)["token"]
	if code != 200 || t2 == draft || len(t2.(string)) != 48 {
		t.Fatalf("%d %v", code, m)
	}
	// 单独换 token
	code, m = do(t, h, "POST", "/api/openbox/subscription-shares/"+id+"/regenerate", "")
	if code != 200 || shareOf(t, m)["token"] == t2 {
		t.Fatalf("%d %v", code, m)
	}
	// 列表里能看到最新的
	_, m = do(t, h, "GET", "/api/openbox/subscription-shares", "")
	if l := m["shares"].([]any); len(l) != 1 || l[0].(map[string]any)["name"] != "客厅" {
		t.Fatalf("%v", m)
	}
	// 删除
	code, m = do(t, h, "DELETE", "/api/openbox/subscription-shares/"+id, "")
	if code != 200 || m["ok"] != true {
		t.Fatalf("%d %v", code, m)
	}
	_, m = do(t, h, "GET", "/api/openbox/subscription-shares", "")
	if len(m["shares"].([]any)) != 0 {
		t.Fatalf("%v", m)
	}
}

func TestPatchCannotSetToken(t *testing.T) {
	h := newHandler(t)
	_, m := do(t, h, "POST", "/api/openbox/subscription-shares", `{"name":"a","subscriptionIds":["s1"]}`)
	sh := shareOf(t, m)
	id, tok := sh["id"].(string), sh["token"]
	_, m = do(t, h, "PATCH", "/api/openbox/subscription-shares/"+id, `{"token":"`+strings.Repeat("f", 40)+`","enabled":false}`)
	if shareOf(t, m)["token"] != tok {
		t.Fatalf("PATCH 不应接受客户端指定的 token: %v", m)
	}
}

func TestTokenReplacedWhenDraftIsTaken(t *testing.T) {
	h := newHandler(t)
	draft := strings.Repeat("ab", 24)
	body := `{"name":"a","subscriptionIds":["s1"],"token":"` + draft + `"}`
	_, m1 := do(t, h, "POST", "/api/openbox/subscription-shares", body)
	_, m2 := do(t, h, "POST", "/api/openbox/subscription-shares", body)
	if shareOf(t, m1)["token"] != draft || shareOf(t, m2)["token"] == draft {
		t.Fatalf("第二条应换 token(前端据此提示):%v %v", m1, m2)
	}
}

func TestErrors(t *testing.T) {
	h := newHandler(t)
	base := "/api/openbox/subscription-shares"
	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"没名称", "POST", base, `{"subscriptionIds":["s1"]}`, 400},
		{"没订阅", "POST", base, `{"name":"a","subscriptionIds":[]}`, 400},
		{"订阅不存在", "POST", base, `{"name":"a","subscriptionIds":["zzz"]}`, 400},
		{"主机不对", "POST", base, `{"name":"a","subscriptionIds":["s1"],"host":"http://x"}`, 400},
		{"协议不对", "POST", base, `{"name":"a","subscriptionIds":["s1"],"protocol":"ftp"}`, 400},
		{"不是 JSON", "POST", base, `不是json`, 400},
		{"JSON 后面还有东西", "POST", base, `{"name":"a","subscriptionIds":["s1"]} {}`, 400},
		{"名称类型不对", "POST", base, `{"name":1,"subscriptionIds":["s1"]}`, 400},
		{"请求体太大", "POST", base, `{"name":"` + strings.Repeat("a", maxBody) + `","subscriptionIds":["s1"]}`, 413},
		{"改不存在的", "PATCH", base + "/nope", `{"enabled":true}`, 404},
		{"换不存在的", "POST", base + "/nope/regenerate", ``, 404},
		{"删不存在的", "DELETE", base + "/nope", ``, 404},
		{"列表不收 PUT", "PUT", base, `{}`, 405},
		{"单条不收 GET", "GET", base + "/x", ``, 405},
		{"换 token 不收 GET", "GET", base + "/x/regenerate", ``, 405},
	} {
		code, m := do(t, h, c.method, c.path, c.body)
		if code != c.want {
			t.Errorf("%s: %d(想要 %d) %v", c.name, code, c.want, m)
			continue
		}
		if c.want < 500 && c.want != 405 {
			if msg, _ := m["error"].(string); msg == "" {
				t.Errorf("%s: 错误响应里没有 error: %v", c.name, m)
			}
		}
	}
}

func TestInvalidPatchLeavesShareUntouched(t *testing.T) {
	h := newHandler(t)
	_, m := do(t, h, "POST", "/api/openbox/subscription-shares", `{"name":"a","subscriptionIds":["s1"]}`)
	id := shareOf(t, m)["id"].(string)
	if code, _ := do(t, h, "PATCH", "/api/openbox/subscription-shares/"+id, `{"name":"改了","protocol":"ftp"}`); code != 400 {
		t.Fatal(code)
	}
	_, m = do(t, h, "GET", "/api/openbox/subscription-shares", "")
	if m["shares"].([]any)[0].(map[string]any)["name"] != "a" {
		t.Fatalf("校验不过不应改动: %v", m)
	}
}
