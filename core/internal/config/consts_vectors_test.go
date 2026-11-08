package config

import (
	"net/netip"
	"testing"
)

// 自写测试向量:consts.go(config.mjs 开头的常量、FlipDirFor、FindDuplicateTag)。期望值人工推算:
//   - 端口 / tag / 路径常量按设计文档逐个点名(它们是和 Node、init 脚本、nft 表约定好的值,改了就是协议变化);
//   - 地址常量之间的关系用 net/netip 独立验证(网段包含地址、对端是网段里的第二个地址、排除表是规范写法且互不重叠、
//     末尾地址确实是地址空间的最后一个);
//   - FlipDirFor:去掉「最后一段路径」(正则 /[^/]*/?$ 从左到右取第一个能一直匹配到结尾的位置),再接 /flip;
//     rulesetDir 为空串 / null / 缺失 / false / 0 取默认值;其余值先按 JS 的 String() 转字符串(空数组是真值,转出空串);
//   - FindDuplicateTag:按顺序扫描,第一个「此前已出现过」的字符串 tag;非字符串 tag 与非对象条目不参与。

func TestConfigConstValues(t *testing.T) {
	for _, c := range []struct {
		name string
		got  any
		want any
	}{
		{"PanelInboundPort", PanelInboundPort, 7891},
		{"PanelInboundTag", PanelInboundTag, "panel-in"},
		{"TunV4", TunV4, "172.19.0.1/30"},
		{"TunV6", TunV6, "fdfe:dcba:9876::1/126"},
		{"TunV4Net", TunV4Net, "172.19.0.0/30"},
		{"TunV6Net", TunV6Net, "fdfe:dcba:9876::/126"},
		{"TunV4Peer", TunV4Peer, "172.19.0.2"},
		{"TunV6Peer", TunV6Peer, "fdfe:dcba:9876::2"},
		{"TunUDPTimeout", TunUDPTimeout, "60s"},
		{"DNSInboundPort", DNSInboundPort, 7853},
		{"DNSDirectInboundTag", DNSDirectInboundTag, "dns-in-direct"},
		{"DNSDirectInboundPort", DNSDirectInboundPort, 7855},
		{"DNSLanInboundTag", DNSLanInboundTag, "dns-in-lan"},
		{"DNSLanInboundPort", DNSLanInboundPort, 53},
		{"DNSMasqOutboundTag", DNSMasqOutboundTag, "dnsmasq"},
		{"DefaultRulesetDir", DefaultRulesetDir, "/opt/open-box/data/rulesets"},
	} {
		if c.got != c.want {
			t.Errorf("%s=%v 要 %v", c.name, c.got, c.want)
		}
	}
	// 三个专用 DNS 入站的端口互不相同
	if DNSInboundPort == DNSDirectInboundPort || DNSInboundPort == DNSLanInboundPort || DNSDirectInboundPort == DNSLanInboundPort {
		t.Error("DNS 入站端口撞了")
	}
}

func TestTunAddressRelations(t *testing.T) {
	for _, c := range []struct {
		name, addrWithLen, net, peer string
	}{
		{"v4", TunV4, TunV4Net, TunV4Peer},
		{"v6", TunV6, TunV6Net, TunV6Peer},
	} {
		self := netip.MustParsePrefix(c.addrWithLen)
		network := netip.MustParsePrefix(c.net)
		peer := netip.MustParseAddr(c.peer)
		if self.Masked() != network {
			t.Errorf("%s:%s 所在网段应是 %s,得到 %s", c.name, c.addrWithLen, c.net, self.Masked())
		}
		if network.Masked() != network {
			t.Errorf("%s:%s 不是规范的网段写法", c.name, c.net)
		}
		if !network.Contains(peer) {
			t.Errorf("%s:对端 %s 不在 %s 里", c.name, c.peer, c.net)
		}
		if peer == self.Addr() {
			t.Errorf("%s:对端和本机地址相同", c.name)
		}
		// 对端是网段里的第二个地址:网段起点 + 2,本机是 + 1
		if self.Addr() != network.Addr().Next() || peer != self.Addr().Next() {
			t.Errorf("%s:本机 %s / 对端 %s 应是网段起点的第 2 / 第 3 个地址", c.name, self.Addr(), peer)
		}
	}
	// 隧道地址落在私网排除段里:这就是 BuildDetailed 要给排除表挖洞的原因
	in := func(list []string, a netip.Addr) bool {
		for _, s := range list {
			if netip.MustParsePrefix(s).Contains(a) {
				return true
			}
		}
		return false
	}
	if !in(TunExcludeV4, netip.MustParseAddr("172.19.0.1")) {
		t.Error("172.19.0.1 应落在 172.16.0.0/12 里")
	}
	if !in(TunExcludeV6, netip.MustParseAddr("fdfe:dcba:9876::1")) {
		t.Error("fdfe:dcba:9876::1 应落在 fc00::/7 里")
	}
}

