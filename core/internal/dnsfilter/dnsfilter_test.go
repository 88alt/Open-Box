package dnsfilter

import (
	"reflect"
	"testing"
)

// 自写测试向量:期望值按 sing-box DNS 规则的字段定义(rule_set / invert / logical and /
// predefined + rcode)与本包注释写明的语义人工核对,不依赖 engine 基线或官方 openbox-core 输出。

type M = map[string]any

const notReadyMsg = "域名过滤名单尚未准备好,请更新名单后应用 DNS 设置"

func on() M { return M{"dns": M{"filter": M{"enabled": true}}} }

func art(extra M) M {
	a := M{"key": "k1"}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

func nx(parts ...any) M {
	if len(parts) == 1 {
		m := M{}
		for k, v := range parts[0].(M) {
			m[k] = v
		}
		m["action"], m["rcode"] = "predefined", "NXDOMAIN"
		return m
	}
	return M{"type": "logical", "mode": "and", "rules": parts, "action": "predefined", "rcode": "NXDOMAIN"}
}

func TestDisabledReturnsEmpty(t *testing.T) {
	profiles := []any{
		nil, M{}, M{"dns": nil}, M{"dns": M{}}, M{"dns": M{"filter": nil}},
		M{"dns": M{"filter": M{}}}, M{"dns": M{"filter": M{"enabled": false}}},
		M{"dns": M{"filter": M{"enabled": float64(0)}}}, M{"dns": M{"filter": M{"enabled": ""}}},
		M{"dns": M{"filter": M{"enabled": nil}}},
	}
	for _, p := range profiles {
		// 关闭时不看产物,也不看 key,哪怕产物是脏数据
		res, err := Build(p, "garbage", nil)
		if err != nil {
			t.Fatalf("%v: 不应报错: %v", p, err)
		}
		if res.Rules == nil || res.Sets == nil || len(res.Rules) != 0 || len(res.Sets) != 0 {
			t.Errorf("%v: want 非 nil 空切片,got %+v", p, res)
		}
	}
}

func TestEnabledTruthyValues(t *testing.T) {
	for _, en := range []any{true, "yes", float64(1), M{}, []any{}} {
		p := M{"dns": M{"filter": M{"enabled": en}}}
		if _, err := Build(p, art(nil), "k1"); err != nil {
			t.Errorf("enabled=%v 应视为开启且就绪,err=%v", en, err)
		}
		if _, err := Build(p, nil, "k1"); err == nil {
			t.Errorf("enabled=%v 开启后没有产物应报未就绪", en)
		}
	}
}

func TestNotReady(t *testing.T) {
	cases := []struct {
		name     string
		artifact any
		expected any
	}{
		{"产物 nil", nil, "k1"},
		{"产物 false", false, "k1"},
		{"产物 0", float64(0), "k1"},
		{"产物缺 key", M{"blocks": []any{}}, "k1"},
		{"key 不是字符串", M{"key": float64(1)}, "k1"},
		{"key 是 null", M{"key": nil}, "k1"},
		{"期望 key 缺失", art(nil), nil},
		{"期望 key 不是字符串", art(nil), float64(1)},
		{"key 对不上", art(nil), "k2"},
		{"大小写不同", M{"key": "K1"}, "k1"},
	}
	for _, c := range cases {
		res, err := Build(on(), c.artifact, c.expected)
		if err == nil || err.Error() != notReadyMsg || err != ErrNotReady {
			t.Errorf("%s: want ErrNotReady, got err=%v res=%+v", c.name, err, res)
		}
	}
}

func TestEmptyKeyMatchesEmpty(t *testing.T) {
	// 两边都是空字符串也算对得上(只比较,不要求非空)
	if _, err := Build(on(), M{"key": ""}, ""); err != nil {
		t.Errorf("空 key 对空 key 应通过: %v", err)
	}
}

func TestListFieldMustBeArray(t *testing.T) {
	for _, f := range []string{"sets", "blocks", "allow", "allowImportant", "userAllow"} {
		for _, bad := range []any{nil, "x", float64(1), true, M{}} {
			_, err := Build(on(), art(M{f: bad}), "k1")
			want := "域名过滤产物的 " + f + " 不是数组"
			if err == nil || err.Error() != want {
				t.Errorf("%s=%v: want %q, got %v", f, bad, want, err)
			}
		}
		// 缺失(没有这个键)按空数组
		if _, err := Build(on(), art(nil), "k1"); err != nil {
			t.Errorf("缺 %s 应按空数组,err=%v", f, err)
		}
	}
	// 多个字段同时坏:先报 sets,再 blocks,再 allow,再 allowImportant,最后 userAllow
	order := []string{"sets", "blocks", "allow", "allowImportant", "userAllow"}
	bad := M{}
	for i := len(order) - 1; i >= 0; i-- {
		bad[order[i]] = "x"
		_, err := Build(on(), art(bad), "k1")
		if want := "域名过滤产物的 " + order[i] + " 不是数组"; err == nil || err.Error() != want {
			t.Errorf("校验顺序:want %q, got %v", want, err)
		}
	}
}

func TestMinimalReady(t *testing.T) {
	res, err := Build(on(), art(nil), "k1")
	if err != nil || res.Rules == nil || res.Sets == nil || len(res.Rules) != 0 || len(res.Sets) != 0 {
		t.Errorf("无 blocks:want 非 nil 空切片,got %+v err=%v", res, err)
	}
}

func TestSetsPassThrough(t *testing.T) {
	sets := []any{M{"tag": "b1", "type": "local", "format": "binary", "path": "/p/b1.srs"}, M{"tag": "a1"}}
	res, _ := Build(on(), art(M{"sets": sets}), "k1")
	if !reflect.DeepEqual(res.Sets, sets) {
		t.Errorf("sets 应原样带出: %#v", res.Sets)
	}
}

func TestRulesSingleBlockNoAllow(t *testing.T) {
	res, _ := Build(on(), art(M{"blocks": []any{M{"tag": "b1", "important": false}}}), "k1")
	want := []any{nx(M{"rule_set": []any{"b1"}})}
	if !reflect.DeepEqual(res.Rules, want) {
		t.Errorf("got %#v\nwant %#v", res.Rules, want)
	}
}

func TestRulesAllowListsByImportance(t *testing.T) {
	a := art(M{
		"blocks":         []any{M{"tag": "b-imp", "important": true}, M{"tag": "b-norm", "important": false}},
		"allow":          []any{"al1", "al2"},
		"allowImportant": []any{"ai1"},
		"userAllow":      []any{"ua1"},
	})
	res, _ := Build(on(), a, "k1")
	want := []any{
		// 重要的封锁:只排除 userAllow + allowImportant(顺序固定:先 userAllow)
		nx(M{"rule_set": []any{"b-imp"}}, M{"rule_set": []any{"ua1", "ai1"}, "invert": true}),
		// 普通封锁:再加上 allow
		nx(M{"rule_set": []any{"b-norm"}}, M{"rule_set": []any{"ua1", "ai1", "al1", "al2"}, "invert": true}),
	}
	if !reflect.DeepEqual(res.Rules, want) {
		t.Errorf("got %#v\nwant %#v", res.Rules, want)
	}
}

func TestRulesOnlyAllowAffectsNormal(t *testing.T) {
	// 只有 allow(普通放行):重要封锁没有排除项,单条形态;普通封锁带排除
	a := art(M{
		"blocks": []any{M{"tag": "i", "important": true}, M{"tag": "n", "important": false}},
		"allow":  []any{"al"},
	})
	res, _ := Build(on(), a, "k1")
	want := []any{
		nx(M{"rule_set": []any{"i"}}),
		nx(M{"rule_set": []any{"n"}}, M{"rule_set": []any{"al"}, "invert": true}),
	}
	if !reflect.DeepEqual(res.Rules, want) {
		t.Errorf("got %#v\nwant %#v", res.Rules, want)
	}
}

func TestRulesOrderImportantFirst(t *testing.T) {
	a := art(M{"blocks": []any{
		M{"tag": "n1", "important": false},
		M{"tag": "i1", "important": true},
		M{"tag": "n2", "important": false},
		M{"tag": "i2", "important": true},
	}})
	res, _ := Build(on(), a, "k1")
	var tags []string
	for _, r := range res.Rules {
		tags = append(tags, r.(M)["rule_set"].([]any)[0].(string))
	}
	if want := []string{"i1", "i2", "n1", "n2"}; !reflect.DeepEqual(tags, want) {
		t.Errorf("顺序 = %v, want %v(重要在前,同类保持原序)", tags, want)
	}
}

func TestBlockImportantMustBeBool(t *testing.T) {
	// important 不是布尔(缺失 / null / 字符串 / 数字)的封锁整条跳过,既不算重要也不算普通
	a := art(M{"blocks": []any{
		M{"tag": "missing"},
		M{"tag": "null", "important": nil},
		M{"tag": "str", "important": "true"},
		M{"tag": "num", "important": float64(1)},
		M{"tag": "ok", "important": false},
		"not-an-object",
		nil,
	}})
	res, _ := Build(on(), a, "k1")
	want := []any{nx(M{"rule_set": []any{"ok"}})}
	if !reflect.DeepEqual(res.Rules, want) {
		t.Errorf("got %#v\nwant %#v", res.Rules, want)
	}
}

func TestSingleRuleIsCopyNotAlias(t *testing.T) {
	// 单条形态是拷贝:往生成规则里加 action / rcode 不能污染调用方传入的 parts
	b := M{"tag": "b1", "important": true}
	a := art(M{"blocks": []any{b}})
	if _, err := Build(on(), a, "k1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b, M{"tag": "b1", "important": true}) {
		t.Errorf("输入的 block 被改动: %#v", b)
	}
}
