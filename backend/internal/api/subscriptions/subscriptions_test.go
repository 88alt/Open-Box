package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

type env struct {
	h     http.Handler
	svc   *subs.Service
	pages map[string]string // url → 订阅内容
	calls atomic.Int32
	fail  error
}

func links(names ...string) string {
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("trojan://pw@" + strings.ToLower(n) + ".example.com:443#" + n + "\n")
	}
	return sb.String()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pages: map[string]string{}}
	e.svc = subs.New(st, func(_ context.Context, u, ua string) (*sub.Fetched, error) {
		e.calls.Add(1)
		if e.fail != nil {
			return nil, e.fail
		}
		p, ok := e.pages[u]
		if !ok {
			return nil, &sub.FetchError{Msg: "订阅服务器返回 HTTP 404"}
		}
		return &sub.Fetched{Body: []byte(p), Usage: sub.ParseUserinfo("upload=1; download=2; total=30; expire=1800000000")}, nil
	})
	mux := http.NewServeMux()
	Register(mux, Deps{Subs: e.svc})
	e.h = mux
	return e
}

func (e *env) do(method, path string, body any) (int, map[string]any) {
	var rd *strings.Reader
	switch b := body.(type) {
	case nil:
		rd = strings.NewReader("")
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = strings.NewReader(string(j))
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func (e *env) create(t *testing.T, name, url string, extra map[string]any) string {
	t.Helper()
	// 这些测试关心的是别的行为,默认关掉按模板改名;改名本身见 TestRename*。
	body := map[string]any{"name": name, "url": url, "renameOptions": map[string]any{"enabled": false}}
	for k, v := range extra {
		body[k] = v
	}
	code, m := e.do("POST", "/api/openbox/subscriptions", body)
	if code != 200 {
		t.Fatalf("create %s: %d %v", name, code, m)
	}
	return m["id"].(string)
}

func (e *env) list(t *testing.T) []map[string]any {
	t.Helper()
	code, m := e.do("GET", "/api/openbox/subscriptions", nil)
	if code != 200 {
		t.Fatal(code, m)
	}
	var out []map[string]any
	for _, s := range m["subscriptions"].([]any) {
		out = append(out, s.(map[string]any))
	}
	return out
}

func TestEmptyList(t *testing.T) {
	e := newEnv(t)
	code, m := e.do("GET", "/api/openbox/subscriptions", nil)
	if code != 200 {
		t.Fatal(code)
	}
	arr, ok := m["subscriptions"].([]any)
	if !ok || len(arr) != 0 {
		t.Fatalf("空列表必须是 [] 而不是 null: %v", m)
	}
}

func TestCreateListShape(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a.example/sub"] = links("HK-1", "US-1")
	code, m := e.do("POST", "/api/openbox/subscriptions", map[string]any{
		"name": " 机场A ", "url": "https://a.example/sub", "userAgent": "clash.meta", "renameOptions": map[string]any{"enabled": false},
		"autoUpdate": map[string]any{"enabled": true, "days": 1, "hour": 4},
		"nodeDns":    map[string]any{"url": "https://dns.example/dns-query", "bootstrap": "1.1.1.1"},
	})
	if code != 200 || m["nodeCount"] != float64(2) || m["name"] != "机场A" || m["changed"] != true {
		t.Fatal(code, m)
	}
	if sk, ok := m["skipped"].([]any); !ok || len(sk) != 0 {
		t.Fatalf("skipped 必须是 []: %v", m["skipped"])
	}
	l := e.list(t)
	if len(l) != 1 {
		t.Fatal(l)
	}
	s := l[0]
	for _, k := range []string{"id", "name", "url", "enabled", "format", "nodeCount", "createdAt", "updatedAt", "usage", "autoUpdate", "nodeDns", "userAgent"} {
		if _, ok := s[k]; !ok {
			t.Errorf("缺少 %s: %v", k, s)
		}
	}
	if s["enabled"] != true || s["url"] != "https://a.example/sub" || s["format"] != "links" || s["userAgent"] != "clash.meta" {
		t.Fatalf("%v", s)
	}
	if s["createdAt"].(float64) < 1e12 {
		t.Fatalf("createdAt 应是毫秒: %v", s["createdAt"])
	}
	if u := s["usage"].(map[string]any); u["total"] != float64(30) || u["expire"] != float64(1800000000) {
		t.Fatalf("%v", u)
	}
	// 节点不出现在列表里,也不泄露内容
	if _, has := s["nodes"]; has {
		t.Fatal("列表里不该有 nodes")
	}
	pool, _ := e.svc.PoolNodes(context.Background())
	if len(pool) != 2 || pool[0].Tag != "HK-1" {
		t.Fatalf("%+v", pool)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a.example/sub"] = links("A")
	e.pages["https://empty.example/"] = "ftp://x/y\n"
	cases := []struct {
		name string
		body any
		want int
	}{
		{"没有名字", map[string]any{"url": "https://a.example/sub"}, 400},
		{"名字空白", map[string]any{"name": "  ", "url": "https://a.example/sub"}, 400},
		{"没有来源", map[string]any{"name": "x"}, 400},
		{"地址不是 http", map[string]any{"name": "x", "url": "ftp://a.example/sub"}, 400},
		{"urls 类型错", map[string]any{"name": "x", "urls": "https://a"}, 400},
		{"autoUpdate 小时越界", map[string]any{"name": "x", "url": "https://a.example/sub", "autoUpdate": map[string]any{"enabled": true, "days": 1, "hour": 24}}, 400},
		{"autoUpdate 缺 enabled", map[string]any{"name": "x", "url": "https://a.example/sub", "autoUpdate": map[string]any{"days": 1, "hour": 1}}, 400},
		{"nodeDns 不是 https", map[string]any{"name": "x", "url": "https://a.example/sub", "nodeDns": map[string]any{"url": "http://1.1.1.1/dns"}}, 400},
		{"nodeDns 域名缺 bootstrap", map[string]any{"name": "x", "url": "https://a.example/sub", "nodeDns": map[string]any{"url": "https://dns.example/q"}}, 400},
		{"登录来源", map[string]any{"name": "x", "loginId": "abc"}, 501},
		{"订阅服务器 404", map[string]any{"name": "x", "url": "https://nope.example/"}, 502},
		{"内容里没有可用节点", map[string]any{"name": "x", "url": "https://empty.example/"}, 400},
		{"不是 JSON", "not json", 400},
		{"JSON 不是对象", "[1]", 400},
	}
	for _, c := range cases {
		code, m := e.do("POST", "/api/openbox/subscriptions", c.body)
		if code != c.want {
			t.Errorf("%s: 状态码 %d(想要 %d)%v", c.name, code, c.want, m)
		}
		if code >= 400 {
			if msg, _ := m["error"].(string); msg == "" {
				t.Errorf("%s: 错误响应缺少 error", c.name)
			}
		}
	}
	if n := len(e.list(t)); n != 0 {
		t.Fatalf("失败的新增不能留下记录: %d", n)
	}
}

func TestPasteContentMode(t *testing.T) {
	e := newEnv(t)
	enc := base64.StdEncoding.EncodeToString([]byte(links("X-1", "X-2")))
	id := e.create(t, "手贴", "", map[string]any{"url": nil, "content": enc})
	if e.calls.Load() != 0 {
		t.Fatal("粘贴内容不该联网")
	}
	s := e.list(t)[0]
	if s["content"] != enc || s["format"] != "base64" || s["nodeCount"] != float64(2) {
		t.Fatalf("%v", s)
	}
	// 刷新 = 重新解析保存的内容,不联网
	code, m := e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", "{}")
	if code != 200 || m["nodeCount"] != float64(2) || e.calls.Load() != 0 {
		t.Fatal(code, m, e.calls.Load())
	}
}

func TestMultiURLMerge(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a/1"] = links("A", "B")
	e.pages["https://a/2"] = links("B", "C") // B 重名
	code, m := e.do("POST", "/api/openbox/subscriptions", map[string]any{"name": "多", "urls": []string{"https://a/1", "https://a/2", "https://a/1"}, "renameOptions": map[string]any{"enabled": false}})
	if code != 200 || m["nodeCount"] != float64(4) {
		t.Fatal(code, m)
	}
	pool, _ := e.svc.PoolNodes(context.Background())
	var tags []string
	for _, n := range pool {
		tags = append(tags, n.Tag)
	}
	if strings.Join(tags, ",") != "A,B,B 2,C" {
		t.Fatalf("重名要加序号: %v", tags)
	}
	s := e.list(t)[0]
	if s["url"] != "https://a/1" || len(s["urls"].([]any)) != 2 {
		t.Fatalf("url 是第一条、urls 去重: %v", s)
	}
	// 第二条失败要指出是第几条
	e.pages["https://a/2"] = "garbage"
	code, m = e.do("POST", "/api/openbox/subscriptions/"+s["id"].(string)+"/refresh", "{}")
	if code != 400 || !strings.Contains(m["error"].(string), "第 2 条") {
		t.Fatal(code, m)
	}
}

func TestCrossSubscriptionDuplicateTags(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = links("HK")
	e.pages["https://b"] = links("HK")
	e.create(t, "A", "https://a", nil)
	e.create(t, "B", "https://b", nil)
	pool, _ := e.svc.PoolNodes(context.Background())
	if len(pool) != 2 || pool[0].Tag != "HK" || pool[1].Tag != "HK 2" || pool[1].OriginalTag != "HK" {
		t.Fatalf("跨订阅重名必须改名,内核出站不能重名: %+v", pool)
	}
	// 重新拉取 A 不能因为 B 的存在把自己改名
	id := e.list(t)[0]["id"].(string)
	if code, _ := e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", "{}"); code != 200 {
		t.Fatal(code)
	}
	pool, _ = e.svc.PoolNodes(context.Background())
	if pool[0].Tag != "HK" || pool[1].Tag != "HK 2" {
		t.Fatalf("%+v", pool)
	}
}

func TestRenameFilterAndPrefix(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = links("HK-1", "HK-2", "剩余流量 10G", "US-1")
	opts := map[string]any{
		"excludeKeywords": []string{"剩余流量"},
		"disabled":        []string{"US-1"},
		"overrides":       map[string]string{"HK-2": "香港 二号"},
		"usePrefix":       true,
		"enabled":         false,
		"regionDict":      []any{map[string]any{"code": "HK", "name": "香港", "keywords": []string{"HK"}}},
	}
	code, m := e.do("POST", "/api/openbox/subscriptions/preview", map[string]any{"url": "https://a", "name": "破晓", "renameOptions": opts})
	if code != 200 {
		t.Fatal(code, m)
	}
	nodes := m["nodes"].([]any)
	if len(nodes) != 2 || nodes[0].(map[string]any)["tag"] != "破晓 | HK-1" || nodes[1].(map[string]any)["tag"] != "破晓 | 香港 二号" {
		t.Fatalf("%v", nodes)
	}
	if ex := m["excluded"].([]any); len(ex) != 1 || ex[0].(map[string]any)["name"] != "剩余流量 10G" {
		t.Fatalf("%v", m["excluded"])
	}
	if d := m["disabled"].([]any); len(d) != 1 || d[0].(map[string]any)["name"] != "US-1" {
		t.Fatalf("%v", m["disabled"])
	}
	if pv := m["preview"].([]any); len(pv) != 2 || pv[1].(map[string]any)["originalTag"] != "HK-2" || pv[0].(map[string]any)["regionCode"] != "HK" {
		t.Fatalf("%v", m["preview"])
	}
	for _, k := range []string{"format", "skipped", "groups", "usage"} {
		if _, ok := m[k]; !ok {
			t.Errorf("预览缺少 %s", k)
		}
	}
	// 预览不落盘
	if len(e.list(t)) != 0 {
		t.Fatal("预览不该保存")
	}
	// 保存后,没认的改名规则键(regionDict)原样保留
	id := e.create(t, "破晓", "https://a", map[string]any{"renameOptions": opts})
	s := e.list(t)[0]
	if s["id"] != id || len(s["renameOptions"].(map[string]any)["regionDict"].([]any)) != 1 || s["nodeCount"] != float64(2) {
		t.Fatalf("%v", s)
	}
}

func TestEverythingFilteredIsError(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = links("广告")
	code, m := e.do("POST", "/api/openbox/subscriptions", map[string]any{"name": "x", "url": "https://a", "renameOptions": map[string]any{"excludeKeywords": []string{"广告"}}})
	if code != 400 || !strings.Contains(m["error"].(string), "没有剩下") {
		t.Fatal(code, m)
	}
}

func TestPatchRenameWithoutFetch(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = links("A")
	id := e.create(t, "旧名", "https://a", nil)
	before := e.calls.Load()
	e.fail = errors.New("机场挂了") // 只改名 / 开关 / 计划时,联不上机场也要能改
	code, m := e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{
		"name": "新名", "autoUpdate": map[string]any{"enabled": false, "days": 2, "hour": 3}, "nodeDns": nil,
	})
	if code != 200 || m["name"] != "新名" || m["changed"] != false || e.calls.Load() != before {
		t.Fatal(code, m, e.calls.Load(), before)
	}
	s := e.list(t)[0]
	if s["name"] != "新名" || s["autoUpdate"].(map[string]any)["days"] != float64(2) || s["nodeDns"] != nil {
		t.Fatalf("%v", s)
	}
	// 改地址要重新拉取,拉不下来就报错,原数据不动
	code, m = e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"url": "https://a2"})
	if code != 500 && code != 502 {
		t.Fatal(code, m)
	}
	if e.list(t)[0]["url"] != "https://a" {
		t.Fatal("失败的修改不能改掉原地址")
	}
	// 相同地址不触发重新拉取
	n := e.calls.Load()
	if code, _ = e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"url": "https://a"}); code != 200 || e.calls.Load() != n {
		t.Fatal(code, e.calls.Load(), n)
	}
}

