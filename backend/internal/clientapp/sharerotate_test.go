package clientapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/shareservers"
)

func rotateServers() []any {
	return []any{
		map[string]any{"id": "a", "name": "A", "protocol": "hysteria2", "port": 7001, "password": "old-a", "obfs": "keep"},
		map[string]any{"id": "b", "name": "B", "protocol": "vless", "port": 7002, "uuid": "11111111-2222-3333-4444-555555555555"},
	}
}

// rotateEnv 在 shareSvc 上接好 UpdateServer(照 profile.Service.UpdateServer 的语义:副本交给 change、整条过校验、没有这台回 ErrServerNotFound)。
// 记下每次部署调用。
type rotateEnv struct {
	*shareEnv
	mu      sync.Mutex
	deploys int
}

func newRotateEnv(t *testing.T, running bool, deploy DeployResult) *rotateEnv {
	t.Helper()
	e := &rotateEnv{shareEnv: shareSvc(t)}
	*e.servers = rotateServers()
	e.s.UpdateServer = func(_ context.Context, id string, change func(map[string]any) error) (map[string]any, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		list := *e.servers
		for i, it := range list {
			m, _ := it.(map[string]any)
			if m["id"] != id {
				continue
			}
			c := map[string]any{}
			for k, v := range m {
				c[k] = v
			}
			if err := change(c); err != nil {
				return nil, err
			}
			if _, err := shareservers.Normalize([]any{c}); err != nil {
				return nil, &InputError{Msg: err.Error()}
			}
			nl := append([]any(nil), list...)
			nl[i] = c
			*e.servers = nl
			return c, nil
		}
		return nil, ErrServerNotFound
	}
	e.s.Running = func(context.Context) (bool, error) { return running, nil }
	e.s.Deploy = func(context.Context) DeployResult {
		e.mu.Lock()
		e.deploys++
		e.mu.Unlock()
		return deploy
	}
	return e
}

