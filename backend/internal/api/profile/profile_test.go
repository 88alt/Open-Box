package profile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

type stubCore struct{}

func (stubCore) MigrateRouting(context.Context, map[string]any) (map[string]any, bool, error) {
	return nil, false, nil
}

func (stubCore) NormalizeGroups(_ context.Context, g []any) ([]any, error) {
	return append([]any{map[string]any{"id": "builtin-direct"}}, g...), nil
}

func newHandler(t *testing.T, c profile.Core) http.Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Profile: profile.New(st, c, nil)})
	return mux
}

func call(h http.Handler, method, path, body string) (int, map[string]any) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestGetProfileIsFullyPopulated(t *testing.T) {
	h := newHandler(t, nil)
	code, body := call(h, "GET", "/api/openbox/profile", "")
	if code != 200 {
		t.Fatal(code, body)
	}
	p, ok := body["profile"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 profile: %v", body)
	}
	// 前端类型里非可选的字段必须每次都在
	for _, k := range []string{"region", "ipv6", "dns", "routing"} {
		if _, has := p[k]; !has {
			t.Errorf("profile 缺少 %s", k)
		}
	}
}

func TestPutMergesAndReturnsProfile(t *testing.T) {
	h := newHandler(t, nil)
	code, body := call(h, "PUT", "/api/openbox/profile", `{"ipv6":true,"dns":{"mode":"off"}}`)
	if code != 200 {
		t.Fatal(code, body)
	}
	p := body["profile"].(map[string]any)
	if p["ipv6"] != true || p["dns"].(map[string]any)["mode"] != "off" {
		t.Fatalf("PUT 没生效: %v", p)
	}
	_, again := call(h, "GET", "/api/openbox/profile", "")
	if again["profile"].(map[string]any)["ipv6"] != true {
		t.Fatal("PUT 后 GET 应读到新值")
	}
}

func TestPutErrors(t *testing.T) {
	h := newHandler(t, nil)
	cases := []struct {
		name, body string
		code       int
		errHas     string
	}{
		{"非 JSON", `not json`, 400, "JSON"},
		{"null 请求体", `null`, 400, "JSON 对象"},
		{"数组请求体", `[1]`, 400, "JSON"},
		{"取值非法", `{"tun":{"stack":"x"}}`, 400, "tun.stack"},
		{"过大", `{"x":"` + strings.Repeat("a", 2<<20) + `"}`, 413, "过大"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := call(h, "PUT", "/api/openbox/profile", c.body)
			if code != c.code {
				t.Fatalf("状态码 %d,期望 %d(%v)", code, c.code, body)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, c.errHas) {
				t.Fatalf("error = %q,应含 %q", msg, c.errHas)
			}
		})
	}
	// 失败的请求不改变已保存的内容
	_, body := call(h, "GET", "/api/openbox/profile", "")
	if body["profile"].(map[string]any)["tun"].(map[string]any)["stack"] != "mixed" {
		t.Fatal("失败的 PUT 不应留下改动")
	}
}

// shareRegions / serverInfo 的校验:400,原因是英文(前端原样显示),失败的请求不留改动;合法的整份覆盖。
func TestPutShareRegionsAndServerInfo(t *testing.T) {
	h := newHandler(t, nil)
	two := `[{"id":"a","name":"a","regions":["CN"],"rules":[],"catchAll":"direct"},` +
		`{"id":"x","name":"x","regions":["CN"],"rules":[],"catchAll":"direct"}]`
	for _, c := range []struct{ body, want string }{
		{`{"shareRegions":` + two + `}`, "region CN is in both a and x"},
		{`{"shareRegions":[{"id":"a","name":"a","regions":[],"rules":[{"type":"ipcidr","value":"nope","action":"direct"}],"catchAll":"direct"}]}`, "not a valid ipcidr value"},
		{`{"serverInfo":{"region":"cn"}}`, "region must be a two-letter uppercase country code"},
	} {
		code, body := call(h, "PUT", "/api/openbox/profile", c.body)
		if msg, _ := body["error"].(string); code != 400 || !strings.Contains(msg, c.want) {
			t.Fatalf("%s\n应是 400 且含 %q: %d %v", c.body, c.want, code, body)
		}
	}
	_, body := call(h, "GET", "/api/openbox/profile", "")
	if p := body["profile"].(map[string]any); p["shareRegions"] != nil || p["serverInfo"] != nil {
		t.Fatalf("失败的 PUT 不应留下改动: %v %v", p["shareRegions"], p["serverInfo"])
	}

	code, body := call(h, "PUT", "/api/openbox/profile",
		`{"shareRegions":[{"id":"jp","name":" 日本 ","regions":["JP"],"rules":[],"catchAll":"proxy"}],"serverInfo":{"name":"家里","icon":"JP"}}`)
	if code != 200 {
		t.Fatalf("合法的应保存: %d %v", code, body)
	}
	p := body["profile"].(map[string]any)
	if g := p["shareRegions"].([]any)[0].(map[string]any); g["name"] != "日本" || g["dns"] == nil {
		t.Fatalf("应返回规整后的值: %v", g)
	}
	// serverInfo 整份覆盖
	code, body = call(h, "PUT", "/api/openbox/profile", `{"serverInfo":{"name":"办公室"}}`)
	if si := body["profile"].(map[string]any)["serverInfo"].(map[string]any); code != 200 || len(si) != 1 || si["name"] != "办公室" {
		t.Fatalf("serverInfo 应整份覆盖: %d %v", code, body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHandler(t, nil)
	for _, p := range []string{"/api/openbox/profile", "/api/openbox/profile/defaults", "/api/openbox/defaults/routing", "/api/openbox/defaults/groups"} {
		code, _ := call(h, "DELETE", p, "")
		if code != 405 {
			t.Errorf("DELETE %s = %d,期望 405", p, code)
		}
	}
}

func TestRegionDefaults(t *testing.T) {
	h := newHandler(t, nil)
	for _, region := range []string{"cn", "intl", ""} {
		path := "/api/openbox/profile/defaults"
		if region != "" {
			path += "?region=" + region
		}
		code, body := call(h, "GET", path, "")
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, body)
		}
		d, ok := body["defaults"].(map[string]any)
		if !ok {
			t.Fatalf("%s 缺少 defaults", path)
		}
		want := region
		if want == "" {
			want = "cn"
		}
		if d["region"] != want || d["dns"] == nil || d["routing"] == nil {
			t.Errorf("%s: defaults = %v", path, d)
		}
		// fetchDnsRewriteDefaults 读 dnsRewriteDefaults,必须是数组而不是缺失
		if _, isList := body["dnsRewriteDefaults"].([]any); !isList {
			t.Errorf("%s: dnsRewriteDefaults 应为数组: %v", path, body["dnsRewriteDefaults"])
		}
	}
	if code, body := call(h, "GET", "/api/openbox/profile/defaults?region=us", ""); code != 400 {
		t.Fatalf("非法 region 应 400: %d %v", code, body)
	}
}