func TestPatchEnabledAndRebuild(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = links("A", "B")
	id := e.create(t, "S", "https://a", nil)
	code, m := e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"enabled": false})
	if code != 200 || m["changed"] != true {
		t.Fatal(code, m)
	}
	if pool, _ := e.svc.PoolNodes(context.Background()); len(pool) != 0 {
		t.Fatal("停用的订阅不进节点池")
	}
	e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"enabled": true})
	// 改过滤规则:重新解析
	e.pages["https://a"] = links("A", "B", "C")
	code, m = e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"renameOptions": map[string]any{"enabled": false, "excludeKeywords": []string{"B"}}})
	if code != 200 || m["nodeCount"] != float64(2) || m["changed"] != true {
		t.Fatal(code, m)
	}
	// 名称前缀开着时,改订阅名要重算节点名
	e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"renameOptions": map[string]any{"enabled": false, "usePrefix": true}})
	e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"name": "N2"})
	if pool, _ := e.svc.PoolNodes(context.Background()); pool[0].Tag != "N2 | A" {
		t.Fatalf("%+v", pool)
	}
	if code, _ = e.do("PATCH", "/api/openbox/subscriptions/nope", map[string]any{"name": "x"}); code != 404 {
		t.Fatal(code)
	}
	if code, _ = e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"enabled": "yes"}); code != 400 {
		t.Fatal(code)
	}
}