func (e *rotateEnv) server(id string) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, it := range *e.servers {
		if m := it.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func TestRotateShareEndToEnd(t *testing.T) {
	e := newRotateEnv(t, true, DeployResult{OK: true})
	ctx := context.Background()
	if _, err := e.s.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{"a": linkA, "b": linkB}
	first, err := e.s.ShareCodes(ctx, links)
	if err != nil {
		t.Fatal(err)
	}
	oldA, oldB := first["a"], first["b"]

	got, err := e.s.RotateShare(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Applied != AppliedRestarted || got.Message != "" || e.deploys != 1 {
		t.Fatalf("内核在跑、部署成功应是 restarted 且只部署一次: %+v 部署 %d 次", got, e.deploys)
	}
	if !validPairCode(got.PairCode) || got.PairCode == oldA.PairCode || got.Gen != "0000000000000002" {
		t.Fatalf("新配对码和加一的代数: %+v(旧 %+v)", got, oldA)
	}
	if got.Server["id"] != "a" || got.Server["password"] == "old-a" || got.Server["obfs"] != "keep" || got.Server["port"] != 7001 {
		t.Fatalf("返回换完密码的整条服务器: %+v", got.Server)
	}
	if e.server("a")["password"] != got.Server["password"] {
		t.Fatal("档案里的密码要和返回的一致")
	}
	if e.server("b")["uuid"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatal("别的服务器不该动")
	}

	// 旧代数立刻取不回;新代数在面板重新交链接之前也取不回(没有封包)
	if _, err := e.s.Share(ctx, "a", oldA.Gen, "10.0.0.1"); !errors.Is(err, ErrSyncNotFound) {
		t.Fatalf("旧代数应 404: %v", err)
	}
	if _, err := e.s.Share(ctx, "a", got.Gen, "10.0.0.1"); !errors.Is(err, ErrSyncNotFound) {
		t.Fatalf("还没重新封包,新代数也 404: %v", err)
	}
	// 面板拿新密码拼出新链接再交一次:新码封好,新配对码解得开,旧配对码解不开
	next, err := e.s.ShareCodes(ctx, map[string]string{"a": linkA + "&new=1", "b": linkB})
	if err != nil {
		t.Fatal(err)
	}
	if next["a"].PairCode != got.PairCode || next["a"].Gen != got.Gen {
		t.Fatalf("重新出码沿用换好的配对码和代数: %+v", next["a"])
	}
	if p := openShare(t, e.s, "a", next["a"]); p.Gen != got.Gen || !strings.HasSuffix(p.Link, "&new=1") {
		t.Fatalf("新封包明文: %+v", p)
	}
	if _, err := e.s.Share(ctx, "a", got.Gen, "10.0.0.1"); err != nil {
		t.Fatalf("新代数现在能取回: %v", err)
	}
	// b 的配对码、代数、封包都没动
	if next["b"] != oldB {
		t.Fatalf("b 不该变: %+v -> %+v", oldB, next["b"])
	}
}

func TestRotateShareGenKeepsCounting(t *testing.T) {
	e := newRotateEnv(t, false, DeployResult{})
	ctx := context.Background()
	// 从没出过码的服务器:按第一次的代数加一,不是从头
	r1, err := e.s.RotateShare(ctx, "a")
	if err != nil || r1.Gen != "0000000000000002" {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, err := e.s.RotateShare(ctx, "a")
	if err != nil || r2.Gen != "0000000000000003" || r2.PairCode == r1.PairCode {
		t.Fatalf("每次加一、配对码每次换新: %+v %v", r2, err)
	}
	var st state
	e.s.Store.Load(stateFile, &st)
	if sh := st.Shares["a"]; sh == nil || sh.PairCode != r2.PairCode || sh.Gen != r2.Gen || sh.Seal != nil || sh.LinkMAC != "" {
		t.Fatalf("状态里只留配对码和代数: %+v", sh)
	}
	if len(st.Shares) != 1 {
		t.Fatalf("别的服务器不建状态: %+v", st.Shares)
	}
}

func TestRotateShareApplied(t *testing.T) {
	cases := []struct {
		name        string
		running     bool
		runErr      error
		deploy      DeployResult
		noDeploy    bool // Service.Deploy = nil
		mutate      func(m map[string]any)
		target      string
		want        string
		wantMsg     string
		wantDeploys int
	}{
		{"内核没在运行", false, nil, DeployResult{OK: true}, false, nil, "a", AppliedSaved, "", 0},
		{"没有服务管理器", true, ErrUnavailable, DeployResult{OK: true}, false, nil, "a", AppliedSaved, "", 0},
		{"读不出运行状态", true, errors.New("procd 没响应"), DeployResult{OK: true}, false, nil, "a", AppliedFailed, "无法确认内核是否在运行", 0},
		{"部署失败", true, nil, DeployResult{Message: "sing-box check 没通过"}, false, nil, "a", AppliedFailed, "sing-box check 没通过", 1},
		{"部署失败没有原因", true, nil, DeployResult{}, false, nil, "a", AppliedFailed, "部署失败", 1},
		{"已有部署在进行", true, nil, DeployResult{Busy: true}, false, nil, "a", AppliedFailed, "已有一次部署在进行", 1},
		{"没接部署", true, nil, DeployResult{OK: true}, true, nil, "a", AppliedSaved, "", 0},
		{"服务器已停用", true, nil, DeployResult{OK: true}, false, func(m map[string]any) { m["enabled"] = false }, "a", AppliedSaved, "", 0},
		{"enabled 为 true", true, nil, DeployResult{OK: true}, false, func(m map[string]any) { m["enabled"] = true }, "a", AppliedRestarted, "", 1},
		{"vless 换 uuid", true, nil, DeployResult{OK: true}, false, nil, "b", AppliedRestarted, "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newRotateEnv(t, c.running, c.deploy)
			e.s.Running = func(context.Context) (bool, error) { return c.running, c.runErr }
			if c.noDeploy {
				e.s.Deploy = nil
			}
			if c.mutate != nil {
				c.mutate((*e.servers)[0].(map[string]any))
			}
			got, err := e.s.RotateShare(context.Background(), c.target)
			if err != nil {
				t.Fatal(err)
			}
			if got.Applied != c.want || !strings.Contains(got.Message, c.wantMsg) || e.deploys != c.wantDeploys {
				t.Fatalf("applied=%q message=%q 部署 %d 次,期望 %q / %q / %d", got.Applied, got.Message, e.deploys, c.want, c.wantMsg, c.wantDeploys)
			}
			if c.want == AppliedSaved && c.wantMsg != "" || (c.want != AppliedFailed && got.Message != "") {
				t.Fatalf("只有 failed 带 message: %+v", got)
			}
			// 不论部署结果如何,密码和配对码都已经换了(前端据 failed 提示)
			if !validPairCode(got.PairCode) || got.Gen != "0000000000000002" {
				t.Fatalf("配对码和代数都已换: %+v", got)
			}
			if got.Server["password"] == "old-a" && c.target == "a" {
				t.Fatal("密码都已经换了")
			}
		})
	}
}

// 没设认证的 SOCKS5 / HTTP 入站没有凭据可换:配对码照换,配置不动、不部署。
func TestRotateShareOpenMixed(t *testing.T) {
	e := newRotateEnv(t, true, DeployResult{OK: true})
	*e.servers = []any{map[string]any{"id": "m", "name": "M", "protocol": "mixed", "port": 7080}}
	got, err := e.s.RotateShare(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if got.Applied != AppliedSaved || e.deploys != 0 || got.Server["password"] != nil || got.Server["username"] != nil || got.Gen != "0000000000000002" {
		t.Fatalf("%+v 部署 %d 次", got, e.deploys)
	}
}

func TestRotateShareErrors(t *testing.T) {
	ctx := context.Background()
	e := newRotateEnv(t, true, DeployResult{OK: true})
	for _, id := range []string{"ghost", "", "bad id!", strings.Repeat("x", 65)} {
		if _, err := e.s.RotateShare(ctx, id); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("%q: 应是 ErrServerNotFound: %v", id, err)
		}
	}
	var st state
	e.s.Store.Load(stateFile, &st)
	if len(st.Shares) != 0 || e.deploys != 0 {
		t.Fatalf("找不到的服务器不建状态、不部署: %+v %d", st.Shares, e.deploys)
	}

	// 没接档案修改能力:503 的来源
	e2 := shareSvc(t)
	if _, err := e2.s.RotateShare(ctx, "a"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("UpdateServer 为 nil 应是 ErrUnavailable: %v", err)
	}

	// 档案那一侧失败:什么都不动,错误原样往上给
	boom := errors.New("档案写不进去")
	e3 := newRotateEnv(t, true, DeployResult{OK: true})
	first, _ := e3.s.ShareCodes(ctx, map[string]string{"a": linkA})
	e3.s.UpdateServer = func(context.Context, string, func(map[string]any) error) (map[string]any, error) { return nil, boom }
	if _, err := e3.s.RotateShare(ctx, "a"); !errors.Is(err, boom) {
		t.Fatalf("应原样返回: %v", err)
	}
	var st3 state
	e3.s.Store.Load(stateFile, &st3)
	if sh := st3.Shares["a"]; sh == nil || sh.PairCode != first["a"].PairCode || sh.Gen != first["a"].Gen || sh.Seal == nil || e3.deploys != 0 {
		t.Fatalf("档案没换成功,配对码、代数和封包都不该动、不部署: %+v", sh)
	}

	// 别的服务器不合格,整份写回被校验拦下:InputError,配对码不动
	e4 := newRotateEnv(t, true, DeployResult{OK: true})
	first4, _ := e4.s.ShareCodes(ctx, map[string]string{"a": linkA})
	e4.s.UpdateServer = func(context.Context, string, func(map[string]any) error) (map[string]any, error) {
		return nil, &InputError{Msg: "第 2 台服务器: 端口必须是 1–65535 的整数"}
	}
	var ie *InputError
	if _, err := e4.s.RotateShare(ctx, "a"); !errors.As(err, &ie) {
		t.Fatalf("应是 *InputError: %v", err)
	}
	var st4 state
	e4.s.Store.Load(stateFile, &st4)
	if st4.Shares["a"].PairCode != first4["a"].PairCode {
		t.Fatal("没换成功,配对码不该动")
	}
}

func TestRotateShareConcurrent(t *testing.T) {
	e := newRotateEnv(t, true, DeployResult{OK: true})
	ctx := context.Background()
	var wg sync.WaitGroup
	res := make([]ShareRotated, 6)
	for i := range res {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := []string{"a", "b"}[i%2]
			r, err := e.s.RotateShare(ctx, id)
			if err != nil {
				t.Error(err)
				return
			}
			res[i] = r
		}()
	}
	wg.Wait()
	var st state
	e.s.Store.Load(stateFile, &st)
	if st.Shares["a"].Gen != "0000000000000004" || st.Shares["b"].Gen != "0000000000000004" {
		t.Fatalf("每台各换了三次,代数应是 4: a=%v b=%v", st.Shares["a"], st.Shares["b"])
	}
	if e.deploys != 6 {
		t.Fatalf("每次都部署: %d", e.deploys)
	}
}

func TestNextGen(t *testing.T) {
	for in, want := range map[string]string{
		"0000000000000001": "0000000000000002",
		"00000000000000ff": "0000000000000100",
		"fffffffffffffffe": "ffffffffffffffff",
	} {
		if got, err := nextGen(in); err != nil || got != want {
			t.Errorf("%s -> %s %v,期望 %s", in, got, err, want)
		}
	}
	for _, in := range []string{"ffffffffffffffff", "", "1", "000000000000000G", "0000000000000001 "} {
		if _, err := nextGen(in); err == nil {
			t.Errorf("%q 应报错", in)
		}
	}
}
