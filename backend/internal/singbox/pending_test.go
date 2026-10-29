package singbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// ---------- 纯函数:指纹 ----------

func baseInput() FingerprintInput {
	return FingerprintInput{
		Profile: map[string]any{
			"region":  "cn",
			"ipv6":    false,
			"dns":     map[string]any{"region": "cn", "mode": "dnsmasq", "filter": map[string]any{"enabled": false, "lists": []any{}}},
			"routing": map[string]any{"siteSets": []any{map[string]any{"id": "a"}}},
			"servers": []any{map[string]any{"id": "s1", "port": float64(8443)}},
			"tun":     map[string]any{"stack": "mixed"},
			"updates": map[string]any{"subscriptions": "weekly"},
			"traffic": map[string]any{"keepMonths": float64(12)},
		},
		Groups: []any{map[string]any{"id": "g1", "name": "自动选择"}},
		Subs: []subs.Record{
			{ID: "s1", Name: "A", URL: "https://a.example/sub", Enabled: true, NodeCount: 2, UpdatedAt: 1},
			{ID: "s2", Name: "B", URL: "https://b.example/sub", Enabled: false},
		},
		Nodes: []subs.CatalogNode{
			{SubscriptionID: "s1", SubscriptionName: "A", Node: sub.Node{Tag: "HK 01", Type: "trojan", Server: "hk.example.com", ServerPort: 443}},
			{SubscriptionID: "s1", SubscriptionName: "A", Node: sub.Node{Tag: "US 01", Type: "trojan", Server: "us.example.com", ServerPort: 443}},
		},
	}
}

// changed 返回两份指纹里不一样的原因(按固定顺序)。
func changed(a, b Fingerprint) []string {
	var out []string
	for _, r := range reasonOrder {
		if a[r] != b[r] {
			out = append(out, r)
		}
	}
	return out
}

func TestFingerprintStableAndComplete(t *testing.T) {
	a, b := ComputeFingerprint(baseInput()), ComputeFingerprint(baseInput())
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("同样的输入指纹必须一样: %v %v", a, b)
	}
	if len(a) != len(reasonOrder) {
		t.Fatalf("每个原因都要有指纹: %v", a)
	}
	seen := map[string]bool{}
	for _, r := range reasonOrder {
		if a[r] == "" || seen[a[r]] {
			t.Fatalf("%s 的指纹为空或与别的原因撞了: %v", r, a)
		}
		seen[a[r]] = true
	}
}