func TestRefreshKeepsUsageWhenHeaderMissing(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = links("A")
	id := e.create(t, "S", "https://a", nil)
	e.pages["https://a"] = links("A", "NEW")
	code, m := e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", map[string]any{"renameOptions": map[string]any{"excludeKeywords": []string{"zzz"}}})
	if code != 200 || m["nodeCount"] != float64(2) || m["changed"] != true {
		t.Fatal(code, m)
	}
	if e.list(t)[0]["renameOptions"].(map[string]any)["excludeKeywords"] == nil {
		t.Fatal("刷新时传的 renameOptions 要保存")
	}
	// 没变化 → changed:false
	code, m = e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", "{}")
	if code != 200 || m["changed"] != false {
		t.Fatal(code, m)
	}
	if code, _ = e.do("POST", "/api/openbox/subscriptions/nope/refresh", "{}"); code != 404 {
		t.Fatal(code)
	}
}

func TestReorderAndDelete(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a", "b", "c"} {
		e.pages["https://"+n] = links(strings.ToUpper(n))
		e.create(t, n, "https://"+n, nil)
	}
	l := e.list(t)
	ids := []string{l[2]["id"].(string), l[0]["id"].(string), l[1]["id"].(string)}
	code, m := e.do("PUT", "/api/openbox/subscriptions/order", map[string]any{"ids": ids})
	if code != 200 || m["changed"] != true || m["ok"] != true {
		t.Fatal(code, m)
	}
	l = e.list(t)
	if l[0]["name"] != "c" || l[1]["name"] != "a" || l[2]["name"] != "b" {
		t.Fatalf("%v", l)
	}
	pool, _ := e.svc.PoolNodes(context.Background())
	if pool[0].Tag != "C" || pool[2].Tag != "B" {
		t.Fatalf("节点池要跟着订阅顺序: %+v", pool)
	}
	if _, m = e.do("PUT", "/api/openbox/subscriptions/order", map[string]any{"ids": ids}); m["changed"] != false {
		t.Fatal("顺序没变应返回 changed:false")
	}
	for _, bad := range [][]string{{ids[0]}, {ids[0], ids[0], ids[1]}, {ids[0], ids[1], "zzz"}} {
		if code, _ = e.do("PUT", "/api/openbox/subscriptions/order", map[string]any{"ids": bad}); code != 400 {
			t.Errorf("%v 应 400,得到 %d", bad, code)
		}
	}
	code, m = e.do("DELETE", "/api/openbox/subscriptions/"+ids[0], nil)
	if code != 200 || m["ok"] != true || m["changed"] != true {
		t.Fatal(code, m)
	}
	if len(e.list(t)) != 2 {
		t.Fatal("没删掉")
	}
	if pool, _ = e.svc.PoolNodes(context.Background()); len(pool) != 2 {
		t.Fatalf("节点也要删: %+v", pool)
	}
	if code, _ = e.do("DELETE", "/api/openbox/subscriptions/"+ids[0], nil); code != 404 {
		t.Fatal(code)
	}
}