func TestDefaultRouting(t *testing.T) {
	h := newHandler(t, nil)
	code, body := call(h, "GET", "/api/openbox/defaults/routing", "")
	if code != 200 {
		t.Fatal(code)
	}
	r := body["routing"].(map[string]any)
	if pol, _ := r["policies"].([]any); len(pol) == 0 {
		t.Fatalf("默认分流应有站点集: %v", r)
	}
}

func TestDefaultGroups(t *testing.T) {
	code, body := call(newHandler(t, nil), "GET", "/api/openbox/defaults/groups", "")
	if code != 503 || !strings.Contains(body["error"].(string), "openbox-core") {
		t.Fatalf("无核心应 503 并提示: %d %v", code, body)
	}
	code, body = call(newHandler(t, stubCore{}), "GET", "/api/openbox/defaults/groups", "")
	if code != 200 {
		t.Fatal(code, body)
	}
	if g, _ := body["groups"].([]any); len(g) != 2 {
		t.Fatalf("groups = %v", body["groups"])
	}
}

// 集成:编译真实 openbox-core,验证默认节点组和旧版分流迁移。
// 没有 go 工具链或编译失败时跳过(云端 Actions 里会跑)。
func TestWithRealCore(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过集成测试")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("没有 go 工具链")
	}
	coreDir, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "core"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(coreDir, "go.mod")); err != nil {
		t.Skip("找不到 core/ 目录")
	}
	bin := filepath.Join(t.TempDir(), "openbox-core")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/openbox-core")
	build.Dir = coreDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("编译 core 失败,跳过: %v\n%s", err, out)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	// 一份改版前的旧档案:有地区层
	legacy := map[string]any{"routing": map[string]any{
		"regions":  []any{map[string]any{"id": "r1", "name": "地区一", "rules": []any{}}},
		"regionId": "r1",
	}}
	if err := st.Save("profile", legacy); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Profile: profile.New(st, profile.CoreAdapter{C: core.New(bin)}, nil)})

	code, body := call(mux, "GET", "/api/openbox/defaults/groups", "")
	if code != 200 {
		t.Fatal(code, body)
	}
	ids := map[string]bool{}
	for _, g := range body["groups"].([]any) {
		ids[g.(map[string]any)["id"].(string)] = true
	}
	for _, id := range []string{"builtin-direct", "builtin-block", "auto"} {
		if !ids[id] {
			t.Errorf("默认节点组缺少 %s: %v", id, ids)
		}
	}

	code, body = call(mux, "GET", "/api/openbox/profile", "")
	if code != 200 {
		t.Fatal(code, body)
	}
	routing := body["profile"].(map[string]any)["routing"].(map[string]any)
	if _, has := routing["regions"]; has {
		t.Fatalf("真实核心迁移后不应再有 regions: %v", routing)
	}
	var disk map[string]any
	if _, err := st.Load("profile", &disk); err != nil {
		t.Fatal(err)
	}
	if _, has := disk["routing"].(map[string]any)["regions"]; has {
		t.Fatal("迁移结果应写回档案")
	}
}
