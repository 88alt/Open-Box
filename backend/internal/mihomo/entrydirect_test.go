package mihomo

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 3e-4d 的自写向量。期望值人工推算:旁路放行的网段 + 直连 IP 计划的网段,合并后扣掉
// 198.18.0.0/16、2001:2:0:8000::/49、198.19.0.0/16、2001:2::/49。

func entryDirectDeps(fs FS, shape ShapeFunc) EntryDirectDeps {
	return EntryDirectDeps{
		FS:        fs,
		RulesDir:  filepath.Join(testMihomoDir, "rules"),
		MihomoDir: testMihomoDir,
		Bypass: EntryBypassDeps{
			Shape:     shape,
			UserLists: func() (map[string]UserList, error) { return map[string]UserList{}, nil },
			SrsPath:   func(tag string) (string, error) { return "/geo/" + tag + ".srs", nil },
			FS:        fs,
		},
	}
}

func shapeOf(m map[string][]string) ShapeFunc {
	return func(_ context.Context, tag string) (RuleShape, error) {
		cidrs, ok := m[tag]
		if !ok {
			return RuleShape{}, errors.New("没有这个规则集")
		}
		return RuleShape{CIDRs: cidrs, DomainKeys: []string{}, OtherKeys: []string{}}, nil
	}
}

