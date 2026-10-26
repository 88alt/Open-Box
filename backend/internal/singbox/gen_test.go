package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// realCore 编译仓库里的 core/(配置生成都在核心里,假的没有意义);没有 Go 工具链时跳过。
func realCore(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "obcore")
		if err != nil {
			buildErr = err
			return
		}
		corePath = filepath.Join(dir, "openbox-core")
		cmd := exec.Command("go", "build", "-o", corePath, "./cmd/openbox-core")
		cmd.Dir = filepath.Join("..", "..", "..", "core")
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
	gen  *Generator
	subs *subs.Service
	prof *profile.Service
	st   *store.Store
}

func newEnv(t *testing.T, sys System) *env {
	t.Helper()
	cc := core.New(realCore(t))
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, subs: subs.New(st, nil)}
	e.prof = profile.New(st, profile.CoreAdapter{C: cc}, nil)
	groups := nodegroups.New(st, nodegroups.CoreAdapter{C: cc}, e.subs, nodegroups.ChainsFromProfile(e.prof))
	e.gen = NewGenerator(cc, st, e.prof, groups, e.subs, sys, Options{
		RulesetDir: "/data/rules", CacheFile: "/data/cache.db", CertPath: "/etc/c.crt", KeyPath: "/etc/c.key",
	})
	return e
}

