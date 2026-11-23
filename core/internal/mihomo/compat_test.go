package mihomo

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 自写向量(期望值人工写出):mihomoCompatibility 的 problems 清单。
// 好节点用 trojan,坏节点用 proxies_test 里已确认 Mihomo 表达不了的三种写法:vmess + quic 传输、socks4、shadow-tls 插件。

const (
	nGood1  = `{"type":"trojan","tag":"hk1","server":"a.example.com","server_port":443,"fields":{"password":"p","tls":{"enabled":true,"server_name":"a.example.com"}},"regionName":"HK"}`
	nGood2  = `{"type":"trojan","tag":"jp1","server":"b.example.com","server_port":443,"fields":{"password":"p","tls":{"enabled":true,"server_name":"b.example.com"}},"regionName":"JP"}`
	nQuic   = `{"type":"vmess","tag":"q","server":"s","server_port":1,"fields":{"uuid":"u","transport":{"type":"quic"}}}`
	nSocks4 = `{"type":"socks","tag":"k","server":"s","server_port":1,"fields":{"version":"4"}}`
	nPlugin = `{"type":"shadowsocks","tag":"p","server":"s","server_port":1,"fields":{"method":"m","password":"p","plugin":"shadow-tls"}}`
)

func compat(t *testing.T, in string) []string {
	t.Helper()
	var a any
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatal(err)
	}
	return Compatibility(a)
}

func compatArgs(nodes, groups, chain string) string {
	return fmt.Sprintf(`{"profile":{"routing":{}},"nodes":[%s],"groups":[%s],"chainNodes":[%s],"selections":{}}`, nodes, groups, chain)
}

func TestCompatibilityClean(t *testing.T) {
	groups := `{"id":"auto","name":"Auto","type":"urltest","mode":"dynamic","keywords":[],"interval":"3m","enabled":true},
{"id":"sel","name":"Sel","type":"selector","mode":"static","members":["Auto","hk1"],"enabled":true}`
	got := compat(t, compatArgs(nGood1+","+nGood2, groups, ""))
	if b, _ := json.Marshal(got); string(b) != "[]" {
		t.Fatalf("clean profile should give [], got %s", b)
	}
}

func TestCompatibilityEmptyInputsGiveEmptyArrayNotNull(t *testing.T) {
	for _, in := range []string{`null`, `{}`, `[]`, `{"nodes":"x","groups":0,"chainNodes":5}`, `{"nodes":null}`} {
		got := compat(t, in)
		if b, _ := json.Marshal(got); string(b) != "[]" {
			t.Errorf("%s: want [] got %s", in, b)
		}
	}
}

// 翻不了的节点要逐个列全(顺序同输入),不是遇到第一个就停;好节点不出现。
func TestCompatibilityListsEveryBadNodeInOrder(t *testing.T) {
	got := compat(t, compatArgs(nQuic+","+nGood1+","+nSocks4+","+nPlugin, "", ""))
	want := []string{"transport quic", "socks version 4", "plugin shadow-tls"}
	tags := []string{"q", "k", "p"}
	if len(got) != 3 {
		t.Fatalf("want 3 problems, got %d: %v", len(got), got)
	}
	for i := range want {
		prefix := "节点「" + tags[i] + "」Mihomo 不支持:"
		if !strings.HasPrefix(got[i], prefix) || !strings.Contains(got[i], want[i]) {
			t.Errorf("problem %d = %q, want prefix %q containing %q", i, got[i], prefix, want[i])
		}
	}
}

// 组里引用了翻不了的节点:节点本身已经报了,组不再多报一条(静态成员里查不到的节点会被丢掉)。
func TestCompatibilityGroupReferencingBadNodeNotReportedTwice(t *testing.T) {
	groups := `{"id":"sel","name":"Sel","type":"selector","mode":"static","members":["q","hk1"],"enabled":true},
{"id":"auto","name":"Auto","type":"urltest","mode":"dynamic","keywords":[],"interval":"3m","enabled":true}`
	got := compat(t, compatArgs(nQuic+","+nGood1, groups, ""))
	if len(got) != 1 || !strings.HasPrefix(got[0], "节点「q」") {
		t.Fatalf("want exactly the node problem, got %v", got)
	}
}

