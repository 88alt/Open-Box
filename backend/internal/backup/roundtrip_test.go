package backup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/api/storage"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// passCore:规范化不改动输入(规范化是核心的事,这里只测备份的往返)。
type passCore struct{}

func (passCore) NormalizeGroups(_ context.Context, g []any) ([]any, error) { return g, nil }
func (passCore) EmitGroups(context.Context, []any, []any) ([]nodegroups.Dropped, error) {
	return nil, nil
}

type realEnv struct {
	st     *store.Store
	prof   *profile.Service
	subs   *subs.Service
	groups *nodegroups.Service
	svc    *Service
}

func newRealEnv(t *testing.T) realEnv {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prof := profile.New(st, nil, nil)
	sb := subs.New(st, nil)
	g := nodegroups.New(st, passCore{}, sb, nodegroups.ChainsFromProfile(prof))
	return realEnv{st: st, prof: prof, subs: sb, groups: g, svc: &Service{
		Profiles: prof, Groups: g, Subs: sb, Panel: storage.Panel{Store: st}, Version: "t",
		Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
	}}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExportThenImportIntoFreshStoreRoundTrips(t *testing.T) {
	ctx := context.Background()
	a := newRealEnv(t)
	// 做一份有内容的「旧路由器」:档案、两份订阅(粘贴内容,不联网)、节点组、面板设置、背景图
	if _, err := a.prof.Patch(ctx, map[string]any{"ipv6": true, "testUrl": "https://t.test/204",
		"clientRoutes": []any{}, "serverInfo": map[string]any{"name": "家里"}}); err != nil {
		t.Fatal(err)
	}
	name1, name2 := "订阅一", "订阅二"
	r1, err := a.subs.Create(ctx, subs.Input{Name: &name1, Source: &subs.Source{Content: "trojan://pw1@h1.test:443#甲\ntrojan://pw2@h2.test:443#乙"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.subs.Create(ctx, subs.Input{Name: &name2, Source: &subs.Source{Content: "trojan://pw3@h3.test:443#丙"}, Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	_, nodes, _ := a.subs.Dump(ctx)
	first := nodes[r1.ID][0].Tag
	if _, err := a.groups.Save(ctx, []any{map[string]any{"id": "g1", "name": "我的组", "type": "selector", "mode": "static", "members": []any{first}, "enabled": true}}); err != nil {
		t.Fatal(err)
	}
	pa := storage.Panel{Store: a.st}
	if err := pa.ReplaceSettings(ctx, map[string]string{"config/theme": "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := pa.SetBackgroundImage(ctx, "data:image/png;base64,AAAA"); err != nil {
		t.Fatal(err)
	}

	exported, err := a.svc.Export(ctx, Options{Subscriptions: true, ChainProxies: true, ClientRoutes: true, Servers: true})
	if err != nil {
		t.Fatal(err)
	}
	// 走一遍真实的文件:序列化成 JSON 再读回 ImportFile(接口就是这样收到的)
	var file ImportFile
	if err := json.Unmarshal([]byte(jsonOf(t, exported)), &file); err != nil {
		t.Fatal(err)
	}

	b := newRealEnv(t)
	res, err := b.svc.Import(ctx, &file, subs.RestoreReplace)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported.Subscriptions != 2 || res.Imported.Nodes != 3 || res.Imported.PanelSettings == nil || len(res.Warnings) != 0 {
		t.Fatalf("导入结果不对: %+v %v", res.Imported, res.Warnings)
	}
	again, err := b.svc.Export(ctx, Options{Subscriptions: true, ChainProxies: true, ClientRoutes: true, Servers: true})
	if err != nil {
		t.Fatal(err)
	}
	if jsonOf(t, exported) != jsonOf(t, again) {
		t.Fatalf("导入之后再导出,内容应与原来完全一致\n原来 %s\n之后 %s", jsonOf(t, exported), jsonOf(t, again))
	}
}

func TestImportReplacesExistingAndAppendKeepsIt(t *testing.T) {
	ctx := context.Background()
	src := newRealEnv(t)
	n := "来自备份"
	if _, err := src.subs.Create(ctx, subs.Input{Name: &n, Source: &subs.Source{Content: "trojan://pw@h.test:443#甲"}}); err != nil {
		t.Fatal(err)
	}
	exported, _ := src.svc.Export(ctx, Options{Subscriptions: true, ChainProxies: true})
	var file ImportFile
	_ = json.Unmarshal([]byte(jsonOf(t, exported)), &file)

	dst := newRealEnv(t)
	m := "本机原有"
	if _, err := dst.subs.Create(ctx, subs.Input{Name: &m, Source: &subs.Source{Content: "trojan://pw@h.test:443#甲"}}); err != nil {
		t.Fatal(err)
	}
	// 追加:原有的保留,同名节点改名,节点名全局唯一
	res, err := dst.svc.Import(ctx, &file, subs.RestoreAppend)
	if err != nil {
		t.Fatal(err)
	}
	recs, nodes, _ := dst.subs.Dump(ctx)
	if len(recs) != 2 || res.Imported.RenamedNodes != 1 || len(res.Warnings) != 1 {
		t.Fatalf("追加后应有两份订阅且一个节点被改名: %d %+v %v", len(recs), res.Imported, res.Warnings)
	}
	if nodes[recs[0].ID][0].Tag == nodes[recs[1].ID][0].Tag {
		t.Fatal("追加后节点名必须不同")
	}
	// 覆盖:只剩备份里的那份
	if _, err := dst.svc.Import(ctx, &file, subs.RestoreReplace); err != nil {
		t.Fatal(err)
	}
	recs, _, _ = dst.subs.Dump(ctx)
	if len(recs) != 1 || recs[0].Name != "来自备份" {
		t.Fatalf("覆盖后只该剩备份里的订阅: %+v", recs)
	}
}

func TestImportGroupNameClashingWithNodeIsRejectedBeforeAnythingIsWritten(t *testing.T) {
	ctx := context.Background()
	e := newRealEnv(t)
	n := "S"
	if _, err := e.subs.Create(ctx, subs.Input{Name: &n, Source: &subs.Source{Content: "trojan://pw@h.test:443#甲"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prof.Patch(ctx, map[string]any{"ipv6": true}); err != nil {
		t.Fatal(err)
	}
	before := jsonOf(t, mustExport(t, e))
	_, nodes, _ := e.subs.Dump(ctx)
	var tag string
	for _, ns := range nodes {
		tag = ns[0].Tag
	}
	// 文件的节点组名字和(追加后仍存在的)现有节点重名
	file := ImportFile{Format: Format, Version: f64(1), Profile: map[string]any{"ipv6": false},
		Groups: []any{map[string]any{"id": "g", "name": tag, "type": "selector", "mode": "static", "members": []any{}, "enabled": true}}}
	_, err := e.svc.Import(ctx, &file, "")
	var ie *InputError
	if !asInputError(err, &ie) {
		t.Fatalf("应被拒绝为 400: %v", err)
	}
	if after := jsonOf(t, mustExport(t, e)); after != before {
		t.Fatalf("被拒绝的导入不该改动任何东西\n前 %s\n后 %s", before, after)
	}
}

func mustExport(t *testing.T, e realEnv) *Backup {
	t.Helper()
	b, err := e.svc.Export(context.Background(), Options{Subscriptions: true, ChainProxies: true, ClientRoutes: true, Servers: true})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