func (e *env) addSub(t *testing.T, name, content string) string {
	t.Helper()
	nm := name
	if _, err := e.subs.Create(context.Background(), subs.Input{Name: &nm, Source: &subs.Source{Content: content}, Rename: map[string]any{"enabled": false}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := e.subs.List(context.Background())
	return recs[len(recs)-1].ID
}

func tagsOf(cfg map[string]any, key string) map[string]string {
	out := map[string]string{}
	list, _ := cfg[key].([]any)
	for _, o := range list {
		m := o.(map[string]any)
		out[m["tag"].(string)], _ = m["type"].(string)
	}
	return out
}

const twoNodes = "trojan://pw@hk.example.com:443#HK 01\ntrojan://pw@us.example.com:443#US 01\n"

func TestBuildDefaultsWithNodes(t *testing.T) {
	e := newEnv(t, System{})
	e.addSub(t, "A", twoNodes)
	b, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b.NodeCount != 2 {
		t.Fatalf("NodeCount=%d", b.NodeCount)
	}
	out := tagsOf(b.Config, "outbounds")
	want := map[string]string{"直连": "direct", "拒绝": "block", "自动选择": "urltest", "国内直连": "selector", "其他": "selector", "HK 01": "trojan", "US 01": "trojan"}
	for tag, typ := range want {
		if out[tag] != typ {
			t.Errorf("出站 %q 类型=%q,期望 %q(全部: %v)", tag, out[tag], typ, out)
		}
	}
	// 自动选择组的成员就是订阅里的节点
	for _, o := range b.Config["outbounds"].([]any) {
		m := o.(map[string]any)
		if m["tag"] == "自动选择" {
			mem := m["outbounds"].([]any)
			if len(mem) != 2 || mem[0] != "HK 01" || mem[1] != "US 01" {
				t.Fatalf("自动选择成员=%v", mem)
			}
		}
	}
	if raw, err := b.JSON(); err != nil || len(raw) < 100 {
		t.Fatalf("JSON() err=%v len=%d", err, len(raw))
	}
}

func TestBuildNoNodesStillValidShape(t *testing.T) {
	e := newEnv(t, System{})
	b, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := tagsOf(b.Config, "outbounds")
	if out["直连"] != "direct" || out["拒绝"] != "block" || b.NodeCount != 0 {
		t.Fatalf("out=%v", out)
	}
}

func TestDisabledSubscriptionNodesExcluded(t *testing.T) {
	e := newEnv(t, System{})
	id := e.addSub(t, "A", twoNodes)
	off := false
	if _, err := e.subs.Update(context.Background(), id, subs.Input{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	b, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out := tagsOf(b.Config, "outbounds"); out["HK 01"] != "" || b.NodeCount != 0 {
		t.Fatalf("停用订阅的节点不该进配置: %v", out)
	}
}

func TestChainProxyBecomesDetourOutbound(t *testing.T) {
	e := newEnv(t, System{})
	e.addSub(t, "A", twoNodes)
	if _, err := e.prof.Patch(context.Background(), map[string]any{"chainProxies": []any{
		map[string]any{"id": "c1", "enabled": true, "name": "链1", "link": "trojan://pw@chain.example.com:443#x", "upstream": "HK 01"},
		map[string]any{"id": "c2", "enabled": false, "name": "链停用", "link": "trojan://pw@c2.example.com:443#x", "upstream": "HK 01"},
		map[string]any{"id": "c3", "enabled": true, "name": "坏链接", "link": "not a link", "upstream": "HK 01"},
	}}); err != nil {
		t.Fatal(err)
	}
	b, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found map[string]any
	for _, o := range b.Config["outbounds"].([]any) {
		if m := o.(map[string]any); m["tag"] == "链1" {
			found = m
		}
	}
	if found == nil || found["detour"] != "HK 01" || found["server"] != "chain.example.com" {
		t.Fatalf("链式出站不对: %v", found)
	}
	out := tagsOf(b.Config, "outbounds")
	if out["链停用"] != "" || out["坏链接"] != "" {
		t.Fatalf("停用 / 解析不了的链式代理不该进配置: %v", out)
	}
}

func TestClashSecretStableAndRedacted(t *testing.T) {
	e := newEnv(t, System{})
	b1, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := e.gen.Build(context.Background())
	secret := func(b *Built) string {
		return b.Config["experimental"].(map[string]any)["clash_api"].(map[string]any)["secret"].(string)
	}
	if secret(b1) == "" || secret(b1) != secret(b2) || secret(b1) != b1.ProfileSecret {
		t.Fatalf("密钥应非空且重复生成不变: %q %q", secret(b1), secret(b2))
	}
	red := Redacted(b1.Config)
	if got := red["experimental"].(map[string]any)["clash_api"].(map[string]any)["secret"]; got != "***" {
		t.Fatalf("预览里的密钥没遮: %v", got)
	}
	if secret(b1) == "***" {
		t.Fatal("Redacted 不能改动原配置")
	}
}

type fakeCaller struct{ args map[string]any }

func (f *fakeCaller) CallInto(_ context.Context, method string, args []any, out any, _ ...core.CallOption) error {
	f.args = args[0].(map[string]any)
	return json.Unmarshal([]byte(`{"config":{"outbounds":[]},"dnsRuleOwners":{},"directHosts":{},"failover":[]}`), out)
}

func TestArgsPassedToCore(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state"))
	fc := &fakeCaller{}
	sub := subs.New(st, nil)
	prof := profile.New(st, nil, nil)
	gen := NewGenerator(fc, st, prof, fakeGroups{}, sub, System{
		Resolvers:    func() []string { return []string{"192.168.1.1"} },
		LocalSubnets: func() []string { return []string{"192.168.1.0/24"} },
	}, Options{Platform: "systemd", RulesetDir: "/r", CacheFile: "/c.db", CertPath: "/a.crt", KeyPath: "/a.key"})
	if _, err := gen.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := fc.args
	if a["platform"] != "systemd" || a["rulesetDir"] != "/r" || a["cacheFilePath"] != "/c.db" {
		t.Fatalf("args=%v", a)
	}
	if r := a["systemDns"].([]any); len(r) != 1 || r[0] != "192.168.1.1" {
		t.Fatalf("systemDns=%v", r)
	}
	if r := a["localSubnets"].([]any); len(r) != 1 || r[0] != "192.168.1.0/24" {
		t.Fatalf("localSubnets=%v", r)
	}
	if tls := a["tlsCert"].(map[string]any); tls["certPath"] != "/a.crt" || tls["keyPath"] != "/a.key" {
		t.Fatalf("tlsCert=%v", tls)
	}
	if p := a["profile"].(map[string]any); p["clashApiSecret"] == "" || p["clashApiSecret"] == nil {
		t.Fatal("profile 里要带 clashApiSecret")
	}
	for _, k := range []string{"nodes", "userGroups", "subscriptions", "chainNodes", "selections"} {
		if a[k] == nil {
			t.Errorf("缺少 %s", k)
		}
	}
}

type fakeGroups struct{}

func (fakeGroups) Groups(context.Context) ([]any, error) { return []any{}, nil }

func TestBuildWithoutCore(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state"))
	gen := NewGenerator(nil, st, profile.New(st, nil, nil), fakeGroups{}, subs.New(st, nil), System{}, Options{})
	if _, err := gen.Build(context.Background()); !errors.Is(err, ErrCoreUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
