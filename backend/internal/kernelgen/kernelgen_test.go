package kernelgen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/mihomo"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// memFS 是最小的内存文件系统(mihomo.FS),测试里不碰真磁盘。
type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMemFS() *memFS { return &memFS{files: map[string][]byte{}} }

func (f *memFS) Exists(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[p]
	return ok
}

func (f *memFS) WriteFile(p string, data []byte, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[p] = append([]byte(nil), data...)
	return nil
}

func (f *memFS) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}

func (f *memFS) MkdirAll(string) error { return nil }

func (f *memFS) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, p)
	return nil
}

type stubCall map[string]string

func (s stubCall) call(_ context.Context, method string, _ ...any) (json.RawMessage, error) {
	v, ok := s[method]
	if !ok {
		return nil, errors.New("测试桩没有这个方法: " + method)
	}
	return json.RawMessage(v), nil
}

type fakeProfiles struct{ p map[string]any }

func (f fakeProfiles) Get(context.Context) (map[string]any, error) { return f.p, nil }

type fakeGroups struct{ g []any }

func (f fakeGroups) Groups(context.Context) ([]any, error) { return f.g, nil }

type fakeCatalog struct{}

func (fakeCatalog) Catalog(context.Context) ([]subs.Record, []subs.CatalogNode, error) {
	return []subs.Record{}, []subs.CatalogNode{}, nil
}

type fakeLists struct {
	res   map[string]*rulelist.Result
	errOn map[string]error
}

func (f fakeLists) Load(_ context.Context, rawURL string) (*rulelist.Result, error) {
	if err := f.errOn[rawURL]; err != nil {
		return nil, err
	}
	if r, ok := f.res[rawURL]; ok {
		return r, nil
	}
	return &rulelist.Result{}, nil
}

// minimalDeps 是一份能跑通装配的最小依赖:没有规则集链接、没有订阅节点。
func minimalDeps(t *testing.T, fs *memFS, lists ListLoader) Deps {
	t.Helper()
	stub := stubCall{
		"builtinTags":        `{"direct":"direct","block":"block","directEnabled":true,"blockEnabled":true}`,
		"normalizeRouting":   `{"custom":{"enabled":false,"rules":[]},"policies":[],"activePolicies":[],"fallback":{"name":"其他","default":"proxy"}}`,
		"mihomoDnsUpstreams": `{"direct":{"protocol":"udp","server":"223.5.5.5","port":53},"proxy":{"protocol":"tcp","server":"1.1.1.1","port":53},"directExtras":[],"proxyExtras":[]}`,
		"mihomoGroups": `{"proxies":[{"name":"n1","type":"ss","server":"1.2.3.4","port":8388}],` +
			`"groups":[{"name":"PROXY","type":"select","proxies":["n1","DIRECT"]}],` +
			`"first":{"PROXY":"n1"},"targets":{"direct":"DIRECT","block":"REJECT","PROXY":"PROXY","n1":"n1"},` +
			`"builtins":{"direct":"direct","block":"block"},"failover":[]}`,
		"mihomoTargetRules": `{"rules":["DOMAIN-SUFFIX,example.com,PROXY","MATCH,PROXY"],"firstTerminal":0,"endTerminal":0,"directTerminal":0,"firstPolicy":0}`,
	}
	return Deps{
		Profiles: fakeProfiles{p: map[string]any{
			"ipv6": false, "directForNodes": false,
			"tun":     map[string]any{"stack": "mixed", "autoRedirect": false},
			"dns":     map[string]any{"mode": "hijack", "split": false},
			"servers": []any{}, "clientRoutes": []any{},
		}},
		Groups:   fakeGroups{},
		Catalog:  fakeCatalog{},
		Lists:    lists,
		Secret:   func() (string, error) { return "s3cret", nil },
		Compiler: mihomo.NewCompiler(stub.call),

		Platform:   "linux",
		DataDir:    "/d",
		GeoDir:     "/geo",
		RulesetDir: "/d/rules",
		ConfigDir:  "/etc",
		FS:         fs,
	}
}

func TestMihomoBuildMinimal(t *testing.T) {
	fs := newMemFS()
	g := New(minimalDeps(t, fs, fakeLists{}))
	res, err := g.MihomoBuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, has := res.Config.Get("proxy-groups"); !has {
		t.Fatal("缺少 proxy-groups")
	}
	if v, _ := res.Config.Get("secret"); v != "s3cret" {
		t.Errorf("secret = %v", v)
	}
	rules, _ := res.Config.Get("rules")
	if list, _ := rules.([]any); len(list) == 0 {
		t.Error("rules 不应为空")
	}
}

