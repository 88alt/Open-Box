package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 自写测试向量:chain.go 的 resolveChainNodes(链式节点进内核前的核对)。输入自己拟,期望值按下面的规则人工推算,JSON 逐字节比:
//   - 候选只收对象(非数组当没有、非对象项丢掉);与节点 tag、任何组的名字(含停用的)、内置直连 / 拒绝现在的名字重名的候选先剔除,
//     候选之间不互相去重(同名候选的重名由 BuildDetailed 之后的出站重名检查报);
//   - 上游(detour)合格 = 不是自己、不是内置直连 / 拒绝现在的名字、是某个 wireguard 以外的节点(含别的候选)的 tag 或某个对外可见的组;
//     停用 / 因环被丢的组、故障转移的内部子组都不算;
//   - 环 = 候选沿「上游 / 组成员」的边走回自己(组可以把候选当成员:静态组、故障转移页签、负载均衡);
//   - 逐轮核对:每轮按输入顺序找第一个不合格(上游不合格或在环上)的候选剔掉,重新计算组再来一轮,直到没有为止;
//     被连带的(上游被剔)下一轮才剔;幸存者保持输入顺序,内容原样;
//   - 组的生成出错(负载均衡超过 64 个节点)原样往外报。

func chainRun(t *testing.T, cands, nodes, groups string) (string, error) {
	t.Helper()
	parse := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("bad json %s: %v", s, err)
		}
		return v
	}
	out, err := resolveChainNodes(parse(cands), parse(nodes), parse(groups))
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), nil
}

func chainCheck(t *testing.T, cands, nodes, groups, want string) {
	t.Helper()
	got, err := chainRun(t, cands, nodes, groups)
	if err != nil {
		t.Errorf("意外报错 %v\n候选 %s\n节点 %s\n组 %s", err, cands, nodes, groups)
		return
	}
	if got != want {
		t.Errorf("候选 %s\n节点 %s\n组 %s\n得到 %s\n期望 %s", cands, nodes, groups, got, want)
	}
}

func cn(tag, detour string) string {
	return fmt.Sprintf(`{"detour":%q,"tag":%q,"type":"vless"}`, detour, tag) // 键序按字母序,和 json.Marshal 对 map 的输出一致
}

// 带 chain:true 的候选(Node 传进来的链式节点都带这个标记,动态组据此不收它)
func cnc(tag, detour string) string {
	return fmt.Sprintf(`{"chain":true,"detour":%q,"tag":%q,"type":"vless"}`, detour, tag)
}

