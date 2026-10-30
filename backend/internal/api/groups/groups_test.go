package groups

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

var (
	buildOnce sync.Once
	corePath  string
	buildErr  error
)

// realCore 编译仓库里的 core/ 当真核心用(节点组的规范化和环检测都在核心里,假的没有意义)。
// 没有 Go 工具链或编译失败时跳过。
func realCore(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "obcore")
		if err != nil {
			buildErr = err
			return
		}
		corePath = filepath.Join(dir, "openbox-core")
		if runtime.GOOS == "windows" {
			corePath += ".exe" // Windows 上没有扩展名的文件运行不了
		}
		cmd := exec.Command("go", "build", "-o", corePath, "./cmd/openbox-core")
		cmd.Dir = filepath.Join("..", "..", "..", "..", "core")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = &buildFail{string(out), err}
		}
	})
	if buildErr != nil {
		t.Skipf("编译不了 core,跳过: %v", buildErr)
	}
	return corePath
}

type buildFail struct {
	out string
	err error
}

func (b *buildFail) Error() string { return b.err.Error() + ": " + b.out }

type env struct {
	h    http.Handler
	subs *subs.Service
	prof *profile.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	cc := core.New(realCore(t))
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{subs: subs.New(st, nil)}
	e.prof = profile.New(st, profile.CoreAdapter{C: cc}, nil)
	mux := http.NewServeMux()
	Register(mux, Deps{Groups: nodegroups.New(st, nodegroups.CoreAdapter{C: cc}, e.subs, nodegroups.ChainsFromProfile(e.prof))})
	e.h = mux
	return e
}

func (e *env) do(method, path string, body any) (int, map[string]any) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (e *env) addSub(t *testing.T, name string, tags ...string) string {
	t.Helper()
	var sb strings.Builder
	for _, n := range tags {
		sb.WriteString("trojan://pw@" + strings.ToLower(strings.ReplaceAll(n, " ", "")) + ".example.com:443#" + n + "\n")
	}
	nm := name
	res, err := e.subs.Create(context.Background(), subs.Input{
		Name: &nm, Source: &subs.Source{Content: sb.String()}, Rename: map[string]any{"enabled": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := e.subs.List(context.Background())
	_ = res
	return recs[len(recs)-1].ID
}

func list(m map[string]any, k string) []any { l, _ := m[k].([]any); return l }

func names(groups []any) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g.(map[string]any)["name"].(string))
	}
	return out
}

func TestGetFreshHasDefaults(t *testing.T) {
	e := newEnv(t)
	code, m := e.do("GET", "/api/openbox/groups", nil)
	if code != 200 {
		t.Fatalf("code=%d %v", code, m)
	}
	gs := list(m, "groups")
	hasKind := map[string]bool{}
	for _, g := range gs {
		if k, _ := g.(map[string]any)["kind"].(string); k != "" {
			hasKind[k] = true
		}
	}
	if !hasKind["direct"] || !hasKind["block"] || len(gs) < 3 {
		t.Fatalf("默认组不对: %v", names(gs))
	}
	if ty := list(m, "types"); len(ty) != 3 {
		t.Fatalf("types=%v", ty)
	}
	if len(list(m, "availableNodes")) != 0 || list(m, "availableNodes") == nil {
		t.Fatalf("availableNodes 应为空数组: %v", m["availableNodes"])
	}
	if list(m, "availableGroups") == nil || len(list(m, "availableGroups")) != len(gs) {
		t.Fatalf("availableGroups=%v", m["availableGroups"])
	}
}

