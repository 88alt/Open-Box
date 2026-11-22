package mihomo

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const groupsArgs = `{
 "profile": {"testUrl":"http://cp.cloudflare.com/generate_204","testExpectedStatus":"204","nodeScoreWeights":{"latency":50,"download":30,"jitter":20},
   "routing":{}},
 "nodes": [
  {"type":"trojan","tag":"hk1","server":"a.example.com","server_port":443,"fields":{"password":"p","tls":{"enabled":true,"server_name":"a.example.com"}},"regionName":"HK","subscriptionId":"s1"},
  {"type":"trojan","tag":"jp1","server":"b.example.com","server_port":443,"fields":{"password":"p","tls":{"enabled":true,"server_name":"b.example.com"}},"regionName":"JP","subscriptionId":"s1"}
 ],
 "groups": [
  {"id":"auto","name":"Auto","type":"urltest","mode":"dynamic","keywords":["HK"],"interval":"3m","tolerance":50,"bandwidth":true,"enabled":true},
  {"id":"lb","name":"LB","type":"loadbalance","mode":"dynamic","keywords":[],"interval":"5m","enabled":true},
  {"id":"fo","name":"FO","type":"failover","mode":"static","lanes":[{"id":"l1","name":"L1","members":["hk1"]},{"id":"l2","name":"L2","members":["jp1"]}],"enabled":true},
  {"id":"sel","name":"Sel","type":"selector","mode":"static","members":["Auto","hk1"],"enabled":true}
 ],
 "selections": {"Sel":"hk1"}
}`

func runGroups(t *testing.T, in string) *GroupsResult {
	t.Helper()
	var a any
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatal(err)
	}
	r, err := Groups(a)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", v)
	}
	return m
}