func TestMihomoBuildNeedsCompiler(t *testing.T) {
	d := minimalDeps(t, newMemFS(), fakeLists{})
	d.Compiler = nil
	if _, err := New(d).MihomoBuild(context.Background()); err == nil {
		t.Fatal("没有 core 编译时应当报错")
	}
}

// 规则集链接拉不到、磁盘上也没有旧文件时,装配要如实报错并列上 tag。
func TestMihomoBuildRuleListFailure(t *testing.T) {
	fs := newMemFS()
	url := "https://example.com/list.txt"
	d := minimalDeps(t, fs, fakeLists{errOn: map[string]error{url: errors.New("连接超时")}})
	d.Profiles = fakeProfiles{p: map[string]any{
		"ipv6": false,
		"tun":  map[string]any{"autoRedirect": false},
		"dns":  map[string]any{"mode": "hijack"},
		"routing": map[string]any{"custom": map[string]any{
			"enabled": true, "rules": []any{map[string]any{"type": "ruleUrl", "value": url}},
		}},
		"servers": []any{}, "clientRoutes": []any{},
	}}
	_, err := New(d).MihomoBuild(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list-") {
		t.Fatalf("err = %v,想要包含规则集名", err)
	}
}

// 规则集链接正常时,形状要进 BuildInput(这里通过 providers 的落盘文件间接看:domain 名单会写 provider)。
func TestMihomoBuildRuleListShape(t *testing.T) {
	fs := newMemFS()
	url := "https://example.com/list.txt"
	d := minimalDeps(t, fs, fakeLists{res: map[string]*rulelist.Result{url: {}}})
	prof := d.Profiles.(fakeProfiles)
	prof.p["routing"] = map[string]any{
		"policies": []any{map[string]any{"name": "p", "default": "proxy", "ruleUrls": []any{url}}},
		"custom":   map[string]any{"enabled": false, "rules": []any{}},
	}
	d.Profiles = prof
	// 没有真 .srs 时转换会失败,但错误必须来自「mihomo 不支持规则集 / .srs 不在」,而不是形状没登记。
	_, err := New(d).MihomoBuild(context.Background())
	if err == nil {
		t.Skip("有可用的 .srs 时转换成功")
	}
	if strings.Contains(err.Error(), "规则集链接尚未准备好") {
		t.Fatalf("形状已登记,不该报没准备好: %v", err)
	}
}

func TestSingboxCheckNeedsGenerator(t *testing.T) {
	g := New(Deps{FS: newMemFS(), DataDir: t.TempDir()})
	if _, err := g.SingboxBuild(context.Background(), nil); err == nil {
		t.Fatal("没有生成器时应当报错")
	}
	if _, err := g.SingboxRulesets(nil); err == nil {
		t.Fatal("没有配置时应当报错")
	}
	if _, err := g.SingboxValidate(context.Background(), nil); err == nil {
		t.Fatal("没有配置时应当报错")
	}
}

// 入口 nft 为空时防火墙一项不适用(容器 / 纯 TUN 场景)。
func TestMihomoFirewallNotApplicable(t *testing.T) {
	g := New(Deps{FS: newMemFS(), DataDir: t.TempDir()})
	if _, ok, err := g.MihomoFirewall(context.Background(), &mihomo.BuildResult{}); ok || err != nil {
		t.Fatalf("ok=%v err=%v,想要不适用", ok, err)
	}
}

func TestChainNodesSkipsDisabled(t *testing.T) {
	got := chainNodes(map[string]any{"chainProxies": []any{
		map[string]any{"enabled": false, "name": "a", "link": "ss://YWVzLTEyOC1nY206cGFzcw@1.2.3.4:8388", "upstream": "PROXY"},
		map[string]any{"enabled": true, "name": "", "link": "ss://x", "upstream": "PROXY"},
	}})
	if len(got) != 0 {
		t.Fatalf("应当都跳过,得到 %d 条", len(got))
	}
}