func TestLoginAndMethods(t *testing.T) {
	e := newEnv(t)
	if code, m := e.do("POST", "/api/openbox/subscriptions/login", map[string]any{"url": "https://x", "username": "u", "password": "p"}); code != 501 || m["error"] == nil {
		t.Fatal(code, m)
	}
	if code, _ := e.do("GET", "/api/openbox/subscriptions/preview", nil); code != 405 {
		t.Fatal(code)
	}
	if code, _ := e.do("PUT", "/api/openbox/subscriptions", "{}"); code != 405 {
		t.Fatal(code)
	}
}

func TestPersistsAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	st, _ := store.Open(dir)
	fetch := func(context.Context, string, string) (*sub.Fetched, error) {
		return &sub.Fetched{Body: []byte(links("A"))}, nil
	}
	s1 := subs.New(st, fetch)
	if _, err := s1.Create(context.Background(), subs.Input{Name: ptr("n"), Source: &subs.Source{URL: "https://a"}}); err != nil {
		t.Fatal(err)
	}
	st2, _ := store.Open(dir)
	s2 := subs.New(st2, fetch)
	l, err := s2.List(context.Background())
	if err != nil || len(l) != 1 || l[0].Name != "n" {
		t.Fatalf("%+v %v", l, err)
	}
	if pool, _ := s2.PoolNodes(context.Background()); len(pool) != 1 {
		t.Fatal(pool)
	}
}