func groupByName(t *testing.T, r *GroupsResult, name string) map[string]any {
	t.Helper()
	for _, g := range r.Groups {
		if m := asMap(t, g); m["name"] == name {
			return m
		}
	}
	t.Fatalf("group %s not found", name)
	return nil
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestGroupsShape(t *testing.T) {
	r := runGroups(t, groupsArgs)
	if len(r.Proxies) != 2 {
		t.Fatalf("proxies: %d", len(r.Proxies))
	}
	auto := groupByName(t, r, "Auto")
	want := `{"expected-status":"204","name":"Auto","openbox":{"bandwidth":true,"node_score_weights":{"download":30,"jitter":20,"latency":50}},"openbox-health-check-interval":"3m","proxies":["hk1"],"tolerance":50,"type":"url-test","url":"http://cp.cloudflare.com/generate_204"}`
	if jsonOf(auto) != want {
		t.Errorf("Auto:\n got  %s\n want %s", jsonOf(auto), want)
	}
	lb := groupByName(t, r, "LB")
	if lb["type"] != "load-balance" || jsonOf(lb["proxies"]) != `["hk1","jp1"]` || asMap(t, lb["openbox"])["bandwidth"] != false {
		t.Errorf("LB: %s", jsonOf(lb))
	}
	if _, has := lb["interval"]; has {
		t.Error("automatic group must not carry interval")
	}
	// failover 在 Mihomo 里是 select,泳道由后端的故障转移管理器切
	fo := groupByName(t, r, "FO")
	if fo["type"] != "select" || jsonOf(fo["proxies"]) != `["hk1","jp1","REJECT"]` || fo["interrupt-exist-connections"] != true {
		t.Errorf("FO: %s", jsonOf(fo))
	}
	if len(r.Failover) != 1 || r.Failover[0].Tag != "FO" {
		t.Errorf("failover: %+v", r.Failover)
	}
	// 选择:Sel 选了 hk1,排第一;没有选择的 select 组用默认
	if sel := groupByName(t, r, "Sel"); jsonOf(sel["proxies"]) != `["hk1","Auto"]` {
		t.Errorf("Sel: %s", jsonOf(sel))
	}
	// 兜底组:默认直连排第一,成员是全部公开组 + 内置
	fb := groupByName(t, r, "其他")
	if jsonOf(fb["proxies"]) != `["DIRECT","Auto","LB","FO","Sel","REJECT"]` {
		t.Errorf("fallback: %s", jsonOf(fb))
	}
	if r.First["其他"] != "DIRECT" || r.First["Sel"] != "hk1" || r.First["Auto"] != "hk1" {
		t.Errorf("first: %v", r.First)
	}
	wantTargets := `{"Auto":"Auto","FO":"FO","LB":"LB","Sel":"Sel","block":"REJECT","direct":"DIRECT","hk1":"hk1","jp1":"jp1","其他":"其他","拒绝":"REJECT","直连":"DIRECT"}`
	if jsonOf(r.Targets) != wantTargets || len(r.Builtins) != 0 {
		t.Errorf("targets/builtins:\n got  %s %v\n want %s", jsonOf(r.Targets), r.Builtins, wantTargets)
	}
}

func TestGroupsSelectionOverridesAndFallsBack(t *testing.T) {
	// 选择指向不存在的成员:回到默认
	r := runGroups(t, `{"profile":{},"nodes":[{"type":"trojan","tag":"hk1","server":"a","server_port":1,"fields":{"password":"p"},"regionName":"HK"}],
	  "groups":[{"id":"sel","name":"Sel","type":"selector","mode":"static","members":["hk1"],"enabled":true}],"selections":{"Sel":"gone","其他":"Sel"}}`)
	if r.First["Sel"] != "hk1" {
		t.Errorf("first: %v", r.First)
	}
	if r.First["其他"] != "Sel" {
		t.Errorf("fallback selection ignored: %v", r.First)
	}
}

func TestGroupsBuiltinNameCollision(t *testing.T) {
	// 节点叫 DIRECT:内置直连改别名,targets 跟着变
	r := runGroups(t, `{"profile":{},"nodes":[{"type":"trojan","tag":"DIRECT","server":"a","server_port":1,"fields":{"password":"p"},"regionName":"HK"}],"groups":[]}`)
	alias := "__openbox_builtin_DIRECT__"
	if r.Builtins["DIRECT"] != alias || r.Targets["direct"] != alias || r.Targets["block"] != "REJECT" || r.Targets["直连"] != alias || r.Targets["DIRECT"] != "DIRECT" {
		t.Errorf("builtins=%v targets=%v", r.Builtins, r.Targets)
	}
}

func TestGroupsWeightsAndBandwidthSwitches(t *testing.T) {
	bad := runGroups(t, `{"profile":{"nodeScoreWeights":{"latency":50,"download":30,"jitter":10}},"nodes":[{"type":"trojan","tag":"hk1","server":"a","server_port":1,"fields":{"password":"p"},"regionName":"HK"}],
	  "groups":[{"id":"a","name":"A","type":"urltest","mode":"dynamic","keywords":[],"interval":"3m","bandwidth":true,"enabled":true}]}`)
	if _, has := asMap(t, groupByName(t, bad, "A")["openbox"])["node_score_weights"]; has {
		t.Error("weights not summing to 100 must be ignored")
	}
	off := runGroups(t, `{"profile":{"disableBandwidth":true},"nodes":[{"type":"trojan","tag":"hk1","server":"a","server_port":1,"fields":{"password":"p"},"regionName":"HK"}],
	  "groups":[{"id":"a","name":"A","type":"urltest","mode":"dynamic","keywords":[],"interval":"3m","bandwidth":true,"enabled":true}]}`)
	if asMap(t, groupByName(t, off, "A")["openbox"])["bandwidth"] != false {
		t.Error("disableBandwidth must turn bandwidth off")
	}
}

// 设了 OPENBOX_MIHOMO_BIN(上游发布包里那份打过补丁的内核)才跑:产出整份交给真实内核做 -t。
func TestGroupsRealMihomoCheck(t *testing.T) {
	bin := os.Getenv("OPENBOX_MIHOMO_BIN")
	if bin == "" {
		t.Skip("OPENBOX_MIHOMO_BIN not set")
	}
	r := runGroups(t, groupsArgs)
	check := func(groups []any) ([]byte, error) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "c.yaml")
		b, _ := json.Marshal(map[string]any{"proxies": r.Proxies, "proxy-groups": groups, "rules": []any{"MATCH,其他"}})
		if err := os.WriteFile(cfg, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return exec.Command(bin, "-d", dir, "-f", cfg, "-t").CombinedOutput()
	}
	if out, err := check(r.Groups); err != nil {
		t.Fatalf("mihomo -t: %v\n%s", err, out)
	}
	// 对照:自动组再写 interval(与 openbox-health-check-interval 互斥),内核必须拒绝,说明 -t 确实在查组
	bad := []any{}
	for _, g := range r.Groups {
		m := map[string]any{}
		for k, v := range g.(map[string]any) {
			m[k] = v
		}
		if m["name"] == "Auto" {
			m["interval"] = 60
		}
		bad = append(bad, m)
	}
	if out, err := check(bad); err == nil {
		t.Fatalf("kernel accepted interval + openbox-health-check-interval:\n%s", out)
	}
}

func TestGroupsRejectCollisionEmptyAndInterval(t *testing.T) {
	r := runGroups(t, `{"profile":{},"nodes":[{"type":"trojan","tag":"REJECT","server":"a","server_port":1,"fields":{"password":"p"},"regionName":"HK"}],
	  "groups":[{"id":"lb","name":"LB","type":"loadbalance","mode":"dynamic","keywords":["ZZZ"],"interval":"5m","enabled":true},
	            {"id":"u","name":"U","type":"urltest","mode":"static","members":["REJECT"],"interval":"abc","enabled":true}]}`)
	alias := "__openbox_builtin_REJECT__"
	if r.Builtins["REJECT"] != alias || r.Targets["block"] != alias || r.Targets["direct"] != "DIRECT" {
		t.Errorf("builtins=%v targets=%v", r.Builtins, r.Targets)
	}
	// 空组:成员换成内置拒绝(别名之后的名字),不能留空 —— 内核要求自动组有明确成员
	if lb := groupByName(t, r, "LB"); jsonOf(lb["proxies"]) != `["`+alias+`"]` {
		t.Errorf("empty group: %s", jsonOf(lb))
	}
	if u := groupByName(t, r, "U"); u["openbox-health-check-interval"] != "3m" {
		t.Errorf("bad interval: %s", jsonOf(u))
	}
	// 内置直连 / 拒绝本身不出现在组里(它们是内核的 DIRECT / REJECT)
	for _, g := range r.Groups {
		if n := asMap(t, g)["name"]; n == "直连" || n == "拒绝" {
			t.Errorf("builtin leaked as group: %v", n)
		}
	}
}