func TestAvailableNodesCarrySubscriptionAndChain(t *testing.T) {
	e := newEnv(t)
	id := e.addSub(t, "机场A", "HK 01", "US 01")
	_, err := e.prof.Patch(context.Background(), map[string]any{"chainProxies": []any{
		map[string]any{"id": "c1", "enabled": true, "name": "链1", "link": "trojan://p@h.example.com:443#x", "upstream": "HK 01"},
		map[string]any{"id": "c2", "enabled": false, "name": "链停用", "link": "trojan://p@h2.example.com:443#y", "upstream": "HK 01"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, m := e.do("GET", "/api/openbox/groups", nil)
	nodes := list(m, "availableNodes")
	if len(nodes) != 3 {
		t.Fatalf("节点数=%d %v", len(nodes), nodes)
	}
	first := nodes[0].(map[string]any)
	if first["name"] != "HK 01" || first["subscription"] != "机场A" || first["subscriptionId"] != id {
		t.Fatalf("first=%v", first)
	}
	chain := nodes[2].(map[string]any)
	if chain["name"] != "链1" || chain["chain"] != true || chain["upstream"] != "HK 01" {
		t.Fatalf("chain=%v", chain)
	}
	subsList := list(m, "subscriptions")
	if len(subsList) != 1 || subsList[0].(map[string]any)["id"] != id {
		t.Fatalf("subscriptions=%v", subsList)
	}
}

func TestPutRoundTripAndPersist(t *testing.T) {
	e := newEnv(t)
	e.addSub(t, "A", "HK 01", "HK 02")
	_, cur := e.do("GET", "/api/openbox/groups", nil)
	groups := append(list(cur, "groups"), map[string]any{
		"id": "g-1", "name": "我的手动", "type": "selector", "mode": "static", "members": []any{"HK 01", "HK 02"},
	})
	code, res := e.do("PUT", "/api/openbox/groups", map[string]any{"groups": groups})
	if code != 200 || res["ok"] != true {
		t.Fatalf("code=%d %v", code, res)
	}
	if res["dropped"] == nil || len(list(res, "dropped")) != 0 {
		t.Fatalf("dropped 应为空数组: %v", res["dropped"])
	}
	if _, has := res["dangling"]; has {
		t.Fatalf("不该有 dangling: %v", res["dangling"])
	}
	_, again := e.do("GET", "/api/openbox/groups", nil)
	found := false
	for _, g := range list(again, "groups") {
		m := g.(map[string]any)
		if m["name"] == "我的手动" {
			found = true
			if len(m["members"].([]any)) != 2 {
				t.Fatalf("成员丢了: %v", m)
			}
		}
	}
	if !found {
		t.Fatal("保存后读不到新组")
	}
}

func TestPutValidation(t *testing.T) {
	e := newEnv(t)
	e.addSub(t, "A", "HK 01")
	mk := func(id, name string) map[string]any {
		return map[string]any{"id": id, "name": name, "type": "selector", "mode": "static", "members": []any{}}
	}
	cases := []struct {
		name   string
		body   any
		status int
	}{
		{"缺 groups", map[string]any{}, 400},
		{"组重名", map[string]any{"groups": []any{mk("a", "同名"), mk("b", "同名")}}, 400},
		{"与节点重名", map[string]any{"groups": []any{mk("a", "HK 01")}}, 400},
		{"元素不是对象", map[string]any{"groups": []any{"x"}}, 400},
		{"正常", map[string]any{"groups": []any{mk("a", "好")}}, 200},
	}
	for _, c := range cases {
		code, m := e.do("PUT", "/api/openbox/groups", c.body)
		if code != c.status {
			t.Errorf("%s: code=%d %v", c.name, code, m)
		}
		if code == 400 && m["error"] == nil {
			t.Errorf("%s: 400 要带 error", c.name)
		}
	}
	// 失败的保存不能改动已存的内容
	_, m := e.do("GET", "/api/openbox/groups", nil)
	if len(list(m, "groups")) < 3 {
		t.Fatalf("groups=%v", names(list(m, "groups")))
	}
}

// 名字留空时核心会补成「分组-N」(与上游一致),不报错。
func TestBlankNameGetsDefault(t *testing.T) {
	e := newEnv(t)
	code, res := e.do("PUT", "/api/openbox/groups", map[string]any{"groups": []any{
		map[string]any{"id": "a", "name": "   ", "type": "selector", "mode": "static", "members": []any{}},
	}})
	if code != 200 {
		t.Fatalf("%d %v", code, res)
	}
	found := false
	for _, n := range names(list(res, "groups")) {
		if n == "分组-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("groups=%v", names(list(res, "groups")))
	}
}

func TestRenameMigratesReferences(t *testing.T) {
	e := newEnv(t)
	e.addSub(t, "A", "HK 01")
	base := func(inner string) []any {
		return []any{
			map[string]any{"id": "inner", "name": inner, "type": "selector", "mode": "static", "members": []any{"HK 01"}},
			map[string]any{"id": "outer", "name": "外层", "type": "selector", "mode": "static", "members": []any{inner, "HK 01"}},
		}
	}
	if code, m := e.do("PUT", "/api/openbox/groups", map[string]any{"groups": base("旧名")}); code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	// 前端只改了内层的名字,外层成员里还是旧名
	g := base("旧名")
	g[0].(map[string]any)["name"] = "新名"
	code, res := e.do("PUT", "/api/openbox/groups", map[string]any{"groups": g})
	if code != 200 {
		t.Fatalf("%d %v", code, res)
	}
	rn := list(res, "renamed")
	if len(rn) != 1 || rn[0].(map[string]any)["from"] != "旧名" || rn[0].(map[string]any)["to"] != "新名" {
		t.Fatalf("renamed=%v", rn)
	}
	for _, x := range list(res, "groups") {
		m := x.(map[string]any)
		if m["name"] == "外层" {
			mem := m["members"].([]any)
			if len(mem) != 2 || mem[0] != "新名" || mem[1] != "HK 01" {
				t.Fatalf("引用没迁移: %v", mem)
			}
		}
	}
	if _, has := res["dangling"]; has {
		t.Fatalf("迁移之后不该悬空: %v", res["dangling"])
	}
}

func TestDanglingAndCycleReported(t *testing.T) {
	e := newEnv(t)
	e.addSub(t, "A", "HK 01")
	g := []any{
		map[string]any{"id": "a", "name": "甲", "type": "selector", "mode": "static", "members": []any{"乙", "不存在"}},
		map[string]any{"id": "b", "name": "乙", "type": "selector", "mode": "static", "members": []any{"甲", "HK 01"}},
	}
	code, res := e.do("PUT", "/api/openbox/groups", map[string]any{"groups": g})
	if code != 200 {
		t.Fatalf("%d %v", code, res)
	}
	dl := list(res, "dangling")
	if len(dl) != 1 || dl[0].(map[string]any)["name"] != "甲" {
		t.Fatalf("dangling=%v", dl)
	}
	if mem := dl[0].(map[string]any)["members"].([]any); len(mem) != 1 || mem[0] != "不存在" {
		t.Fatalf("dangling members=%v", mem)
	}
	dr := list(res, "dropped")
	if len(dr) != 2 {
		t.Fatalf("成环的两个组都应被丢弃: %v", dr)
	}
	for _, d := range dr {
		if d.(map[string]any)["reason"] != "cycle" {
			t.Fatalf("reason=%v", d)
		}
	}
}

func TestFailoverLaneMembersRenamed(t *testing.T) {
	e := newEnv(t)
	e.addSub(t, "A", "HK 01", "HK 02")
	g := []any{
		map[string]any{"id": "in", "name": "内", "type": "selector", "mode": "static", "members": []any{"HK 01"}},
		map[string]any{"id": "fo", "name": "故障转移", "type": "failover", "mode": "static", "lanes": []any{
			map[string]any{"id": "l1", "name": "主", "members": []any{"内"}},
			map[string]any{"id": "l2", "name": "备", "members": []any{"HK 02"}},
		}},
	}
	if code, m := e.do("PUT", "/api/openbox/groups", map[string]any{"groups": g}); code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	g[0].(map[string]any)["name"] = "内新"
	code, res := e.do("PUT", "/api/openbox/groups", map[string]any{"groups": g})
	if code != 200 {
		t.Fatalf("%d %v", code, res)
	}
	for _, x := range list(res, "groups") {
		m := x.(map[string]any)
		if m["name"] == "故障转移" {
			lane := list(m, "lanes")[0].(map[string]any)
			if mem := lane["members"].([]any); len(mem) != 1 || mem[0] != "内新" {
				t.Fatalf("页签成员没迁移: %v", mem)
			}
		}
	}
	if _, has := res["dangling"]; has {
		t.Fatalf("dangling=%v", res["dangling"])
	}
}

func TestCoreUnavailable(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state"))
	mux := http.NewServeMux()
	Register(mux, Deps{Groups: nodegroups.New(st, nil, subs.New(st, nil), nil)})
	for _, method := range []string{"GET", "PUT"} {
		req := httptest.NewRequest(method, "/api/openbox/groups", strings.NewReader(`{"groups":[]}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 503 {
			t.Errorf("%s code=%d %s", method, w.Code, w.Body.String())
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("DELETE", "/api/openbox/groups", nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d", code)
	}
}

// 改名识别出的地区名会进成员选择器(动态组的关键词也匹配它)。
func TestAvailableNodeRegionName(t *testing.T) {
	e := newEnv(t)
	nm := "A"
	if _, err := e.subs.Create(context.Background(), subs.Input{
		Name: &nm, Source: &subs.Source{Content: "trojan://pw@h.example.com:443#香港 IEPL 01\n"},
	}); err != nil {
		t.Fatal(err)
	}
	_, m := e.do("GET", "/api/openbox/groups", nil)
	nodes := list(m, "availableNodes")
	if len(nodes) != 1 {
		t.Fatalf("nodes=%v", nodes)
	}
	if r, _ := nodes[0].(map[string]any)["region"].(string); r == "" {
		t.Fatalf("没有地区名: %v", nodes[0])
	}
}