func ptr[T any](v T) *T { return &v }

func TestURLEntriesDisabledLinkIsNotFetched(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a/1"] = links("A", "B")
	e.pages["https://a/2"] = links("C")
	code, m := e.do("POST", "/api/openbox/subscriptions", map[string]any{"name": "开关", "urlEntries": []map[string]any{
		{"url": " https://a/1 ", "enabled": true}, {"url": "https://a/2", "enabled": false},
	}, "renameOptions": map[string]any{"enabled": false}})
	if code != 200 || m["nodeCount"] != float64(2) || e.calls.Load() != 1 {
		t.Fatalf("停用的链接不该拉取: %d %v calls=%d", code, m, e.calls.Load())
	}
	s := e.list(t)[0]
	entries, _ := s["urlEntries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["url"] != "https://a/1" || entries[1].(map[string]any)["enabled"] != false {
		t.Fatalf("urlEntries 要原样保留停用项: %v", s["urlEntries"])
	}
	if s["url"] != "https://a/1" || s["urls"] != nil {
		t.Fatalf("url / urls 只留启用的: %v %v", s["url"], s["urls"])
	}
	// 启用第二条 → 重新拉取并并入节点
	id := s["id"].(string)
	code, m = e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"urlEntries": []map[string]any{
		{"url": "https://a/1", "enabled": true}, {"url": "https://a/2", "enabled": true},
	}})
	if code != 200 || m["nodeCount"] != float64(3) {
		t.Fatalf("启用后要重新拉取: %d %v", code, m)
	}
}