func TestBuildEntryDirectWritesProvider(t *testing.T) {
	fs := newMemFS()
	d := entryDirectDeps(fs, shapeOf(map[string][]string{"s1": {"10.0.0.0/8", "198.19.0.0/16"}}))
	plan := RoutePlan{
		Plan:     BypassPlan{Enabled: true, Sets: []string{"s1"}},
		DirectIP: DirectIPPlan{CIDRs: []string{"192.168.0.0/24", "2001:db8::/32"}},
	}
	got, err := BuildEntryDirect(context.Background(), plan, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 198.19.0.0/16 是假 IP 池要扣掉;2001:db8::/32 不在保留段里,原样留下。
	if got.Provider == nil {
		t.Fatal("有网段却没写 provider")
	}
	if !strings.HasPrefix(got.Provider.Path, "./rules/ob-entry-direct-") || got.Provider.Behavior != "ipcidr" || got.Provider.Type != "file" {
		t.Fatalf("provider=%+v", got.Provider)
	}
	full := filepath.Join(d.RulesDir, filepath.Base(got.Provider.Path))
	if want := "payload:\n  - 10.0.0.0/8\n  - 192.168.0.0/24\n  - 2001:db8::/32\n"; string(fs.files[full]) != want {
		t.Errorf("载荷 = %q; want %q", fs.files[full], want)
	}
	if len(got.FallbackKeepIPv4) != 0 || len(got.DirectIPCidrs) != 0 {
		t.Errorf("没有兜底计划时不该有兜底网段:%+v", got)
	}
}

func TestBuildEntryDirectScoped(t *testing.T) {
	fs := newMemFS()
	d := entryDirectDeps(fs, shapeOf(map[string][]string{"s1": {"10.0.0.0/8"}}))
	plan := RoutePlan{
		Plan:     BypassPlan{Enabled: true, Sets: []string{"s1"}},
		DirectIP: DirectIPPlan{CIDRs: []string{"172.16.0.0/12"}},
		Scoped:   true,
	}
	got, err := BuildEntryDirect(context.Background(), plan, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != nil {
		t.Errorf("白名单模式不该写 provider:%+v", got.Provider)
	}
	if want := []string{"10.0.0.0/8", "172.16.0.0/12"}; !reflect.DeepEqual(got.DirectIPCidrs, want) {
		t.Errorf("DirectIPCidrs = %v; want %v", got.DirectIPCidrs, want)
	}
	if fs.writes != 0 {
		t.Errorf("白名单模式不该写文件,写了 %d 次", fs.writes)
	}
}

func TestBuildEntryDirectFallback(t *testing.T) {
	fs := newMemFS()
	d := entryDirectDeps(fs, shapeOf(map[string][]string{
		"s1":      {"10.0.0.0/8"},
		"geosite": {"1.2.3.0/24"},
	}))
	plan := RoutePlan{
		Plan:     BypassPlan{Enabled: true, Sets: []string{"s1"}},
		DirectIP: DirectIPPlan{CIDRs: []string{}},
		Fallback: &SourceScopedFallback{NeedSets: []string{"geosite"}, NeedCidrs: []string{"8.8.8.0/24"}},
	}
	got, err := BuildEntryDirect(context.Background(), plan, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 兜底 = 假 IP 池 + 0/8 + 127/8 + 169.254/16 + 224/3 + 保留段(198.18/16、198.19/16 在 v4 里)
	// + 调用方给的 8.8.8.0/24 + 规则集里的 1.2.3.0/24,合并后只留 IPv4。
	want := []string{"0.0.0.0/8", "1.2.3.0/24", "8.8.8.0/24", "127.0.0.0/8", "169.254.0.0/16", "198.18.0.0/15", "224.0.0.0/3"}
	if !reflect.DeepEqual(got.FallbackKeepIPv4, want) {
		t.Errorf("FallbackKeepIPv4 = %v; want %v", got.FallbackKeepIPv4, want)
	}
	// 兜底规则集取不到时整体为空(上游 Nu 同样如此),不影响 provider。
	d2 := entryDirectDeps(newMemFS(), func(_ context.Context, tag string) (RuleShape, error) {
		if tag == "s1" {
			return RuleShape{CIDRs: []string{"10.0.0.0/8"}, DomainKeys: []string{}, OtherKeys: []string{}}, nil
		}
		return RuleShape{}, errors.New("读不出来")
	})
	got2, err := BuildEntryDirect(context.Background(), plan, d2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.FallbackKeepIPv4) != 0 {
		t.Errorf("规则集读不出来时兜底应为空:%v", got2.FallbackKeepIPv4)
	}
}

func TestBuildEntryDirectCache(t *testing.T) {
	fs := newMemFS()
	calls := 0
	shape := func(_ context.Context, tag string) (RuleShape, error) {
		calls++
		return RuleShape{CIDRs: []string{"10.0.0.0/8"}, DomainKeys: []string{}, OtherKeys: []string{}}, nil
	}
	d := entryDirectDeps(fs, shape)
	plan := RoutePlan{Plan: BypassPlan{Enabled: true, Sets: []string{"s1"}}, DirectIP: DirectIPPlan{}, Key: "k1"}
	cache := NewEntryDirectCache(16)
	first, err := BuildEntryDirect(context.Background(), plan, d, cache)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || fs.writes != 1 {
		t.Fatalf("第一次: calls=%d writes=%d", calls, fs.writes)
	}
	// 再来一次:命中缓存,不再取形状、不再写文件。
	second, err := BuildEntryDirect(context.Background(), plan, d, cache)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || fs.writes != 1 {
		t.Errorf("命中缓存又算了一遍: calls=%d writes=%d", calls, fs.writes)
	}
	if second.Provider == nil || second.Provider.Path != first.Provider.Path {
		t.Errorf("缓存返回的 provider 不一致:%+v vs %+v", second.Provider, first.Provider)
	}
	// provider 文件被删了 → 缓存作废,重算(再写一次)。
	delete(fs.files, filepath.Join(d.RulesDir, filepath.Base(first.Provider.Path)))
	if _, err := BuildEntryDirect(context.Background(), plan, d, cache); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || fs.writes != 2 {
		t.Errorf("文件没了应重算: calls=%d writes=%d", calls, fs.writes)
	}
}

func TestEntryDirectCacheEviction(t *testing.T) {
	c := NewEntryDirectCache(2)
	c.put("a", EntryDirect{})
	c.put("b", EntryDirect{})
	// 已在里面的再放回去 = 移到队尾,于是 c 最老。
	c.put("a", EntryDirect{})
	c.put("d", EntryDirect{})
	if _, ok := c.get("b"); ok {
		t.Error("b 应被淘汰")
	}
	for _, k := range []string{"a", "d"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s 不该被淘汰", k)
		}
	}
}

func TestBuildEntryDirectUsesDeployedProvider(t *testing.T) {
	fs := newMemFS()
	p := writeGeoIP(t, fs, "geoip-cn", "1.2.3.0/24")
	d := entryDirectDeps(fs, func(_ context.Context, tag string) (RuleShape, error) {
		return RuleShape{}, errors.New("应该先读已部署的 provider")
	})
	d.Deployed = map[string]Provider{"geoip-cn": p}
	plan := RoutePlan{Plan: BypassPlan{Enabled: true, Sets: []string{"geoip-cn"}}, DirectIP: DirectIPPlan{}}
	got, err := BuildEntryDirect(context.Background(), plan, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(d.RulesDir, filepath.Base(got.Provider.Path))
	if !strings.Contains(string(fs.files[full]), "1.2.3.0/24") {
		t.Errorf("provider 应带上已部署的网段:%q", fs.files[full])
	}
}

func TestBuildEntryDirectNoPlan(t *testing.T) {
	fs := newMemFS()
	d := entryDirectDeps(fs, shapeOf(nil))
	got, err := BuildEntryDirect(context.Background(), RoutePlan{}, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != nil || len(got.DirectIPCidrs) != 0 || len(got.FallbackKeepIPv4) != 0 {
		t.Errorf("没有计划时应为空:%+v", got)
	}
	if fs.writes != 0 {
		t.Errorf("不该写文件")
	}
}