// 链式节点:通过核对的才逐个翻;核对不过的(上游不存在)本来就不会进配置,不报。
func TestCompatibilityChainNodes(t *testing.T) {
	chainBad := `{"type":"socks","tag":"cb","detour":"hk1","server":"s","server_port":1,"fields":{"version":"4"},"chain":true}`
	chainGood := `{"type":"trojan","tag":"cg","detour":"hk1","server":"c.example.com","server_port":443,"fields":{"password":"p"},"chain":true}`
	chainOrphan := `{"type":"socks","tag":"co","detour":"nope","server":"s","server_port":1,"fields":{"version":"4"},"chain":true}`

	got := compat(t, compatArgs(nGood1, "", chainBad+","+chainGood))
	if len(got) != 1 || !strings.HasPrefix(got[0], "节点「cb」Mihomo 不支持:") || !strings.Contains(got[0], "socks version 4") {
		t.Fatalf("chain: got %v", got)
	}
	got = compat(t, compatArgs(nGood1, "", chainOrphan))
	if len(got) != 0 {
		t.Fatalf("orphan chain node never reaches the config, got %v", got)
	}
	// 普通节点和链式节点的问题一起列,普通节点在前
	got = compat(t, compatArgs(nSocks4+","+nGood1, "", chainBad))
	if len(got) != 2 || !strings.HasPrefix(got[0], "节点「k」") || !strings.HasPrefix(got[1], "节点「cb」") {
		t.Fatalf("mixed: got %v", got)
	}
}

// 节点组本身生成不了(负载均衡超过 64 个节点):只报一条「节点组:」,不因为链式核对和组生成各报一遍。
func TestCompatibilityGroupBuildErrorReportedOnce(t *testing.T) {
	var nodes []string
	for i := 0; i < 65; i++ {
		nodes = append(nodes, fmt.Sprintf(`{"type":"trojan","tag":"n%d","server":"a.example.com","server_port":443,"fields":{"password":"p"}}`, i))
	}
	groups := `{"id":"lb","name":"LB","type":"loadbalance","mode":"dynamic","keywords":[],"interval":"5m","enabled":true}`
	got := compat(t, compatArgs(strings.Join(nodes, ","), groups, ""))
	if len(got) != 1 || !strings.HasPrefix(got[0], "节点组:") {
		t.Fatalf("want one 节点组 problem, got %v", got)
	}
}

// 节点组问题和节点问题并存时都要列出(节点问题在前)。
func TestCompatibilityNodeAndGroupProblemsTogether(t *testing.T) {
	var nodes []string
	for i := 0; i < 65; i++ {
		nodes = append(nodes, fmt.Sprintf(`{"type":"trojan","tag":"n%d","server":"a.example.com","server_port":443,"fields":{"password":"p"}}`, i))
	}
	groups := `{"id":"lb","name":"LB","type":"loadbalance","mode":"dynamic","keywords":[],"interval":"5m","enabled":true}`
	got := compat(t, compatArgs(nSocks4+","+strings.Join(nodes, ","), groups, ""))
	if len(got) != 2 || !strings.HasPrefix(got[0], "节点「k」") || !strings.HasPrefix(got[1], "节点组:") {
		t.Fatalf("got %v", got)
	}
}

// 不写穿入参。
func TestCompatibilityDoesNotMutateArgs(t *testing.T) {
	in := compatArgs(nQuic+","+nGood1, `{"id":"auto","name":"Auto","type":"urltest","mode":"dynamic","keywords":[],"enabled":true}`, "")
	var a, b any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal([]byte(in), &b)
	Compatibility(a)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatalf("args mutated:\n%s\n%s", x, y)
	}
}