func TestURLEntriesValidation(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]any{
		"全部停用":    map[string]any{"name": "x", "urlEntries": []map[string]any{{"url": "https://a/1", "enabled": false}}},
		"不是数组":    map[string]any{"name": "x", "urlEntries": "https://a/1"},
		"项不是对象":   map[string]any{"name": "x", "urlEntries": []string{"https://a/1"}},
		"enabled": map[string]any{"name": "x", "urlEntries": []map[string]any{{"url": "https://a/1", "enabled": "yes"}}},
		"地址非法":    map[string]any{"name": "x", "urlEntries": []map[string]any{{"url": "ftp://a/1", "enabled": true}}},
	} {
		if code, m := e.do("POST", "/api/openbox/subscriptions", body); code != 400 {
			t.Errorf("%s 应该 400: %d %v", name, code, m)
		}
	}
}

func TestLegacyURLsReadAsEnabledEntries(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a/1"] = links("A")
	e.create(t, "旧写法", "https://a/1", nil)
	entries, _ := e.list(t)[0]["urlEntries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["enabled"] != true {
		t.Fatalf("url 写法要按启用读成 urlEntries: %v", entries)
	}
}

func TestDirectSwitchesStoredAndReportConfigChange(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a/1"] = links("A")
	id := e.create(t, "直连", "https://a/1", nil)
	s := e.list(t)[0]
	if _, has := s["directSubscription"]; has {
		t.Fatalf("没设置时不写出(默认就是开): %v", s)
	}
	n := e.calls.Load()
	code, m := e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"directSubscription": false})
	if code != 200 || m["changed"] != true || e.calls.Load() != n {
		t.Fatalf("改直连开关要标记配置变了、且不重新拉取: %d %v", code, m)
	}
	s = e.list(t)[0]
	if s["directSubscription"] != false || s["directNodes"] != nil {
		t.Fatalf("只写出明确关掉的: %v", s)
	}
	// 同一个值再设一次 → 没变
	if _, m = e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"directSubscription": false}); m["changed"] != false {
		t.Fatalf("没变就不该标记: %v", m)
	}
	// 设回 true → 回到默认(不再写出)
	e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"directSubscription": true})
	if _, has := e.list(t)[0]["directSubscription"]; has {
		t.Fatal("设回开就清掉字段")
	}
	if code, _ = e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"directNodes": "no"}); code != 400 {
		t.Fatal("必须是布尔值")
	}
}

