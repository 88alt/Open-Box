package routing

import "testing"

// 自写测试向量:档案 dns.fakeIpRange(面板「FakeIP 网段」)。期望值按下面的规则人工推算:
// 合法 = IPv4 CIDR、掩码 /15~/24;主机位非零时按该掩码的网络地址写进配置(sing-box 只收网络地址);
// 留空、写法不合法、IPv6、掩码越界一律退回缺省 198.19.0.0/16。

func TestFakeIPRange(t *testing.T) {
	cases := []struct {
		name    string
		profile any
		want    string
	}{
		{"没有档案", nil, FakeIPV4},
		{"没有 dns", obj{"ipv6": true}, FakeIPV4},
		{"dns 不是对象", obj{"dns": "x"}, FakeIPV4},
		{"没写", obj{"dns": obj{}}, FakeIPV4},
		{"留空", obj{"dns": obj{"fakeIpRange": ""}}, FakeIPV4},
		{"只有空白", obj{"dns": obj{"fakeIpRange": "   "}}, FakeIPV4},
		{"合法 /16", obj{"dns": obj{"fakeIpRange": "198.20.0.0/16"}}, "198.20.0.0/16"},
		{"合法 /15 下界", obj{"dns": obj{"fakeIpRange": "198.18.0.0/15"}}, "198.18.0.0/15"},
		{"合法 /24 上界", obj{"dns": obj{"fakeIpRange": "10.99.7.0/24"}}, "10.99.7.0/24"},
		{"首尾空白去掉", obj{"dns": obj{"fakeIpRange": " 198.20.0.0/16 "}}, "198.20.0.0/16"},
		{"主机位非零按网络地址", obj{"dns": obj{"fakeIpRange": "198.20.12.34/16"}}, "198.20.0.0/16"},
		{"掩码 /14 太宽", obj{"dns": obj{"fakeIpRange": "198.18.0.0/14"}}, FakeIPV4},
		{"掩码 /25 太窄", obj{"dns": obj{"fakeIpRange": "10.99.7.0/25"}}, FakeIPV4},
		{"掩码 /32", obj{"dns": obj{"fakeIpRange": "10.99.7.1/32"}}, FakeIPV4},
		{"没写掩码", obj{"dns": obj{"fakeIpRange": "198.20.0.0"}}, FakeIPV4},
		{"IPv6 池不跟着改", obj{"dns": obj{"fakeIpRange": "fd00::/16"}}, FakeIPV4},
		{"写法不合法", obj{"dns": obj{"fakeIpRange": "不是网段"}}, FakeIPV4},
		{"端口之类", obj{"dns": obj{"fakeIpRange": "198.20.0.0/16:53"}}, FakeIPV4},
		{"非字符串", obj{"dns": obj{"fakeIpRange": 16.0}}, FakeIPV4},
		{"null", obj{"dns": obj{"fakeIpRange": nil}}, FakeIPV4},
		{"前导零不收", obj{"dns": obj{"fakeIpRange": "198.020.0.0/16"}}, FakeIPV4},
	}
	for _, c := range cases {
		if got := FakeIPRange(c.profile); got != c.want {
			t.Errorf("%s: FakeIPRange = %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestFakeIPCachePathFollowsRange(t *testing.T) {
	// 缓存文件按「此刻生效的池子」隔离:换了网段就不复用旧池的缓存。
	custom := obj{"dns": obj{"fakeIpRange": "198.20.0.0/16"}}
	if got, want := FakeIPCachePath("/data/c.db", custom), "/data/c.db.fakeip-198_20_0_0_16_fc00___18"; got != want {
		t.Errorf("自定义池的缓存路径 = %q,想要 %q", got, want)
	}
	// 写坏的值退回缺省池,路径与没有这个键时一致。
	if got, want := FakeIPCachePath("/data/c.db", obj{"dns": obj{"fakeIpRange": "x"}}), FakeIPCachePath("/data/c.db", nil); got != want {
		t.Errorf("写坏的值应与缺省一致:%q / %q", got, want)
	}
}
