package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type buildCase struct {
	Args  json.RawMessage `json:"args"`
	Want  json.RawMessage `json:"want"`
	Error string          `json:"error"`
}

// 金标准:同一份入参喂基线 engine/config.mjs 的 buildConfigDetailed 得到的 {config, dnsRuleOwners, directHosts, failover}
// (gen-config-golden.mjs 生成)。
func TestBuildDetailedGolden(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/config_golden.json")
	if err != nil {
		t.Skip("没有金标准文件:", err)
	}
	var g struct {
		Cases []buildCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Cases) == 0 {
		t.Fatal("金标准是空的")
	}
	bad, ran, skipped := 0, 0, 0
	for i, c := range g.Cases {
		// 有意的差异(2026-11-25):前置自定义分流里「引用了规则集、出口又不在出站表里」的行不再写进
		// DNS 规则。基线会写,而连接侧从来就跳过这些行——两边口径不一致时,DNS 会引用路由里没声明的
		// 规则集,sing-box 1.14 起判整份配置非法。这类用例的期望值是旧行为,不再逐条比对;新行为由
		// build_customrules_vectors_test.go 与 internal/routing/customrules_test.go 的自写向量覆盖。
		if c.Error == "" && hasDanglingRuleSetRule(c.Args, c.Want) {
			skipped++
			continue
		}
		var args any
		if err := json.Unmarshal(c.Args, &args); err != nil {
			t.Fatal(err)
		}
		got, err := BuildDetailed(args)
		ran++
		if c.Error != "" {
			if err == nil || err.Error() != c.Error {
				t.Errorf("case %d: 期望报错 %q,实际 %v", i, c.Error, err)
				bad++
			}
		} else {
			if err != nil {
				t.Errorf("case %d: 意外报错 %v", i, err)
				bad++
			} else {
				var want any
				_ = json.Unmarshal(c.Want, &want)
				// flipFlags、nativeBypass 是自研多带的键,基线没有;金标准文件保持只读,所以比对前去掉(内容分别由 build_flip_vectors_test.go、build_nativebypass_vectors_test.go 自写向量覆盖)。
				delete(got, "flipFlags")
				delete(got, "nativeBypass")
				if !reflect.DeepEqual(toAny(got), want) {
					bad++
					if bad <= 3 {
						t.Errorf("case %d: 输出不一致\n%s", i, firstDiff("", toAny(got), want))
					}
				}
			}
		}
		if bad > 25 {
			t.Fatalf("不一致太多,停止(已跑 %d 条)", ran)
		}
	}
	t.Logf("共 %d 条,不一致 %d(另有 %d 条含「引用规则集 + 出口不认识」的自定义行,按有意差异跳过)", len(g.Cases), bad, skipped)
}

// jsonTruthy 是 JS 的真假判断(与 core 里 jsx.Truthy 同口径),只给上面的跳过判定用。
func jsonTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return true
	case map[string]any:
		return true
	}
	return true
}

// hasDanglingRuleSetRule 判断用例的自定义分流里有没有「引用规则集、出口又不在出站表里」的行。
// 出站表取自基线产出的 outbounds / endpoints;direct / block 是内置记号(customOutboundTag 会换成当前
// 名字),算认识。只用于跳过金标准里有意的差异,不参与实现。
func hasDanglingRuleSetRule(argsRaw, wantRaw json.RawMessage) bool {
	var args struct {
		Profile struct {
			DNS struct {
				Split any `json:"split"`
			} `json:"dns"`
			Routing struct {
				Custom struct {
					Rules []struct {
						Type     string `json:"type"`
						Outbound string `json:"outbound"`
					} `json:"rules"`
				} `json:"custom"`
			} `json:"routing"`
		} `json:"profile"`
	}
	if json.Unmarshal(argsRaw, &args) != nil {
		return false
	}
	var want struct {
		Config struct {
			Outbounds []struct {
				Tag string `json:"tag"`
			} `json:"outbounds"`
			Endpoints []struct {
				Tag string `json:"tag"`
			} `json:"endpoints"`
		} `json:"config"`
	}
	if json.Unmarshal(wantRaw, &want) != nil {
		return false
	}
	// split DNS 没开时 BuildDNS 走「只答本地域名」那条早退分支,前置自定义分流根本不写规则,不会有差异。
	if !jsonTruthy(args.Profile.DNS.Split) {
		return false
	}
	known := map[string]bool{"direct": true, "block": true}
	for _, o := range want.Config.Outbounds {
		known[o.Tag] = true
	}
	for _, o := range want.Config.Endpoints {
		known[o.Tag] = true
	}
	for _, r := range args.Profile.Routing.Custom.Rules {
		switch r.Type {
		case "geosite", "ruleset", "ruleUrl", "ruleurl":
		default:
			continue
		}
		if r.Outbound != "" && !known[r.Outbound] {
			return true
		}
	}
	return false
}

// firstDiff 找出第一处不一致的路径,方便定位。
func firstDiff(path string, got, want any) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return path + ": 类型不同"
		}
		for k := range w {
			if _, has := g[k]; !has {
				return path + "." + k + ": 缺少"
			}
			if d := firstDiff(path+"."+k, g[k], w[k]); d != "" {
				return d
			}
		}
		for k := range g {
			if _, has := w[k]; !has {
				return path + "." + k + ": 多余"
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok {
			return path + ": 类型不同"
		}
		if len(g) != len(w) {
			gj, _ := json.Marshal(g)
			wj, _ := json.Marshal(w)
			return path + ": 长度 " + itoa(len(g)) + " vs " + itoa(len(w)) + "\n got " + string(gj) + "\nwant " + string(wj)
		}
		for i := range w {
			if d := firstDiff(path+"["+itoa(i)+"]", g[i], w[i]); d != "" {
				return d
			}
		}
		return ""
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		return path + ": got " + string(gj) + " want " + string(wj)
	}
	return ""
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