func TestListTagMatchesCore(t *testing.T) {
	dir := t.TempDir()
	if tag := listTag(" https://example.com/a.txt "); !strings.HasPrefix(tag, "list-") || len(tag) != len("list-")+8 {
		t.Fatalf("tag = %q", tag)
	}
	// 规则集目录与规则库目录分开:geo 名走规则库,其余走 rules。
	p := mihomo.RuleSetPathFunc(filepath.Join(dir, "geo"), filepath.Join(dir, "rules"))
	a, err := p("geosite-cn")
	if err != nil || !strings.HasPrefix(a, filepath.Join(dir, "geo")) {
		t.Fatalf("geo 路径 = %s, err=%v", a, err)
	}
	b, err := p("list-1234abcd")
	if err != nil || !strings.HasPrefix(b, filepath.Join(dir, "rules")) {
		t.Fatalf("用户规则集路径 = %s, err=%v", b, err)
	}
}

// groupFirst 取装配结果里某个组的第一个成员(选中项)。
func groupFirst(t *testing.T, res *mihomo.BuildResult, name string) string {
	t.Helper()
	raw, _ := res.Config.Get("proxy-groups")
	list, _ := raw.([]any)
	for _, it := range list {
		g, _ := it.(map[string]any)
		if tag, _ := g["name"].(string); tag != name {
			continue
		}
		members, _ := g["proxies"].([]any)
		if len(members) == 0 {
			return ""
		}
		s, _ := members[0].(string)
		return s
	}
	return ""
}

// 面板选中的节点要套到组上,并在结果的 Selections 里回给调用方(切换成功后存快照用)。
func TestMihomoBuildAppliesSelections(t *testing.T) {
	d := minimalDeps(t, newMemFS(), fakeLists{})
	d.Selections = func(context.Context) map[string]any { return map[string]any{"PROXY": "DIRECT"} }
	res, err := New(d).MihomoBuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := groupFirst(t, res, "PROXY"); got != "DIRECT" {
		t.Errorf("PROXY 的第一项 = %q,想要 DIRECT", got)
	}
	if got := res.SelectionsAny()["PROXY"]; got != "DIRECT" {
		t.Errorf("结果里的选中项 = %v,想要 DIRECT", got)
	}
}

// 正在运行的内核里的实时选择优先;它是 DIRECT / REJECT 时要换成我们自己配置里的内置组名字。
func TestMihomoBuildPrefersLiveSelections(t *testing.T) {
	d := minimalDeps(t, newMemFS(), fakeLists{})
	d.Selections = func(context.Context) map[string]any { return map[string]any{"PROXY": "n1"} }
	d.LiveSelections = func(context.Context) (map[string]string, error) {
		return map[string]string{"PROXY": "DIRECT"}, nil
	}
	res, err := New(d).MihomoBuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := groupFirst(t, res, "PROXY"); got != "DIRECT" {
		t.Errorf("PROXY 的第一项 = %q,想要 DIRECT(实时选择优先于快照里的 n1)", got)
	}
}

// 实时读不到(内核没在跑 / 不是 Mihomo)就用快照,快照也没有就按默认第一项。
func TestMihomoBuildSelectionsFallback(t *testing.T) {
	d := minimalDeps(t, newMemFS(), fakeLists{})
	d.LiveSelections = func(context.Context) (map[string]string, error) { return nil, errors.New("连不上") }
	d.Selections = func(context.Context) map[string]any { return map[string]any{"PROXY": "DIRECT"} }
	res, err := New(d).MihomoBuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := groupFirst(t, res, "PROXY"); got != "DIRECT" {
		t.Errorf("PROXY 的第一项 = %q,想要快照里的 DIRECT", got)
	}

	d.Selections = nil
	res, err = New(d).MihomoBuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := groupFirst(t, res, "PROXY"); got != "n1" {
		t.Errorf("没有选中项时 PROXY 的第一项 = %q,想要默认的 n1", got)
	}
}

// 白名单端口要按局域网口写入口 nft;没给接口时如实报错(第 4、5 步的 kernelgen 把 system.LANDeviceNames 接进来)。
func TestMihomoBuildNeedsLanIfaces(t *testing.T) {
	d := minimalDeps(t, newMemFS(), fakeLists{})
	prof := d.Profiles.(fakeProfiles)
	prof.p["bypassPortsMode"] = "whitelist"
	prof.p["bypassPortsWhitelist"] = "80,443"
	d.Profiles = prof
	if _, err := New(d).MihomoBuild(context.Background()); err == nil || !strings.Contains(err.Error(), "局域网接口") {
		t.Fatalf("err = %v,想要「找不到局域网接口」", err)
	}
	d.LANIfaces = func() ([]string, error) { return []string{"br-lan"}, nil }
	if _, err := New(d).MihomoBuild(context.Background()); err != nil {
		t.Fatalf("给了局域网口仍然出错: %v", err)
	}
}