func TestTunExcludeLists(t *testing.T) {
	v4 := []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4"}
	v6 := []string{"fc00::/7", "fe80::/10", "ff00::/8"}
	check := func(name string, got, want []string, is4 bool) {
		if len(got) != len(want) {
			t.Fatalf("%s 有 %d 项,要 %d 项:%v", name, len(got), len(want), got)
		}
		var ps []netip.Prefix
		for i, s := range got {
			if s != want[i] {
				t.Errorf("%s[%d]=%q 要 %q", name, i, s, want[i])
			}
			p, err := netip.ParsePrefix(s)
			if err != nil {
				t.Errorf("%s[%d]=%q 不是网段:%v", name, i, s, err)
				continue
			}
			if p.Masked() != p {
				t.Errorf("%s[%d]=%q 不是规范写法(主机位非零)", name, i, s)
			}
			if p.Addr().Is4() != is4 {
				t.Errorf("%s[%d]=%q 地址族不对", name, i, s)
			}
			ps = append(ps, p)
		}
		for i := range ps {
			for j := i + 1; j < len(ps); j++ {
				if ps[i].Overlaps(ps[j]) {
					t.Errorf("%s:%s 与 %s 重叠", name, ps[i], ps[j])
				}
			}
		}
	}
	check("TunExcludeV4", TunExcludeV4, v4, true)
	check("TunExcludeV6", TunExcludeV6, v6, false)
}

