package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 自写测试向量:tunoptions.go(tun 接口的底层参数:stack / mtu / tcpMss)。期望值按下面的规则人工推算,
// 不取自别的程序的输出:
//   - profile 不是对象、profile.tun 不是对象(null / 数组 / 字符串)都按「什么都没写」处理;
//   - stack 只认恰好是字符串 mixed / gvisor / system(区分大小写、不去空白),其余一律 mixed;
//   - mtu 取值 0 或 [1280, 65535] 内的整数(1280 是 IPv6 要求的最小 MTU,65535 是 IP 包长上限);
//     tcpMss 取值 0 或 [536, 65495] 内的整数(536 = 576 - 40,65495 = 65535 - 40);
//     字符串、布尔、null、数组、小数、越界一律当 0(= 不写);JSON 里的数字都是 float64,1.5e3 即 1500。

func vecJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("测试数据不是合法 JSON:%s:%v", s, err)
	}
	return v
}

func TestTunConsts(t *testing.T) {
	if TunInterfaceName != "openbox-tun" {
		t.Errorf("接口名 %q", TunInterfaceName)
	}
	if TunMTUMin != 1280 || TunMTUMax != 65535 {
		t.Errorf("MTU 范围 %d~%d", TunMTUMin, TunMTUMax)
	}
	if TunMSSMin != 536 || TunMSSMax != 65495 {
		t.Errorf("MSS 范围 %d~%d", TunMSSMin, TunMSSMax)
	}
	if want := []string{"mixed", "gvisor", "system"}; !reflect.DeepEqual(TunStacks, want) {
		t.Errorf("TunStacks=%v", TunStacks)
	}
}

func TestTunStackVectors(t *testing.T) {
	for _, c := range []struct{ profile, want string }{
		// profile / tun 的形状不对:都是默认
		{`null`, "mixed"},
		{`{}`, "mixed"},
		{`[]`, "mixed"},
		{`"gvisor"`, "mixed"},
		{`5`, "mixed"},
		{`true`, "mixed"},
		{`{"tun":null}`, "mixed"},
		{`{"tun":[]}`, "mixed"},
		{`{"tun":["gvisor"]}`, "mixed"},
		{`{"tun":"gvisor"}`, "mixed"},
		{`{"tun":{}}`, "mixed"},
		// 三个合法值
		{`{"tun":{"stack":"mixed"}}`, "mixed"},
		{`{"tun":{"stack":"gvisor"}}`, "gvisor"},
		{`{"tun":{"stack":"system"}}`, "system"},
		// 严格相等:大小写、空白、前缀、相近写法都不认
		{`{"tun":{"stack":"GVISOR"}}`, "mixed"},
		{`{"tun":{"stack":"Gvisor"}}`, "mixed"},
		{`{"tun":{"stack":"System"}}`, "mixed"},
		{`{"tun":{"stack":" system"}}`, "mixed"},
		{`{"tun":{"stack":"system "}}`, "mixed"},
		{`{"tun":{"stack":"gvisorx"}}`, "mixed"},
		{`{"tun":{"stack":"gvis"}}`, "mixed"},
		{`{"tun":{"stack":""}}`, "mixed"},
		{`{"tun":{"stack":"lwip"}}`, "mixed"},
		// 非字符串
		{`{"tun":{"stack":null}}`, "mixed"},
		{`{"tun":{"stack":1}}`, "mixed"},
		{`{"tun":{"stack":true}}`, "mixed"},
		{`{"tun":{"stack":["system"]}}`, "mixed"},
		{`{"tun":{"stack":{"system":1}}}`, "mixed"},
		// 键名:只认 tun.stack,别处的 stack 不算
		{`{"stack":"system"}`, "mixed"},
		{`{"tun":{"Stack":"system"}}`, "mixed"},
		{`{"TUN":{"stack":"system"}}`, "mixed"},
		// 同一个 tun 里别的键不影响
		{`{"tun":{"mtu":1500,"tcpMss":1400,"stack":"system"}}`, "system"},
		{`{"tun":{"mtu":1500,"stack":"nope"},"stack":"system"}`, "mixed"},
	} {
		if got := TunStack(vecJSON(t, c.profile)); got != c.want {
			t.Errorf("TunStack(%s)=%q 要 %q", c.profile, got, c.want)
		}
	}
}