func TestFingerprintReasonsAreIsolated(t *testing.T) {
	base := ComputeFingerprint(baseInput())
	cases := []struct {
		name string
		mut  func(in *FingerprintInput)
		want []string
	}{
		{"节点组", func(in *FingerprintInput) { in.Groups = []any{map[string]any{"id": "g1", "name": "改名"}} }, []string{ReasonNodes}},
		{"链式代理", func(in *FingerprintInput) {
			in.Profile["chainProxies"] = []any{map[string]any{"name": "c1", "link": "trojan://x@y:1#c", "upstream": "HK 01"}}
		}, []string{ReasonNodes}},
		{"测速地址", func(in *FingerprintInput) { in.Profile["testUrl"] = "https://example.com/204" }, []string{ReasonNodes}},
		{"多一个节点", func(in *FingerprintInput) {
			in.Nodes = append(in.Nodes, subs.CatalogNode{SubscriptionID: "s1", Node: sub.Node{Tag: "JP 01", Type: "trojan"}})
		}, []string{ReasonNodes}},
		{"节点内容", func(in *FingerprintInput) { in.Nodes[0].Node.ServerPort = 8443 }, []string{ReasonNodes}},
		{"订阅启停", func(in *FingerprintInput) { in.Subs[0].Enabled = false }, []string{ReasonNodes}},
		{"订阅地址", func(in *FingerprintInput) { in.Subs[0].URL = "https://a.example/other" }, []string{ReasonNodes}},
		{"节点 DNS", func(in *FingerprintInput) { in.Subs[0].NodeDNS = map[string]any{"server": "1.1.1.1"} }, []string{ReasonNodes}},
		{"DNS 设置", func(in *FingerprintInput) { in.Profile["dns"].(map[string]any)["mode"] = "hijack" }, []string{ReasonDNS}},
		{"DNS 地区", func(in *FingerprintInput) { in.Profile["region"] = "intl" }, []string{ReasonDNS}},
		{"IPv6", func(in *FingerprintInput) { in.Profile["ipv6"] = true }, []string{ReasonDNS}},
		{"过滤按当前设置应用了", func(in *FingerprintInput) { in.FilterKey = "abc123" }, []string{ReasonDNS}},
		{"分流规则", func(in *FingerprintInput) { in.Profile["routing"] = map[string]any{"siteSets": []any{}} }, []string{ReasonRoute}},
		{"屏蔽 QUIC", func(in *FingerprintInput) { in.Profile["rejectQuic"] = true }, []string{ReasonRoute}},
		{"终端分流", func(in *FingerprintInput) { in.Profile["clientRoutes"] = []any{map[string]any{"id": "c1"}} }, []string{ReasonRoute}},
		{"共享网络服务器", func(in *FingerprintInput) { in.Profile["servers"].([]any)[0].(map[string]any)["port"] = float64(9443) }, []string{ReasonInbounds}},
		{"tun 参数", func(in *FingerprintInput) { in.Profile["tun"] = map[string]any{"stack": "gvisor"} }, []string{ReasonInbounds}},
		{"没见过的档案键", func(in *FingerprintInput) { in.Profile["someFutureKey"] = true }, []string{ReasonSystem}},
		{"规则集目录", func(in *FingerprintInput) { in.Profile["rulesetDir"] = "/x" }, []string{ReasonSystem}},
		{"同时改两处", func(in *FingerprintInput) {
			in.Profile["ipv6"] = true
			in.Profile["tun"] = map[string]any{"stack": "system"}
		}, []string{ReasonDNS, ReasonInbounds}},
	}
	for _, c := range cases {
		in := baseInput()
		c.mut(&in)
		if got := changed(base, ComputeFingerprint(in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: 变的原因 = %v,期望 %v", c.name, got, c.want)
		}
	}
}

func TestFingerprintIgnoresWhatDoesNotReachTheConfig(t *testing.T) {
	base := ComputeFingerprint(baseInput())
	cases := map[string]func(in *FingerprintInput){
		"自动更新计划":      func(in *FingerprintInput) { in.Profile["updates"] = map[string]any{"subscriptions": "daily"} },
		"流量保留月数":      func(in *FingerprintInput) { in.Profile["traffic"] = map[string]any{"keepMonths": float64(6)} },
		"手机 App 地区分流": func(in *FingerprintInput) { in.Profile["shareRegions"] = []any{map[string]any{"id": "x"}} },
		"路由器名称图标":     func(in *FingerprintInput) { in.Profile["serverInfo"] = map[string]any{"name": "家里"} },
		"控制接口密钥":      func(in *FingerprintInput) { in.Profile["clashApiSecret"] = "abc" },
		"过滤名单名字和计划": func(in *FingerprintInput) {
			in.Profile["dns"].(map[string]any)["filter"] = map[string]any{"enabled": false, "lists": []any{map[string]any{"id": "x"}}, "updateDays": float64(3)}
		},
		"订阅刷新带来的用量和时间": func(in *FingerprintInput) {
			in.Subs[0].UpdatedAt, in.Subs[0].NodeCount, in.Subs[0].Usage = 999, 7, &sub.Usage{}
		},
		"停用的订阅改了": func(in *FingerprintInput) { in.Subs[1].URL, in.Subs[1].Name = "https://b.example/new", "B2" },
	}
	for name, mut := range cases {
		in := baseInput()
		mut(&in)
		if got := changed(base, ComputeFingerprint(in)); len(got) != 0 {
			t.Errorf("%s 不该触发待重启,却变了: %v", name, got)
		}
	}
}

func TestFingerprintDoesNotMutateInput(t *testing.T) {
	in := baseInput()
	_ = ComputeFingerprint(in)
	if _, has := in.Profile["dns"].(map[string]any)["filter"]; !has {
		t.Fatal("算指纹不能改动入参里的档案(dns.filter 被删了)")
	}
}

// ---------- Generator.Fingerprint ----------

type fpProfiles struct {
	p   map[string]any
	err error
}

func (f *fpProfiles) Get(context.Context) (map[string]any, error) { return f.p, f.err }

type fpGroups struct {
	g   []any
	err error
}

func (f fpGroups) Groups(context.Context) ([]any, error) { return f.g, f.err }

type fpCat struct {
	recs  []subs.Record
	nodes []subs.CatalogNode
	err   error
}

func (f fpCat) Catalog(context.Context) ([]subs.Record, []subs.CatalogNode, error) {
	return f.recs, f.nodes, f.err
}

type fpFilter struct {
	key   string
	ready bool
	err   error
	calls int
}

func (f *fpFilter) CoreArtifact(context.Context, map[string]any) (map[string]any, string, bool, bool, error) {
	f.calls++
	return nil, f.key, true, f.ready, f.err
}

func filterOn() map[string]any {
	return map[string]any{"dns": map[string]any{"filter": map[string]any{"enabled": true}}}
}

func genWith(p *fpProfiles, g fpGroups, c fpCat, f FilterSource) *Generator {
	gen := NewGenerator(nil, nil, p, g, c, System{}, Options{})
	if f != nil {
		gen.WithFilter(f)
	}
	return gen
}

func TestGeneratorFingerprintFollowsBuildInputs(t *testing.T) {
	in := baseInput()
	gen := genWith(&fpProfiles{p: in.Profile}, fpGroups{g: in.Groups}, fpCat{recs: in.Subs, nodes: in.Nodes}, nil)
	got, err := gen.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := ComputeFingerprint(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v != %v", got, want)
	}
}

func TestGeneratorFingerprintFilterOnlyCountsWhenReady(t *testing.T) {
	mk := func(f *fpFilter) Fingerprint {
		gen := genWith(&fpProfiles{p: filterOn()}, fpGroups{}, fpCat{}, f)
		fp, err := gen.Fingerprint(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	off := ComputeFingerprint(FingerprintInput{Profile: filterOn()})
	notReady := mk(&fpFilter{key: "k1", ready: false})
	if changed(off, notReady) != nil {
		t.Fatalf("开了但还没应用,生成的配置里没有过滤,与没开一样: %v", changed(off, notReady))
	}
	ready1, ready2 := mk(&fpFilter{key: "k1", ready: true}), mk(&fpFilter{key: "k2", ready: true})
	if !reflect.DeepEqual(changed(off, ready1), []string{ReasonDNS}) || !reflect.DeepEqual(changed(ready1, ready2), []string{ReasonDNS}) {
		t.Fatalf("应用后 / 换了名单设置后 dns 应变: %v %v", changed(off, ready1), changed(ready1, ready2))
	}
	// 没开过滤就不去问
	f := &fpFilter{key: "k", ready: true}
	if _, err := genWith(&fpProfiles{p: map[string]any{}}, fpGroups{}, fpCat{}, f).Fingerprint(context.Background()); err != nil || f.calls != 0 {
		t.Fatalf("没开过滤不该读过滤状态: calls=%d err=%v", f.calls, err)
	}
	// 开了、但没接过滤来源:与 BuildWith 一样当作没生效
	if fp, err := genWith(&fpProfiles{p: filterOn()}, fpGroups{}, fpCat{}, nil).Fingerprint(context.Background()); err != nil || !reflect.DeepEqual(fp, off) {
		t.Fatalf("没接过滤来源: %v %v", fp, err)
	}
}

func TestGeneratorFingerprintErrors(t *testing.T) {
	boom := errors.New("读不到")
	cases := map[string]*Generator{
		"档案":   genWith(&fpProfiles{err: boom}, fpGroups{}, fpCat{}, nil),
		"节点组":  genWith(&fpProfiles{p: map[string]any{}}, fpGroups{err: boom}, fpCat{}, nil),
		"订阅":   genWith(&fpProfiles{p: map[string]any{}}, fpGroups{}, fpCat{err: boom}, nil),
		"过滤状态": genWith(&fpProfiles{p: filterOn()}, fpGroups{}, fpCat{}, &fpFilter{err: boom}),
	}
	for name, g := range cases {
		if fp, err := g.Fingerprint(context.Background()); err == nil || fp != nil {
			t.Errorf("%s 读不到应报错且不给指纹: %v %v", name, fp, err)
		}
	}
}

// ---------- Applied ----------

type fakeSource struct {
	mu  sync.Mutex
	fp  Fingerprint
	err error
}

func (f *fakeSource) Fingerprint(context.Context) (Fingerprint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := Fingerprint{}
	for k, v := range f.fp {
		out[k] = v
	}
	return out, nil
}

func (f *fakeSource) set(reason, v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fp[reason] = v
}

func newApplied(t *testing.T) (*Applied, *fakeSource, *store.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{fp: Fingerprint{ReasonNodes: "n1", ReasonDNS: "d1", ReasonRoute: "r1", ReasonInbounds: "i1", ReasonSystem: "s1"}}
	return &Applied{Store: st, Source: src, Now: func() time.Time { return time.UnixMilli(1700000000000) }}, src, st, dir
}

func TestAppliedNoRecordMeansNoPending(t *testing.T) {
	a, _, _, _ := newApplied(t)
	p, err := a.Pending(context.Background())
	if err != nil || p.Pending || p.Reasons == nil || len(p.Reasons) != 0 {
		t.Fatalf("从没部署过不判断,reasons 必须是空数组而不是 nil: %+v %v", p, err)
	}
}

func TestAppliedRecordThenChange(t *testing.T) {
	a, src, _, _ := newApplied(t)
	ctx := context.Background()
	fp, err := a.Snapshot(ctx)
	if err != nil || a.Record(fp) != nil {
		t.Fatal(err)
	}
	if p, err := a.Pending(ctx); err != nil || p.Pending || len(p.Reasons) != 0 {
		t.Fatalf("刚部署完不该有待重启: %+v %v", p, err)
	}
	src.set(ReasonSystem, "s2")
	src.set(ReasonNodes, "n2")
	p, err := a.Pending(ctx)
	if err != nil || !p.Pending || !reflect.DeepEqual(p.Reasons, []string{ReasonNodes, ReasonSystem}) {
		t.Fatalf("应按固定顺序列出 nodes、system: %+v %v", p, err)
	}
	// 改回去就没了:比的是内容,不是「有没有动过」
	src.set(ReasonSystem, "s1")
	src.set(ReasonNodes, "n1")
	if p, _ := a.Pending(ctx); p.Pending {
		t.Fatalf("改回原样不该再提示: %+v", p)
	}
	// 重新部署(记新的)后清掉
	src.set(ReasonDNS, "d2")
	fp2, _ := a.Snapshot(ctx)
	if err := a.Record(fp2); err != nil {
		t.Fatal(err)
	}
	if p, _ := a.Pending(ctx); p.Pending {
		t.Fatalf("重新部署后应清掉: %+v", p)
	}
}

func TestAppliedRecordPersistsAcrossInstances(t *testing.T) {
	a, src, st, _ := newApplied(t)
	fp, _ := a.Snapshot(context.Background())
	if err := a.Record(fp); err != nil {
		t.Fatal(err)
	}
	b := &Applied{Store: st, Source: src}
	src.set(ReasonRoute, "r2")
	if p, err := b.Pending(context.Background()); err != nil || !reflect.DeepEqual(p.Reasons, []string{ReasonRoute}) {
		t.Fatalf("重启后记录还在: %+v %v", p, err)
	}
}

func TestAppliedRecordEmptyClearsOldRecord(t *testing.T) {
	a, src, _, _ := newApplied(t)
	ctx := context.Background()
	fp, _ := a.Snapshot(ctx)
	_ = a.Record(fp)
	src.set(ReasonDNS, "d2")
	if p, _ := a.Pending(ctx); !p.Pending {
		t.Fatal("前提:此时应有待重启")
	}
	if err := a.Record(nil); err != nil {
		t.Fatal(err)
	}
	if p, err := a.Pending(ctx); err != nil || p.Pending {
		t.Fatalf("取不到指纹的部署成功后,旧记录描述的是更早的配置,必须清掉: %+v %v", p, err)
	}
	if err := a.Record(nil); err != nil {
		t.Fatalf("没有旧记录时清除不算错误: %v", err)
	}
}

func TestAppliedErrors(t *testing.T) {
	a, src, _, dir := newApplied(t)
	ctx := context.Background()
	fp, _ := a.Snapshot(ctx)
	_ = a.Record(fp)
	src.err = errors.New("读不到档案")
	if p, err := a.Pending(ctx); err == nil || p.Pending || p.Reasons == nil {
		t.Fatalf("算不出当前指纹要报错(调用方当作没有): %+v %v", p, err)
	}
	if _, err := a.Snapshot(ctx); err == nil {
		t.Fatal("Snapshot 应把错误带出来")
	}
	// 记录文件损坏:报错,不当作没有记录也不覆盖
	src.err = nil
	bad := filepath.Join(dir, fileApplied+".json")
	if err := os.WriteFile(bad, []byte("{坏了"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pending(ctx); err == nil {
		t.Fatal("记录损坏应报错")
	}
	if b, _ := os.ReadFile(bad); string(b) != "{坏了" {
		t.Fatalf("读时不能改动损坏的文件: %s", b)
	}
}

// ---------- 用真实的档案和订阅服务走一遍 ----------

func TestAppliedWithRealServices(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	prof := profile.New(st, nil, nil)
	sv := subs.New(st, nil)
	gen := NewGenerator(nil, st, prof, fpGroups{g: []any{}}, sv, System{}, Options{})
	a := &Applied{Store: st, Source: gen}
	ctx := context.Background()

	name := "A"
	if _, err := sv.Create(ctx, subs.Input{Name: &name, Source: &subs.Source{Content: "trojan://pw@hk.example.com:443#HK 01\n"}, Rename: map[string]any{"enabled": false}}); err != nil {
		t.Fatal(err)
	}
	fp, err := a.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Record(fp); err != nil {
		t.Fatal(err)
	}
	pending := func() []string {
		t.Helper()
		p, err := a.Pending(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return p.Reasons
	}
	if got := pending(); len(got) != 0 {
		t.Fatalf("刚记完不该有待重启: %v", got)
	}

	// 改了不进配置的键:没有
	if _, err := prof.Patch(ctx, map[string]any{"traffic": map[string]any{"keepMonths": float64(6)}}); err != nil {
		t.Fatal(err)
	}
	if got := pending(); len(got) != 0 {
		t.Fatalf("流量保留月数不进配置: %v", got)
	}
	// 改 IPv6:dns
	if _, err := prof.Patch(ctx, map[string]any{"ipv6": true}); err != nil {
		t.Fatal(err)
	}
	if got := pending(); !reflect.DeepEqual(got, []string{ReasonDNS}) {
		t.Fatalf("%v", got)
	}
	// 再加一份订阅:节点也变了,顺序固定 nodes 在前
	name2 := "B"
	if _, err := sv.Create(ctx, subs.Input{Name: &name2, Source: &subs.Source{Content: "trojan://pw@us.example.com:443#US 01\n"}, Rename: map[string]any{"enabled": false}}); err != nil {
		t.Fatal(err)
	}
	if got := pending(); !reflect.DeepEqual(got, []string{ReasonNodes, ReasonDNS}) {
		t.Fatalf("%v", got)
	}
	// 重新部署(这里就是重新记一份)后清掉
	fp, _ = a.Snapshot(ctx)
	_ = a.Record(fp)
	if got := pending(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// ---------- 部署流程里的记录 ----------

type fakeTracker struct {
	mu        sync.Mutex
	log       *[]string
	snapFP    Fingerprint
	snapErr   error
	recordErr error
	recorded  []Fingerprint
}

func (f *fakeTracker) Snapshot(context.Context) (Fingerprint, error) {
	*f.log = append(*f.log, "snapshot")
	return f.snapFP, f.snapErr
}

func (f *fakeTracker) Record(fp Fingerprint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.log = append(*f.log, "record")
	f.recorded = append(f.recorded, fp)
	return f.recordErr
}

// logBuild 在生成时往日志里记一笔,用来确认指纹是在生成之前取的。
type logBuild struct {
	log   *[]string
	built *Built
}

func (b logBuild) Build(context.Context) (*Built, error) {
	*b.log = append(*b.log, "build")
	return b.built, nil
}

func trackedDeployer(t *testing.T, svc *fakeSvc, tr *fakeTracker, cfg map[string]any) *Deployer {
	t.Helper()
	d := newDP(t, cfg, svc)
	d.Build = logBuild{log: tr.log, built: &Built{Config: cfg}}
	d.Track = tr
	return d
}

func TestDeployRecordsFingerprintTakenBeforeBuild(t *testing.T) {
	var log []string
	tr := &fakeTracker{log: &log, snapFP: Fingerprint{ReasonNodes: "n1"}}
	d := trackedDeployer(t, &fakeSvc{}, tr, map[string]any{"marker": "new"})
	if res := d.Deploy(context.Background()); !res.OK {
		t.Fatalf("%+v", res)
	}
	if got := strings.Join(log, ","); got != "snapshot,build,record" {
		t.Fatalf("顺序应是 取指纹 → 生成 → 成功后记录: %s", got)
	}
	if len(tr.recorded) != 1 || !reflect.DeepEqual(tr.recorded[0], tr.snapFP) {
		t.Fatalf("记的应是生成之前取的那份: %v", tr.recorded)
	}
}

func TestDeployDoesNotRecordOnFailure(t *testing.T) {
	cases := map[string]func(t *testing.T) (*Deployer, *fakeTracker){
		"校验没过": func(t *testing.T) (*Deployer, *fakeTracker) {
			var log []string
			tr := &fakeTracker{log: &log, snapFP: Fingerprint{ReasonNodes: "n1"}}
			d := trackedDeployer(t, &fakeSvc{running: true}, tr, map[string]any{"marker": "BAD"}) // 假 sing-box 见到 BAD 就校验失败
			writeCfg(t, d, "old")
			return d, tr
		},
		"重启失败回滚": func(t *testing.T) (*Deployer, *fakeTracker) {
			var log []string
			tr := &fakeTracker{log: &log, snapFP: Fingerprint{ReasonNodes: "n1"}}
			svc := &fakeSvc{running: true, doFn: func(action string, n int) (system.ActionResult, error, bool) {
				if n == 1 { // 第一次(用新配置)重启失败,回滚时的重启正常
					return system.ActionResult{Code: 1, Stderr: "boom"}, nil, true
				}
				return system.ActionResult{}, nil, false
			}}
			d := trackedDeployer(t, svc, tr, map[string]any{"marker": "new"})
			writeCfg(t, d, "old")
			return d, tr
		},
		"健康检查没过回滚": func(t *testing.T) (*Deployer, *fakeTracker) {
			var log []string
			tr := &fakeTracker{log: &log, snapFP: Fingerprint{ReasonNodes: "n1"}}
			d := trackedDeployer(t, &fakeSvc{running: true}, tr, map[string]any{"marker": "bad"})
			writeCfg(t, d, "old")
			return d, tr
		},
	}
	for name, mk := range cases {
		d, tr := mk(t)
		res := d.Deploy(context.Background())
		if res.OK {
			t.Errorf("%s: 前提:这次部署应失败: %+v", name, res)
			continue
		}
		if len(tr.recorded) != 0 {
			t.Errorf("%s: 失败(含回滚)的部署不能更新记录: %v", name, tr.recorded)
		}
	}
}

func TestDeployWithoutFingerprintClearsOldRecord(t *testing.T) {
	var log []string
	tr := &fakeTracker{log: &log, snapErr: errors.New("读不到档案")}
	d := trackedDeployer(t, &fakeSvc{}, tr, map[string]any{"marker": "new"})
	if res := d.Deploy(context.Background()); !res.OK {
		t.Fatalf("取不到指纹不能让部署失败: %+v", res)
	}
	if len(tr.recorded) != 1 || len(tr.recorded[0]) != 0 {
		t.Fatalf("取不到指纹时应记空(= 清掉旧记录): %v", tr.recorded)
	}
}

func TestDeployRecordFailureDoesNotChangeResult(t *testing.T) {
	var log []string
	tr := &fakeTracker{log: &log, snapFP: Fingerprint{ReasonNodes: "n1"}, recordErr: errors.New("磁盘满了")}
	d := trackedDeployer(t, &fakeSvc{}, tr, map[string]any{"marker": "new"})
	if res := d.Deploy(context.Background()); !res.OK || res.Stage != StageRunning {
		t.Fatalf("系统已经变了,记录失败不能改写部署结果: %+v", res)
	}
}

func TestDeployWithoutTrackerStillWorks(t *testing.T) {
	d := newDP(t, map[string]any{"marker": "new"}, &fakeSvc{})
	if res := d.Deploy(context.Background()); !res.OK {
		t.Fatalf("%+v", res)
	}
}
