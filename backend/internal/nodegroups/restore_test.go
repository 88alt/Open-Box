package nodegroups

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// fakeCore 不改动输入(规范化是核心的事,这里只测服务层的流程);dropped 可以注入。
type fakeCore struct {
	dropped []Dropped
	normErr error
}

func (f fakeCore) NormalizeGroups(_ context.Context, g []any) ([]any, error) { return g, f.normErr }
func (f fakeCore) EmitGroups(context.Context, []any, []any) ([]Dropped, error) {
	return f.dropped, nil
}

type fakeCat struct{ nodes []string }

func (f fakeCat) Catalog(context.Context) ([]subs.Record, []subs.CatalogNode, error) {
	out := []subs.CatalogNode{}
	for _, n := range f.nodes {
		out = append(out, subs.CatalogNode{SubscriptionID: "s", SubscriptionName: "S", Node: sub.Node{Tag: n}})
	}
	return []subs.Record{{ID: "s", Name: "S", Enabled: true}}, out, nil
}

func grp(id, name string, members ...any) map[string]any {
	if members == nil {
		members = []any{}
	}
	return map[string]any{"id": id, "name": name, "type": "selector", "mode": "static", "members": members, "enabled": true}
}

func newRestoreSvc(t *testing.T, core Core, nodes ...string) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, core, fakeCat{nodes: nodes}, nil), st
}

func TestCheckRestoreValidatesWithoutWriting(t *testing.T) {
	s, st := newRestoreSvc(t, fakeCore{})
	tags := map[string]bool{"节点甲": true}
	if _, err := s.CheckRestore(context.Background(), []any{grp("g1", "组一", "节点甲")}, tags); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Load(fileGroups, &groupsFile{}); ok {
		t.Fatal("CheckRestore 不该写盘")
	}
	cases := map[string][]any{
		"组名为空":     {grp("g1", "  ")},
		"组名重复":     {grp("g1", "A"), grp("g2", "A")},
		"与节点重名":    {grp("g1", "节点甲")},
		"组名太长":     {grp("g1", strings.Repeat("长", MaxNameRunes+1))},
		"不是对象":     {"x"},
		"组太多":      make([]any, MaxGroups+1),
		"组里混进了非对象": {grp("g1", "ok"), 5},
	}
	for name, in := range cases {
		if name == "组太多" {
			for i := range in {
				in[i] = grp("g", "x")
			}
		}
		_, err := s.CheckRestore(context.Background(), in, tags)
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%s: 应是 InputError,得到 %v", name, err)
		}
	}
}

func TestCheckRestoreWithoutCoreIsUnavailable(t *testing.T) {
	s, _ := newRestoreSvc(t, nil)
	if _, err := s.CheckRestore(context.Background(), []any{}, nil); !errors.Is(err, ErrCoreUnavailable) {
		t.Fatalf("没有核心应报 ErrCoreUnavailable: %v", err)
	}
	if _, err := s.Restore(context.Background(), []any{}); !errors.Is(err, ErrCoreUnavailable) {
		t.Fatalf("Restore 也一样: %v", err)
	}
}

func TestRestoreWritesWholeFileWithoutRenameMigration(t *testing.T) {
	s, _ := newRestoreSvc(t, fakeCore{dropped: []Dropped{{Name: "环", Reason: "cycle"}}}, "节点甲")
	ctx := context.Background()
	// 旧档:g1 叫「旧名」。Save 会因为 g1 改名而把别的组里的「旧名」改成新名;Restore 不该这么做。
	if _, err := s.Save(ctx, []any{grp("g1", "旧名"), grp("g2", "二", "旧名")}); err != nil {
		t.Fatal(err)
	}
	file := []any{grp("g1", "新名"), grp("g2", "二", "旧名", "节点甲", "不存在的")}
	res, err := s.Restore(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Groups(ctx)
	if !reflect.DeepEqual(got, file) {
		t.Fatalf("应原样整份写入: %v", got)
	}
	if len(res.Renamed) != 0 {
		t.Fatalf("Restore 不做改名迁移: %+v", res.Renamed)
	}
	if len(res.Dropped) != 1 || res.Dropped[0].Name != "环" {
		t.Fatalf("被丢弃的组要如实报告: %+v", res.Dropped)
	}
	if len(res.Dangling) != 1 || res.Dangling[0].Name != "二" || !reflect.DeepEqual(res.Dangling[0].Members, []string{"旧名", "不存在的"}) {
		t.Fatalf("悬空成员要如实报告(旧名既不是节点也不是组了): %+v", res.Dangling)
	}
}

func TestRestoreRejectedChangesNothing(t *testing.T) {
	s, _ := newRestoreSvc(t, fakeCore{}, "节点甲")
	ctx := context.Background()
	if _, err := s.Save(ctx, []any{grp("g1", "原有")}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Restore(ctx, []any{grp("g9", "节点甲")}) // 与现有节点重名
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("应被拒绝: %v", err)
	}
	got, _ := s.Groups(ctx)
	if len(got) != 1 || got[0].(map[string]any)["name"] != "原有" {
		t.Fatalf("被拒绝后原有节点组不该变: %v", got)
	}
}

func TestSaveStillMigratesRenames(t *testing.T) {
	s, _ := newRestoreSvc(t, fakeCore{})
	ctx := context.Background()
	if _, err := s.Save(ctx, []any{grp("g1", "旧名"), grp("g2", "二", "旧名")}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Save(ctx, []any{grp("g1", "新名"), grp("g2", "二", "旧名")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Renamed) != 1 || res.Renamed[0].To != "新名" {
		t.Fatalf("Save 应继续做改名迁移: %+v", res.Renamed)
	}
	if m := res.Groups[1].(map[string]any)["members"].([]any); len(m) != 1 || m[0] != "新名" {
		t.Fatalf("g2 里的引用应跟着改: %v", m)
	}
}
