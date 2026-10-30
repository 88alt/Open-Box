package failover

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

func sp(s string) *string { return &s }

// 与 core 的 FailoverState 同形状的 JSON(两个页签:主用单节点,备用两节点自动择优)。
const corePlan = `[{"id":"g1","tag":"故障转移","rejectTag":"__fo:reject",
 "lanes":[
  {"id":"l1","name":"主线","index":0,"members":["HK-01"],"valid":["HK-01"],"mode":"single","ref":"HK-01","subTag":null},
  {"id":"l2","name":"备线","index":1,"members":["JP-01","JP-02","gone"],"valid":["JP-01","JP-02"],"mode":"urltest","ref":"__fo:g1:l2","subTag":"__fo:g1:l2"},
  {"id":"l3","name":"空","index":2,"members":[],"valid":[],"mode":"empty","ref":null,"subTag":null}],
 "settings":{"interval":"300s","intervalMs":300000,"tolerance":100,"testUrl":"http://www.gstatic.com/generate_204","timeoutMs":5000,"failureThreshold":2,"restorePrimary":true,"recoveryHoldMs":60000}}]`

func planOf(t *testing.T) Plan {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(corePlan), &v); err != nil {
		t.Fatal(err)
	}
	p, err := ParsePlan(v)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseAndSaveLoadPlan(t *testing.T) {
	p := planOf(t)
	if len(p) != 1 || p[0].Tag != "故障转移" || len(p[0].Lanes) != 3 || p[0].Settings.FailureThreshold != 2 || !p[0].Settings.RestorePrimary {
		t.Fatalf("%+v", p)
	}
	path := filepath.Join(t.TempDir(), PlanFileName)
	if got, err := LoadPlan(path); err != nil || len(got) != 0 {
		t.Fatalf("文件不存在 = 没有计划: %v %v", got, err)
	}
	if err := SavePlan(path, p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 && filepath.Separator == '/' {
		t.Errorf("权限 %v", fi.Mode())
	}
	got, err := LoadPlan(path)
	if err != nil || len(got) != 1 || got[0].Lanes[1].Valid[1] != "JP-02" || *got[0].Lanes[1].SubTag != "__fo:g1:l2" {
		t.Fatalf("%+v %v", got, err)
	}
	// 没有故障转移组:存 [],盖掉旧计划
	if err := SavePlan(path, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadPlan(path); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if p, err := ParsePlan(nil); err != nil || len(p) != 0 {
		t.Errorf("nil 当作没有: %v %v", p, err)
	}
}

func TestLoadPlanRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, s := range map[string]string{"notjson": "{{", "notarray": `{"a":1}`, "noid": `[{"tag":"x"}]`, "notag": `[{"id":"x"}]`} {
		if _, err := LoadPlan(write(name, s)); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
	if got, err := LoadPlan(write("empty", "  \n")); err != nil || len(got) != 0 {
		t.Errorf("空文件当作没有计划: %v %v", got, err)
	}
}

func kernel(now map[string]string) KernelFunc {
	return func(context.Context) ([]singbox.KernelProxy, error) {
		var out []singbox.KernelProxy
		for n, v := range now {
			out = append(out, singbox.KernelProxy{Name: n, Type: "Selector", Now: v})
		}
		return out, nil
	}
}

func TestBuildStatusPrimaryBackupReject(t *testing.T) {
	p := planOf(t)
	cases := []struct {
		name     string
		now      string
		status   string
		laneID   string
		hasLane  bool
		kernelNo string
	}{
		{"主用", "HK-01", "ok", "l1", true, "HK-01"},
		{"备用", "__fo:g1:l2", "backup", "l2", true, "__fo:g1:l2"},
		{"全部不可用,走拒绝", "__fo:reject", "reject", "", false, "__fo:reject"},
		{"选中了计划之外的东西", "other", "unknown", "", false, "other"},
	}
	for _, c := range cases {
		st := BuildStatus(context.Background(), p, kernel(map[string]string{"故障转移": c.now, "__fo:g1:l2": "JP-02"}))
		g := st.Groups[0]
		if g.Status != c.status || (g.CurrentLaneID != nil) != c.hasLane || g.KernelNow == nil || *g.KernelNow != c.kernelNo {
			t.Errorf("%s: %+v", c.name, g)
			continue
		}
		if c.hasLane && *g.CurrentLaneID != c.laneID {
			t.Errorf("%s: lane=%s", c.name, *g.CurrentLaneID)
		}
	}
}

func TestBuildStatusShape(t *testing.T) {
	st := BuildStatus(context.Background(), planOf(t), kernel(map[string]string{"故障转移": "HK-01", "__fo:g1:l2": "JP-02"}))
	if st.Running || st.Paused != "" || st.Version != nil || len(st.Groups) != 1 {
		t.Fatalf("没有检测管理器:不在运行: %+v", st)
	}
	g := st.Groups[0]
	if g.RejectTag != "__fo:reject" || len(g.LaneOrder) != 3 || g.LaneOrder[1] != "l2" || g.InFlight || g.LastSwitch != nil || g.Settings.TimeoutMs != 5000 {
		t.Errorf("%+v", g)
	}
	l1, l2, l3 := g.Lanes[0], g.Lanes[1], g.Lanes[2]
	if l1.Role != "primary" || l2.Role != "backup" || l2.Mode != "urltest" || l2.Manual || l1.Health != "unknown" {
		t.Errorf("%+v %+v", l1, l2)
	}
	if l2.KernelNow == nil || *l2.KernelNow != "JP-02" || l1.KernelNow != nil {
		t.Errorf("子组选中的节点: %+v", l2.KernelNow)
	}
	if _, ok := l2.Nodes["JP-01"]; !ok || len(l2.Nodes) != 2 || string(l2.Nodes["JP-01"]) != "null" {
		t.Errorf("每个有效节点一项、没有检测结果是 null: %v", l2.Nodes)
	}
	if l3.Mode != "empty" || l3.Ref != nil || len(l3.Members) != 0 {
		t.Errorf("%+v", l3)
	}
	// JSON 形状:数组 / 对象不能是 null
	b, _ := json.Marshal(st)
	var raw map[string]any
	json.Unmarshal(b, &raw)
	lane := raw["groups"].([]any)[0].(map[string]any)["lanes"].([]any)[2].(map[string]any)
	if lane["members"] == nil || lane["valid"] == nil || lane["nodes"] == nil {
		t.Errorf("空页签的 members / valid / nodes 不能是 null: %v", lane)
	}
}

func TestBuildStatusKernelUnavailable(t *testing.T) {
	down := func(context.Context) ([]singbox.KernelProxy, error) { return nil, errors.New("内核没在运行") }
	for _, k := range []KernelFunc{down, nil} {
		g := BuildStatus(context.Background(), planOf(t), k).Groups[0]
		if g.Status != "unknown" || g.KernelNow != nil || g.CurrentLaneID != nil || len(g.Lanes) != 3 {
			t.Errorf("读不到内核时照计划如实给、状态未知: %+v", g)
		}
	}
	if st := BuildStatus(context.Background(), Plan{}, nil); st.Groups == nil || len(st.Groups) != 0 {
		t.Errorf("没有组时 groups 是空数组: %+v", st.Groups)
	}
}