// 保存 / 刷新的返回:节点池变了才带 applied;内核在跑且有待重启 → ok=false、pendingRestart.pending=true;
// 没接判断、没变化、内核没跑 / 没有待重启 → 不带或 ok=true。
func TestAppliedInSaveAndRefresh(t *testing.T) {
	e := newEnv(t)
	pending, reasons := true, []string{"nodes"}
	mux := http.NewServeMux()
	Register(mux, Deps{Subs: e.svc, Pending: func(context.Context) (bool, []string) { return pending, reasons }})
	e.h = mux

	e.pages["https://a"] = links("A")
	body := map[string]any{"name": "S", "url": "https://a", "renameOptions": map[string]any{"enabled": false}}
	code, m := e.do("POST", "/api/openbox/subscriptions", body)
	if code != 200 || m["changed"] != true {
		t.Fatal(code, m)
	}
	ap, _ := m["applied"].(map[string]any)
	pr, _ := ap["pendingRestart"].(map[string]any)
	if ap == nil || ap["ok"] != false || pr["pending"] != true || len(pr["reasons"].([]any)) != 1 {
		t.Fatalf("节点变了且有待重启要带 applied.ok=false: %v", m)
	}
	id := m["id"].(string)

	// 没变化:不带 applied(面板说「节点没有变化」)
	code, m = e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", "{}")
	if code != 200 || m["changed"] != false || m["applied"] != nil {
		t.Fatal(code, m)
	}

	// 变了但内核没跑 / 没有待重启:ok=true,reasons 是数组而不是 null
	e.pages["https://a"] = links("A", "NEW")
	pending, reasons = false, nil
	code, m = e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", "{}")
	ap, _ = m["applied"].(map[string]any)
	pr, _ = ap["pendingRestart"].(map[string]any)
	if code != 200 || ap["ok"] != true || pr["pending"] != false || pr["reasons"] == nil {
		t.Fatalf("没有待重启要 ok=true、reasons 为空数组: %v", m)
	}

	// 没接判断:不带 applied
	e.h = func() http.Handler { mx := http.NewServeMux(); Register(mx, Deps{Subs: e.svc}); return mx }()
	e.pages["https://a"] = links("A", "NEW", "MORE")
	code, m = e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", "{}")
	if code != 200 || m["changed"] != true || m["applied"] != nil {
		t.Fatal(code, m)
	}
}

// ---- node-share:订阅预览里单个节点的分享链接 ----