func TestEndOfAddressSpace(t *testing.T) {
	if len(EndOfAddressSpace) != 2 {
		t.Fatalf("EndOfAddressSpace=%v", EndOfAddressSpace)
	}
	for i, c := range []struct {
		want string
		bits int
		is4  bool
	}{
		{"255.255.255.255/32", 32, true},
		{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff/128", 128, false},
	} {
		if EndOfAddressSpace[i] != c.want {
			t.Errorf("EndOfAddressSpace[%d]=%q 要 %q", i, EndOfAddressSpace[i], c.want)
		}
		p := netip.MustParsePrefix(EndOfAddressSpace[i])
		if p.Bits() != c.bits || p.Addr().Is4() != c.is4 {
			t.Errorf("%s 的前缀长度或地址族不对", EndOfAddressSpace[i])
		}
		// 是地址空间的最后一个地址:再往后没有了
		if p.Addr().Next().IsValid() {
			t.Errorf("%s 后面还有地址", p.Addr())
		}
	}
}

func TestFlipDirForVectors(t *testing.T) {
	for _, c := range []struct {
		name string
		in   any
		want string
	}{
		// 缺省:null / 缺失 / 空串 / false / 0 都是假值,取默认目录
		{"nil", nil, "/opt/open-box/data/flip"},
		{"空串", "", "/opt/open-box/data/flip"},
		{"false", false, "/opt/open-box/data/flip"},
		{"0", 0.0, "/opt/open-box/data/flip"},
		// 去最后一段
		{"默认目录", "/opt/open-box/data/rulesets", "/opt/open-box/data/flip"},
		{"别的目录", "/data/rs", "/data/flip"},
		{"三层", "/a/b/c", "/a/b/flip"},
		{"两层", "/a/b", "/a/flip"},
		{"一层", "/a", "/flip"},
		{"只有斜杠", "/", "/flip"},
		{"相对路径两段", "a/b", "a/flip"},
		{"没有斜杠", "rulesets", "rulesets/flip"},
		{"空白串是真值", " ", " /flip"},
		{"含空格", "/a b/c d", "/a b/flip"},
		{"含中文", "/数据/规则集", "/数据/flip"},
		// 末尾斜杠:最后一段连同一个末尾斜杠一起去掉
		{"一个末尾斜杠", "/data/rs/", "/data/flip"},
		{"一个末尾斜杠 2", "/opt/open-box/data/rulesets/", "/opt/open-box/data/flip"},
		{"两个末尾斜杠", "/a/b//", "/a/b/flip"},
		{"两个末尾斜杠 2", "/opt/x//", "/opt/x/flip"},
		{"三个末尾斜杠", "/a/b///", "/a/b//flip"},
		{"只有两个斜杠", "//", "/flip"},
		{"中间双斜杠", "/a//b", "/a//flip"},
		// 非字符串先转字符串(JS 的 String())
		{"数字 5", 5.0, "5/flip"},
		{"小数", 1.5, "1.5/flip"},
		{"true", true, "true/flip"},
		{"数组", []any{"a", "b"}, "a,b/flip"},
		{"数组里是路径", []any{"/x/y"}, "/x/flip"},
		{"空数组是真值,转出空串", []any{}, "/flip"},
		{"对象", map[string]any{}, "[object Object]/flip"},
	} {
		if got := FlipDirFor(c.in); got != c.want {
			t.Errorf("%s:FlipDirFor(%#v)=%q 要 %q", c.name, c.in, got, c.want)
		}
	}
}

func TestFindDuplicateTagVectors(t *testing.T) {
	o := func(tag any) any { return map[string]any{"tag": tag} }
	for _, c := range []struct {
		name    string
		list    []any
		wantTag string
		wantOK  bool
	}{
		{"nil 列表", nil, "", false},
		{"空列表", []any{}, "", false},
		{"单个", []any{o("a")}, "", false},
		{"互不相同", []any{o("a"), o("b"), o("c")}, "", false},
		{"相邻重复", []any{o("a"), o("a")}, "a", true},
		{"隔开重复", []any{o("a"), o("b"), o("a")}, "a", true},
		{"先出现第二次的先报", []any{o("a"), o("b"), o("b"), o("a")}, "b", true},
		{"三个相同报一次", []any{o("x"), o("x"), o("x")}, "x", true},
		{"两对重复报先凑齐的", []any{o("a"), o("b"), o("c"), o("c"), o("a"), o("b")}, "c", true},
		{"空串也算 tag", []any{o(""), o("")}, "", true},
		{"空串只出现一次", []any{o(""), o("a")}, "", false},
		{"大小写不同不算重复", []any{o("A"), o("a")}, "", false},
		{"首尾空白不同不算重复", []any{o("a"), o("a ")}, "", false},
		{"中文", []any{o("香港"), o("日本"), o("香港")}, "香港", true},
		// 非字符串 tag 不参与
		{"数字 tag 重复也不报", []any{o(1.0), o(1.0)}, "", false},
		{"null tag", []any{o(nil), o(nil)}, "", false},
		{"布尔 tag", []any{o(true), o(true)}, "", false},
		{"数组 tag", []any{o([]any{"a"}), o([]any{"a"})}, "", false},
		{"数字 1 与字符串 1 不撞", []any{o(1.0), o("1")}, "", false},
		{"缺 tag 的对象", []any{map[string]any{}, map[string]any{}}, "", false},
		{"别的键不算", []any{map[string]any{"name": "a"}, map[string]any{"name": "a"}}, "", false},
		// 非对象条目被跳过,不打断扫描
		{"夹着非对象", []any{nil, "a", 1.0, true, []any{"a"}, o("x"), nil, o("x")}, "x", true},
		{"字符串条目不是 tag", []any{"a", "a"}, "", false},
		{"重复之前有杂项", []any{o(1.0), o(nil), o("k"), map[string]any{}, o("k")}, "k", true},
	} {
		gotTag, gotOK := FindDuplicateTag(c.list)
		if gotTag != c.wantTag || gotOK != c.wantOK {
			t.Errorf("%s:得到 (%q,%v) 要 (%q,%v)", c.name, gotTag, gotOK, c.wantTag, c.wantOK)
		}
	}
}

func TestFindDuplicateTagDoesNotMutate(t *testing.T) {
	list := []any{map[string]any{"tag": "a"}, map[string]any{"tag": "b"}, map[string]any{"tag": "a"}}
	FindDuplicateTag(list)
	if len(list) != 3 || list[0].(map[string]any)["tag"] != "a" || list[1].(map[string]any)["tag"] != "b" || list[2].(map[string]any)["tag"] != "a" {
		t.Errorf("入参被改了:%v", list)
	}
}