func cl(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func nodesOf(tags ...string) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = fmt.Sprintf(`{"tag":%q,"type":"vless"}`, t)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestChainInputShapes(t *testing.T) {
	n := nodesOf("A")
	// 候选不是数组 / 数组里没有对象 → 空数组(不是 null)
	for _, c := range []string{`null`, `{"tag":"C","detour":"A"}`, `"x"`, `5`, `[]`, `[1,"x",null,[],true]`} {
		chainCheck(t, c, n, `[]`, `[]`)
	}
	// 非对象项丢掉,对象项照审
	chainCheck(t, `[1,`+cn("C", "A")+`,null,"x"]`, n, `[]`, cl(cn("C", "A")))
	// 节点 / 组不是数组当没有:此时上游无处可找,全剔
	for _, bad := range []string{`null`, `{"a":1}`, `"x"`, `0`} {
		chainCheck(t, cl(cn("C", "A")), bad, `[]`, `[]`)
		chainCheck(t, cl(cn("C", "A")), n, bad, cl(cn("C", "A")))
	}
	// 组是对象 / 字符串都当没有:内置名取默认(直连 / 拒绝),所以上游叫直连的仍被剔
	chainCheck(t, cl(cn("C", "直连")), n, `{"a":1}`, `[]`)
	// 节点里的 null、非对象、没有 tag 的不影响
	chainCheck(t, cl(cn("C", "A")), `[null,5,"x",{"type":"vless"},{"tag":"A","type":"vless"}]`, `[]`, cl(cn("C", "A")))
	// 幸存者内容原样带出(多余字段、键序按 JSON 编码的字母序)
	chainCheck(t, `[{"tag":"C","detour":"A","server":"s","port":1,"x":null}]`, n, `[]`,
		`[{"detour":"A","port":1,"server":"s","tag":"C","x":null}]`)
}

func TestChainDuplicateTags(t *testing.T) {
	// 与节点同名:剔;别的照留
	chainCheck(t, cl(cn("A", "B"), cn("C", "B")), nodesOf("A", "B"), `[]`, cl(cn("C", "B")))
	// 与组同名(含停用的、含内置出站现在的名字)
	g := `[{"id":"s","name":"S","members":["A"]},{"id":"o","name":"Off","enabled":false},{"id":"b","name":"BK"}]`
	chainCheck(t, cl(cn("S", "A"), cn("Off", "A"), cn("BK", "A"), cn("C", "A")), nodesOf("A"), g, cl(cn("C", "A")))
	// 与内置直连 / 拒绝默认名字同名:剔(拒绝被停用也一样)
	chainCheck(t, cl(cn("直连", "A"), cn("拒绝", "A"), cn("C", "A")), nodesOf("A"),
		`[{"id":"builtin-block","name":"拒绝","enabled":false}]`, cl(cn("C", "A")))
	// 组列表里没有内置条目时,默认名字也要认(只靠内置名这一条来剔)
	chainCheck(t, cl(cn("直连", "A"), cn("拒绝", "A"), cn("C", "A")), nodesOf("A"), `[]`, cl(cn("C", "A")))
	// 节点 / 组的 tag 或名字是空的:空串不算「已有名字」,不剔 tag 为空的候选
	chainCheck(t, `[{"detour":"A"}]`, `[{"tag":"","type":"vless"},{"tag":"A","type":"vless"}]`, `[]`, `[{"detour":"A"}]`)
	chainCheck(t, `[{"detour":"A"}]`, nodesOf("A"), `[{"id":"g","name":"","members":["A"]}]`, `[{"detour":"A"}]`)
	// 内置改名后:新名字重名剔,老名字只是普通名字,留下
	chainCheck(t, cl(cn("DIRECT", "A"), cn("直连", "A"), cn("BLOCK", "A"), cn("拒绝", "A")), nodesOf("A"),
		`[{"id":"builtin-direct","name":"DIRECT"},{"id":"builtin-block","name":"BLOCK"}]`, cl(cn("直连", "A"), cn("拒绝", "A")))
	// 候选之间不互相去重:同名的两个都进核对(各自上游合格就都留下)
	chainCheck(t, cl(cn("C", "A"), cn("C", "A")), nodesOf("A"), `[]`, cl(cn("C", "A"), cn("C", "A")))
	// 候选 tag 缺失或不是字符串:按空串算,此时不与任何名字重名,但上游检查照做
	chainCheck(t, `[{"detour":"A"},{"tag":5,"detour":"A"}]`, nodesOf("A"), `[]`, `[{"detour":"A"},{"detour":"A","tag":5}]`)
}

func TestChainUpstreamVectors(t *testing.T) {
	n := nodesOf("A", "B")
	// 上游是节点 / 别的候选 / 对外可见的组:留
	chainCheck(t, cl(cn("C1", "A"), cn("C2", "C1")), n, `[]`, cl(cn("C1", "A"), cn("C2", "C1")))
	chainCheck(t, cl(cn("C", "S")), n, `[{"id":"s","name":"S","members":["A"]}]`, cl(cn("C", "S")))
	// 上游不存在 / 缺失 / 不是字符串 / 空串:剔
	chainCheck(t, cl(cn("C", "nope")), n, `[]`, `[]`)
	chainCheck(t, `[{"tag":"C"},{"tag":"D","detour":5},{"tag":"E","detour":null},{"tag":"F","detour":""}]`, n, `[]`, `[]`)
	// 上游是自己:剔
	chainCheck(t, cl(cn("C", "C")), n, `[]`, `[]`)
	// 上游是内置直连 / 拒绝现在的名字:剔(含内置改名后的新名字;老名字不存在,同样剔)
	chainCheck(t, cl(cn("C1", "直连"), cn("C2", "拒绝"), cn("C3", "A")), n, `[]`, cl(cn("C3", "A")))
	bi := `[{"id":"builtin-direct","name":"DIRECT"},{"id":"builtin-block","name":"BLOCK"}]`
	chainCheck(t, cl(cn("C1", "DIRECT"), cn("C2", "BLOCK"), cn("C3", "直连"), cn("C4", "A")), n, bi, cl(cn("C4", "A")))
	// 节点恰好叫直连:它是节点,也是内置名,当上游仍不行
	chainCheck(t, cl(cn("C", "直连")), nodesOf("A", "直连"), `[]`, `[]`)
	// 上游是 wireguard 节点(内核里是 endpoint,不能当 detour):剔;候选自己是 wireguard 时它也不能当别人的上游
	wg := `[{"tag":"W","type":"wireguard"},{"tag":"A","type":"vless"}]`
	chainCheck(t, cl(cn("C", "W")), wg, `[]`, `[]`)
	chainCheck(t, `[{"tag":"W2","type":"wireguard","detour":"A"},`+cn("C", "W2")+`]`, wg, `[]`, `[{"detour":"A","tag":"W2","type":"wireguard"}]`)
	// wireguard 节点放进组里,组可以当上游
	chainCheck(t, cl(cn("C", "S")), wg, `[{"id":"s","name":"S","members":["W","A"]}]`, cl(cn("C", "S")))
	// 上游是停用的组 / 因环被丢的组 / 故障转移的内部子组:剔
	chainCheck(t, cl(cn("C", "Off")), n, `[{"id":"o","name":"Off","enabled":false,"members":["A"]}]`, `[]`)
	chainCheck(t, cl(cn("C", "X"), cn("D", "A")), n,
		`[{"id":"x","name":"X","members":["Y"]},{"id":"y","name":"Y","members":["X"]}]`, cl(cn("D", "A")))
	chainCheck(t, cl(cn("C", "__fo:f:l")), n,
		`[{"id":"f","name":"F","type":"failover","lanes":[{"id":"l","members":["A","B"]}]}]`, `[]`)
	// 上游是故障转移组本身(对外可见):留
	chainCheck(t, cl(cn("C", "F")), n,
		`[{"id":"f","name":"F","type":"failover","lanes":[{"id":"l","members":["A","B"]}]}]`, cl(cn("C", "F")))
	// 上游大小写 / 空白敏感
	chainCheck(t, cl(cn("C1", "a"), cn("C2", " A"), cn("C3", "A")), n, `[]`, cl(cn("C3", "A")))
}

func TestChainCycleVectors(t *testing.T) {
	n := nodesOf("A")
	// 候选互相当上游:先剔一个,另一个下一轮因上游不存在被连带剔
	chainCheck(t, cl(cn("C1", "C2"), cn("C2", "C1")), n, `[]`, `[]`)
	// 三个成环,另有一个正常的:环里全剔,正常的按输入顺序留
	chainCheck(t, cl(cn("C1", "C2"), cn("D", "A"), cn("C2", "C3"), cn("C3", "C1")), n, `[]`, cl(cn("D", "A")))
	// 通向环但自己不在环上:环里的剔掉以后它的上游没了,也被连带剔
	chainCheck(t, cl(cn("T", "C1"), cn("C1", "C2"), cn("C2", "C1")), n, `[]`, `[]`)
	// 长链没有环:全留
	chainCheck(t, cl(cn("C1", "A"), cn("C2", "C1"), cn("C3", "C2"), cn("C4", "C3"), cn("C5", "C4")), n, `[]`, cl(cn("C1", "A"), cn("C2", "C1"), cn("C3", "C2"), cn("C4", "C3"), cn("C5", "C4")))
	// 长链断在中间(C3 的上游不存在):C3 先剔,C4、C5 逐轮连带剔,前面的留
	chainCheck(t, cl(cn("C1", "A"), cn("C2", "C1"), cn("C3", "nope"), cn("C4", "C3"), cn("C5", "C4")), n, `[]`, cl(cn("C1", "A"), cn("C2", "C1")))
	// 上游不合格的在后、依赖它的在前:一样逐轮剔光
	chainCheck(t, cl(cn("C5", "C4"), cn("C4", "C3"), cn("C3", "nope")), n, `[]`, `[]`)
	// 组把候选当成员、候选又以该组为上游:成环剔
	chainCheck(t, cl(cn("C", "S")), n, `[{"id":"s","name":"S","members":["C","A"]}]`, `[]`)
	// 组把候选当成员,但候选的上游是节点:没有环,留
	chainCheck(t, cl(cn("C", "A")), n, `[{"id":"s","name":"S","members":["C","A"]}]`, cl(cn("C", "A")))
	// 经过两层组:C → G1 → G2 → C
	chainCheck(t, cl(cn("C", "G1")), n,
		`[{"id":"g1","name":"G1","members":["G2"]},{"id":"g2","name":"G2","members":["C"]}]`, `[]`)
	// 两层组但不回到 C:留
	chainCheck(t, cl(cn("C", "G1")), n,
		`[{"id":"g1","name":"G1","members":["G2"]},{"id":"g2","name":"G2","members":["A"]}]`, cl(cn("C", "G1")))
	// 别的候选在环上、自己只是被组带进去:C1 → G(含 C2),C2 → C1:C1 → G → C2 → C1 成环,按顺序 C1 先剔,C2 下一轮因上游不存在也剔
	chainCheck(t, cl(cn("C1", "G"), cn("C2", "C1")), n, `[{"id":"g","name":"G","members":["C2"]}]`, `[]`)
	// 静态组的成员里有一个候选被剔,组本身与别的候选不受影响
	chainCheck(t, cl(cn("Bad", "nope"), cn("Good", "S")), n, `[{"id":"s","name":"S","members":["Bad","A"]}]`, cl(cn("Good", "S")))
	// 故障转移:单节点页签直接引用候选,候选又以该组为上游 → 环
	chainCheck(t, cl(cn("C", "F")), n,
		`[{"id":"f","name":"F","type":"failover","lanes":[{"id":"l","members":["C"]}]}]`, `[]`)
	// 故障转移:多节点页签经内部子组 F → __fo:f:l → C → F → 环
	chainCheck(t, cl(cn("C", "F")), n,
		`[{"id":"f","name":"F","type":"failover","lanes":[{"id":"l","members":["C","A"]}]}]`, `[]`)
	// 故障转移页签没有候选:没有环,留
	chainCheck(t, cl(cn("C", "F")), n,
		`[{"id":"f","name":"F","type":"failover","lanes":[{"id":"l","members":["A"]}]}]`, cl(cn("C", "F")))
	// 负载均衡只接真实节点,候选算节点:L → C,C → L 成环
	chainCheck(t, cl(cn("C", "L")), n,
		`[{"id":"l","name":"L","type":"loadbalance","members":["C","A"]}]`, `[]`)
	// 负载均衡不含候选时留
	chainCheck(t, cl(cn("C", "L")), n,
		`[{"id":"l","name":"L","type":"loadbalance","members":["A"]}]`, cl(cn("C", "L")))
	// urltest 组同理
	chainCheck(t, cl(cn("C", "U")), n,
		`[{"id":"u","name":"U","type":"urltest","members":["C"]}]`, `[]`)
	// 动态组不收链式节点(chain 为真):候选以动态组为上游合法,组里没有它
	chainCheck(t, cl(cnc("C", "D")), nodesOf("A", "B"),
		`[{"id":"d","name":"D","mode":"dynamic"}]`, cl(cnc("C", "D")))
	// 没带 chain 标记的候选在动态组看来就是普通节点:D → C,C → D 成环剔
	chainCheck(t, cl(cn("C", "D")), nodesOf("A", "B"),
		`[{"id":"d","name":"D","mode":"dynamic"}]`, `[]`)
	// 环上的候选被剔后,以前被它占着的组重新计算:另一个候选以这个组为上游仍然合格
	chainCheck(t, cl(cn("Loop", "S"), cn("Ok", "S")), n,
		`[{"id":"s","name":"S","members":["Loop","A"]}]`, cl(cn("Ok", "S")))
}

func TestChainGroupErrorPropagates(t *testing.T) {
	// 静态负载均衡组含 65 个真实节点:生成出错,原样往外报,不当成「没有候选被剔」
	tags := make([]string, 65)
	members := make([]string, 65)
	for i := range tags {
		tags[i] = fmt.Sprintf("N%02d", i)
		members[i] = fmt.Sprintf("%q", tags[i])
	}
	g := `[{"id":"l","name":"L","type":"loadbalance","members":[` + strings.Join(members, ",") + `]}]`
	_, err := chainRun(t, cl(cn("C", "N00")), nodesOf(tags...), g)
	if err == nil || err.Error() != "load balance supports at most 64 nodes" {
		t.Errorf("该报「load balance supports at most 64 nodes」,得到 %v", err)
	}
	// 没有候选时也会生成一次组,同样报错
	if _, err := chainRun(t, `[]`, nodesOf(tags...), g); err == nil {
		t.Errorf("没有候选时也应报错")
	}
	// 64 个刚好不报
	g64 := `[{"id":"l","name":"L","type":"loadbalance","members":[` + strings.Join(members[:64], ",") + `]}]`
	chainCheck(t, cl(cn("C", "N00")), nodesOf(tags[:64]...), g64, cl(cn("C", "N00")))
	// 候选算进节点表:63 个节点加一个候选当成员,共 64,不报;再多一个就报
	g2 := `[{"id":"l","name":"L","type":"loadbalance","members":[` + strings.Join(members[:63], ",") + `,"C"]}]`
	chainCheck(t, cl(cn("C", "N00")), nodesOf(tags[:63]...), g2, cl(cn("C", "N00")))
	g3 := `[{"id":"l","name":"L","type":"loadbalance","members":[` + strings.Join(members[:64], ",") + `,"C"]}]`
	if _, err := chainRun(t, cl(cn("C", "N00")), nodesOf(tags[:64]...), g3); err == nil {
		t.Errorf("64 个节点加候选共 65 个成员应报错")
	}
}

func TestChainNoWriteThrough(t *testing.T) {
	cands := []any{map[string]any{"tag": "C", "detour": "A"}, map[string]any{"tag": "D", "detour": "nope"}}
	nodes := []any{map[string]any{"tag": "A", "type": "vless"}}
	groups := []any{map[string]any{"id": "s", "name": "S", "members": []any{"C", "A"}}}
	before := func() string {
		b, _ := json.Marshal([]any{cands, nodes, groups})
		return string(b)
	}
	snap := before()
	out, err := resolveChainNodes(cands, nodes, groups)
	if err != nil || len(out) != 1 {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if before() != snap {
		t.Errorf("入参被改写:\n%s\n%s", snap, before())
	}
	if len(cands) != 2 {
		t.Errorf("候选切片被截短")
	}
	// 两次调用结果相同
	out2, _ := resolveChainNodes(cands, nodes, groups)
	a, _ := json.Marshal(out)
	b, _ := json.Marshal(out2)
	if string(a) != string(b) {
		t.Errorf("两次调用不同: %s %s", a, b)
	}
}