func TestNodeShare(t *testing.T) {
	e := newEnv(t)
	// 两个重名节点(密码不同),再加一个 vless
	e.pages["https://a"] = "trojan://pw1@a.example.com:443?sni=a.example.com#Dup\n" +
		"trojan://pw2@b.example.com:443?sni=b.example.com#Dup\n" +
		"vless://11111111-2222-3333-4444-555555555555@c.example.com:443?security=tls&type=ws&path=%2Fws#Other\n"
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"url": "https://a", "name": "S", "renameOptions": map[string]any{"enabled": false}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	const path = "/api/openbox/subscriptions/node-share"

	// 不带 occurrence = 第一个重名节点;名字用 newTag
	code, m := e.do("POST", path, base(map[string]any{"originalTag": "Dup", "newTag": "我的 节点"}))
	if code != 200 || m["name"] != "我的 节点" {
		t.Fatalf("%d %v", code, m)
	}
	link, _ := m["link"].(string)
	if !strings.HasPrefix(link, "trojan://pw1@a.example.com:443?") || !strings.HasSuffix(link, "#%E6%88%91%E7%9A%84%20%E8%8A%82%E7%82%B9") {
		t.Fatalf("link = %s", link)
	}

	// occurrence = 1 取第二个
	code, m = e.do("POST", path, base(map[string]any{"originalTag": "Dup", "occurrence": 1}))
	link, _ = m["link"].(string)
	if code != 200 || !strings.HasPrefix(link, "trojan://pw2@b.example.com:443?") {
		t.Fatalf("%d %v", code, m)
	}

	// newTag 空:用服务端算出来的名字(关了改名规则时是原名,重名的第二个会被去重成「Dup 2」)
	if m["name"] != "Dup 2" {
		t.Fatalf("name = %v", m["name"])
	}
	code, m = e.do("POST", path, base(map[string]any{"originalTag": "Other", "renameOptions": map[string]any{"enabled": false, "usePrefix": true}}))
	if code != 200 || m["name"] != "S | Other" || !strings.HasSuffix(m["link"].(string), "#S%20%7C%20Other") {
		t.Fatalf("%d %v", code, m)
	}

	// 找不到 / occurrence 越界 → 404;被停用的节点不在预览里,同样 404
	if code, _ = e.do("POST", path, base(map[string]any{"originalTag": "Nope"})); code != 404 {
		t.Errorf("找不到节点应 404,得到 %d", code)
	}
	if code, _ = e.do("POST", path, base(map[string]any{"originalTag": "Dup", "occurrence": 2})); code != 404 {
		t.Errorf("occurrence 越界应 404,得到 %d", code)
	}
	if code, _ = e.do("POST", path, base(map[string]any{"originalTag": "Other", "renameOptions": map[string]any{"enabled": false, "disabled": []string{"Other"}}})); code != 404 {
		t.Errorf("停用的节点应 404,得到 %d", code)
	}
}

func TestNodeShareBadInput(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = links("A")
	const path = "/api/openbox/subscriptions/node-share"
	for name, body := range map[string]map[string]any{
		"no-source":       {"originalTag": "A"},
		"no-tag":          {"url": "https://a"},
		"empty-tag":       {"url": "https://a", "originalTag": "  "},
		"tag-not-string":  {"url": "https://a", "originalTag": 1},
		"newtag-not-str":  {"url": "https://a", "originalTag": "A", "newTag": 1},
		"occurrence-neg":  {"url": "https://a", "originalTag": "A", "occurrence": -1},
		"occurrence-frac": {"url": "https://a", "originalTag": "A", "occurrence": 0.5},
		"occurrence-str":  {"url": "https://a", "originalTag": "A", "occurrence": "0"},
	} {
		if code, m := e.do("POST", path, body); code != 400 || m["error"] == nil {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
	// 登录来源与预览一致:501
	if code, _ := e.do("POST", path, map[string]any{"loginId": "x", "originalTag": "A"}); code != 501 {
		t.Errorf("登录来源应 501,得到 %d", code)
	}
	if code, _ := e.do("GET", path, nil); code != 405 {
		t.Errorf("GET 应 405,得到 %d", code)
	}
	// 订阅拉取失败 → 502
	e.fail = &sub.FetchError{Msg: "连不上"}
	if code, _ := e.do("POST", path, map[string]any{"url": "https://a", "originalTag": "A"}); code != 502 {
		t.Errorf("拉取失败应 502,得到 %d", code)
	}
}

// 能解析成节点、但生成不出分享链接(sing-box 订阅里的 vless 出站没写 uuid):应是 400 并带原因,不是 500。
func TestNodeShareUnencodableIs400(t *testing.T) {
	e := newEnv(t)
	e.pages["https://a"] = `{"outbounds":[{"type":"vless","tag":"NoID","server":"v.example.com","server_port":443}]}`
	code, m := e.do("POST", "/api/openbox/subscriptions/node-share", map[string]any{"url": "https://a", "originalTag": "NoID", "renameOptions": map[string]any{"enabled": false}})
	if code != 400 || m["error"] == nil || !strings.Contains(m["error"].(string), "uuid") {
		t.Fatalf("%d %v", code, m)
	}
}
