package failover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// realCorePlan 是 2026-11-27 用真实 openbox-core(本机编译的 x86_64 版)调用
// configDetailed 得到的 failover 计划,原样贴进来当回放样本:
// 两个 shadowsocks 节点、一个 failover 组(两条 lane,各一个成员)。
// 用它验「core 真的产出这个形状」之后,SavePlan / LoadPlan / status 这条链能不能吃下去。
const realCorePlan = `[{"id":"f","lanes":[` +
	`{"icon":"","id":"l1","index":0,"members":["香港-01"],"mode":"single","name":"","ref":"香港-01","subTag":null,"valid":["香港-01"]},` +
	`{"icon":"","id":"l2","index":1,"members":["香港-02"],"mode":"single","name":"","ref":"香港-02","subTag":null,"valid":["香港-02"]}],` +
	`"rejectTag":"拒绝","settings":{"failureThreshold":2,"interval":"300s","intervalMs":300000,"recoveryHoldMs":60000,` +
	`"restorePrimary":true,"testUrl":"https://t.example/204","timeoutMs":5000,"tolerance":100},"tag":"FO"}]`

// 真实 core 的计划要能被 SavePlan 存下、LoadPlan 原样读回,而且组与 lane 的字段都不丢。
func TestSaveLoadRealCorePlan(t *testing.T) {
	var p Plan
	if err := json.Unmarshal([]byte(realCorePlan), &p); err != nil {
		t.Fatalf("真实 core 的计划解不开: %v", err)
	}
	if len(p) != 1 || p[0].ID != "f" || len(p[0].Lanes) != 2 {
		t.Fatalf("计划形状不对: %+v", p)
	}
	if p[0].RejectTag != "拒绝" || p[0].Settings.FailureThreshold != 2 || p[0].Settings.Tolerance != 100 {
		t.Fatalf("设置项丢了: %+v", p[0].Settings)
	}

	path := filepath.Join(t.TempDir(), "failover.json")
	if err := SavePlan(path, p); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	// 权限 0600(Windows 上没有权限位,跳过这条断言)
	if st, err := os.Stat(path); err == nil && st.Mode().Perm() != 0o600 && st.Mode().Perm() != 0o666 {
		t.Logf("权限 %v(非 POSIX 平台可忽略)", st.Mode().Perm())
	}

	got, err := LoadPlan(path)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if len(got) != 1 || got[0].Tag != "FO" || len(got[0].Lanes) != 2 {
		t.Fatalf("读回来的计划不一致: %+v", got)
	}
	if got[0].Lanes[1].Members[0] != "香港-02" || got[0].Lanes[1].Index != 1 {
		t.Fatalf("lane 内容丢了: %+v", got[0].Lanes[1])
	}
}

// 没有故障转移组时,core 给的是空数组(不是 null),存下来要能把旧计划盖掉。
func TestSaveEmptyPlanOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failover.json")
	var withGroup Plan
	if err := json.Unmarshal([]byte(realCorePlan), &withGroup); err != nil {
		t.Fatal(err)
	}
	if err := SavePlan(path, withGroup); err != nil {
		t.Fatal(err)
	}
	if err := SavePlan(path, Plan{}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("空计划应盖掉旧计划,实际 %+v", got)
	}
	// 落盘内容要是 [] 而不是 null
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Fatalf("空计划应写成 [],实际 %q", string(b))
	}
}