func TestTunMTUVectors(t *testing.T) {
	for _, c := range []struct {
		profile string
		want    int
	}{
		// 形状不对
		{`null`, 0},
		{`{}`, 0},
		{`[]`, 0},
		{`"1500"`, 0},
		{`{"tun":null}`, 0},
		{`{"tun":[]}`, 0},
		{`{"tun":[{"mtu":1500}]}`, 0},
		{`{"tun":"mtu"}`, 0},
		{`{"tun":{}}`, 0},
		// 0 = 不写;边界
		{`{"tun":{"mtu":0}}`, 0},
		{`{"tun":{"mtu":-0}}`, 0},
		{`{"tun":{"mtu":1279}}`, 0},
		{`{"tun":{"mtu":1280}}`, 1280},
		{`{"tun":{"mtu":1281}}`, 1281},
		{`{"tun":{"mtu":1500}}`, 1500},
		{`{"tun":{"mtu":9000}}`, 9000},
		{`{"tun":{"mtu":65534}}`, 65534},
		{`{"tun":{"mtu":65535}}`, 65535},
		{`{"tun":{"mtu":65536}}`, 0},
		{`{"tun":{"mtu":1}}`, 0},
		{`{"tun":{"mtu":-1}}`, 0},
		{`{"tun":{"mtu":-1500}}`, 0},
		{`{"tun":{"mtu":1000}}`, 0},
		{`{"tun":{"mtu":100000}}`, 0},
		{`{"tun":{"mtu":1e20}}`, 0},
		// 数字的不同写法:只看值
		{`{"tun":{"mtu":1500.0}}`, 1500},
		{`{"tun":{"mtu":1.5e3}}`, 1500},
		{`{"tun":{"mtu":15E2}}`, 1500},
		// 小数不是整数
		{`{"tun":{"mtu":1500.5}}`, 0},
		{`{"tun":{"mtu":1280.000001}}`, 0},
		{`{"tun":{"mtu":65534.9}}`, 0},
		// 非数字:字符串、布尔、null、数组、对象都不收
		{`{"tun":{"mtu":"1500"}}`, 0},
		{`{"tun":{"mtu":" 1500 "}}`, 0},
		{`{"tun":{"mtu":"0"}}`, 0},
		{`{"tun":{"mtu":true}}`, 0},
		{`{"tun":{"mtu":false}}`, 0},
		{`{"tun":{"mtu":null}}`, 0},
		{`{"tun":{"mtu":[1500]}}`, 0},
		{`{"tun":{"mtu":{"v":1500}}}`, 0},
		// 键名与互不串
		{`{"mtu":1500}`, 0},
		{`{"tun":{"MTU":1500}}`, 0},
		{`{"tun":{"tcpMss":1400}}`, 0},
		{`{"tun":{"stack":"system","mtu":1400,"tcpMss":1000}}`, 1400},
	} {
		if got := TunMTU(vecJSON(t, c.profile)); got != c.want {
			t.Errorf("TunMTU(%s)=%d 要 %d", c.profile, got, c.want)
		}
	}
}

func TestTunTCPMssVectors(t *testing.T) {
	for _, c := range []struct {
		profile string
		want    int
	}{
		{`null`, 0},
		{`{}`, 0},
		{`[]`, 0},
		{`{"tun":null}`, 0},
		{`{"tun":[]}`, 0},
		{`{"tun":{}}`, 0},
		{`{"tun":{"tcpMss":0}}`, 0},
		{`{"tun":{"tcpMss":535}}`, 0},
		{`{"tun":{"tcpMss":536}}`, 536},
		{`{"tun":{"tcpMss":537}}`, 537},
		{`{"tun":{"tcpMss":1360}}`, 1360},
		{`{"tun":{"tcpMss":1460}}`, 1460},
		{`{"tun":{"tcpMss":65494}}`, 65494},
		{`{"tun":{"tcpMss":65495}}`, 65495},
		{`{"tun":{"tcpMss":65496}}`, 0},
		{`{"tun":{"tcpMss":65535}}`, 0},
		{`{"tun":{"tcpMss":1}}`, 0},
		{`{"tun":{"tcpMss":-536}}`, 0},
		{`{"tun":{"tcpMss":1e20}}`, 0},
		{`{"tun":{"tcpMss":1460.0}}`, 1460},
		{`{"tun":{"tcpMss":1.46e3}}`, 1460},
		{`{"tun":{"tcpMss":1460.5}}`, 0},
		{`{"tun":{"tcpMss":"1460"}}`, 0},
		{`{"tun":{"tcpMss":true}}`, 0},
		{`{"tun":{"tcpMss":null}}`, 0},
		{`{"tun":{"tcpMss":[1460]}}`, 0},
		{`{"tun":{"tcpMss":{"v":1460}}}`, 0},
		// 键名:大小写敏感,mtu 不串过来
		{`{"tun":{"tcpmss":1460}}`, 0},
		{`{"tun":{"mss":1460}}`, 0},
		{`{"tun":{"tcp_mss":1460}}`, 0},
		{`{"tcpMss":1460}`, 0},
		{`{"tun":{"mtu":1500}}`, 0},
		{`{"tun":{"stack":"gvisor","mtu":1500,"tcpMss":1400}}`, 1400},
	} {
		if got := TunTCPMss(vecJSON(t, c.profile)); got != c.want {
			t.Errorf("TunTCPMss(%s)=%d 要 %d", c.profile, got, c.want)
		}
	}
}

// 三个取值函数不改入参(profile 是调用方的对象)。
func TestTunOptionsDoNotMutate(t *testing.T) {
	src := `{"tun":{"stack":"gvisor","mtu":1500,"tcpMss":1400,"extra":[1,2]},"other":{"a":1}}`
	p := vecJSON(t, src)
	TunStack(p)
	TunMTU(p)
	TunTCPMss(p)
	if !reflect.DeepEqual(p, vecJSON(t, src)) {
		t.Errorf("入参被改了:%v", p)
	}
}
