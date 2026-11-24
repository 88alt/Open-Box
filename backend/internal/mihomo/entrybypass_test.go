package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildEntryBypassMatchesUpstream(t *testing.T) {
	g := loadGolden(t)
	if len(g.Bypass) < 50 {
		t.Fatalf("向量太少:%d", len(g.Bypass))
	}
	enabled, trimmed, fallback := 0, 0, 0
	for i, c := range g.Bypass {
		fs := newMemFS()
		for _, tag := range c.Existing {
			fs.files["/user/"+tag+".srs"] = []byte("srs")
		}
		deps := EntryBypassDeps{
			Shape:   shapeTable(c.Shapes),
			SrsPath: RuleSetPathFunc("/geo", "/user"),
			FS:      fs,
			UserLists: func() (map[string]UserList, error) {
				if string(c.UserLists) == "null" {
					return nil, errors.New("pt 失败")
				}
				var raw map[string]struct {
					IP bool `json:"ip"`
				}
				if err := json.Unmarshal(c.UserLists, &raw); err != nil {
					return nil, err
				}
				out := map[string]UserList{}
				for k, v := range raw {
					out[k] = UserList{IP: v.IP}
				}
				return out, nil
			},
		}
		plan := c.Plan
		got := BuildEntryBypass(context.Background(), &plan, deps)
		if !reflect.DeepEqual(got, c.Want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(c.Want)
			t.Errorf("#%d %s\n plan %+v\n got  %s\n want %s", i, c.Name, c.Plan, gj, wj)
		}
		if c.Want.Enabled {
			enabled++
		}
		if len(c.Want.Trimmed) > 0 {
			trimmed++
		}
		if strings.Contains(c.Want.Reason, "按兼容路径进内核") {
			fallback++
		}
	}
	// 向量要真的覆盖到三类结果,否则对拍没意义。
	if enabled < 10 || trimmed < 5 || fallback < 10 {
		t.Errorf("向量覆盖不够:启用 %d、扣过地址 %d、走兼容路径 %d", enabled, trimmed, fallback)
	}
}

func TestBuildEntryBypassNilAndEmpty(t *testing.T) {
	got := BuildEntryBypass(context.Background(), nil, EntryBypassDeps{})
	b, _ := json.Marshal(got)
	if want := `{"enabled":false,"sets":[],"trimmed":{},"pending":[],"fakeIp":false,"checked":[],"cidrs":{},"reason":""}`; string(b) != want {
		t.Errorf("nil 计划 = %s", b)
	}
	// 没有要放行的集合:只带 plan 的 fakeIp 和 reason,不碰任何依赖。
	got = BuildEntryBypass(context.Background(), &BypassPlan{FakeIP: true, Reason: "后端设置里关掉了"}, EntryBypassDeps{})
	if got.Enabled || !got.FakeIP || got.Reason != "后端设置里关掉了" || len(got.Sets) != 0 {
		t.Errorf("空计划 = %+v", got)
	}
}

func TestBuildEntryBypassHandPicked(t *testing.T) {
	ctx := context.Background()
	shapes := map[string]shapeVal{
		"geoip-cn": {RuleShape: RuleShape{CIDRs: []string{"1.0.0.0/8", "198.18.0.0/15", "224.0.0.0/3"}}},
	}
	deps := EntryBypassDeps{Shape: shapeTable(shapes), SrsPath: RuleSetPathFunc("/geo", "/user"), FS: newMemFS()}

	// FakeIP 池和地址空间最后一个地址都要扣掉;列表里原有的网段不变样地保留。
	got := BuildEntryBypass(ctx, &BypassPlan{Enabled: true, Sets: []string{"geoip-cn"}, FakeIP: true}, deps)
	if !got.Enabled || !reflect.DeepEqual(got.Sets, []string{"geoip-cn"}) {
		t.Fatalf("%+v", got)
	}
	tr := got.Trimmed["geoip-cn"]
	if tr.Original != 3 || len(tr.Removed) != 2 ||
		!strings.HasPrefix(tr.Removed[0], "到地址空间末尾的区间（224.0.0.0/3）") || tr.Removed[1] != "FakeIP 占位地址池" {
		t.Errorf("trimmed = %+v", tr)
	}
	if !reflect.DeepEqual(got.CIDRs["geoip-cn"], tr.CIDRs) {
		t.Errorf("cidrs 与 trimmed 应一致:%v / %v", got.CIDRs["geoip-cn"], tr.CIDRs)
	}
	for _, c := range tr.CIDRs {
		if c == "198.19.0.0/16" || c == "255.255.255.255/32" {
			t.Errorf("不该保留 %s", c)
		}
	}
	if !strings.Contains(got.Reason, "扣掉 ") || !strings.Contains(got.Reason, "后入口旁路（保留 ") {
		t.Errorf("reason = %q", got.Reason)
	}

	// 取不到形状的集合走兼容路径,原因写进 reason 和 checked。
	got = BuildEntryBypass(ctx, &BypassPlan{Enabled: true, Sets: []string{"geoip-zz"}}, deps)
	if got.Enabled || len(got.Checked) != 1 || got.Checked[0].OK || got.Checked[0].Reason != "集合「geoip-zz」本地没有这份规则集文件" ||
		got.Reason != "集合「geoip-zz」本地没有这份规则集文件,按兼容路径进内核" {
		t.Errorf("%+v", got)
	}

	// 规则集名不合法(上游会抛异常):这里只让相关集合走兼容路径。
	d2 := deps
	d2.Shape = func(context.Context, string) (RuleShape, error) {
		return RuleShape{}, errors.New("不合法的规则集名 ../x")
	}
	got = BuildEntryBypass(ctx, &BypassPlan{Sets: []string{"../x"}}, d2)
	if got.Enabled || !strings.Contains(got.Reason, "不合法的规则集名") {
		t.Errorf("%+v", got)
	}

	// 同一个规则集只取一次形状(memo)。
	calls := 0
	d3 := deps
	d3.Shape = func(ctx context.Context, tag string) (RuleShape, error) {
		calls++
		return shapeTable(shapes)(ctx, tag)
	}
	BuildEntryBypass(ctx, &BypassPlan{Sets: []string{"geoip-cn"}, Pending: []PendingItem{
		{Policy: "p", Sets: []string{"geoip-cn"}, Against: []EarlierRule{{Name: "a", Geoip: []string{"geoip-cn"}}}},
	}}, d3)
	if calls != 1 {
		t.Errorf("形状应只取一次,实际 %d 次", calls)
	}
}
