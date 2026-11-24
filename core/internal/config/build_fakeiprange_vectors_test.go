package config

import (
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/ipx"
)

// 自写测试向量:档案 dns.fakeIpRange(面板「FakeIP 网段」)在整份配置里的落点。
// 期望值人工推算:inet4_range 用档案里的池;缓存文件名按该池隔离;池子落在被排除的私网里时,
// route_exclude_address 要把它挖出来(否则占位地址进不了 TUN);写坏的值退回 198.19.0.0/16。

func TestBuildFakeIPRange(t *testing.T) {
	// 缺省:inet4_range 是 198.19.0.0/16,缓存路径后缀不变。
	base := tl(t, `{"dns":{"split":true,"fakeIpForProxy":true}}`, "")
	if got := fakeIPServer(t, base)["inet4_range"]; got != "198.19.0.0/16" {
		t.Errorf("缺省池 = %v", got)
	}

	// 自定义池(仍旧不在排除表里):inet4_range 跟着改,缓存路径跟着改,排除段不变。
	custom := tl(t, `{"dns":{"split":true,"fakeIpForProxy":true,"fakeIpRange":"198.20.0.0/16"}}`, `,"cacheFilePath":"/tmp/c.db"`)
	if got := fakeIPServer(t, custom)["inet4_range"]; got != "198.20.0.0/16" {
		t.Errorf("自定义池 = %v", got)
	}
	cache := bhCfg(custom)["experimental"].(map[string]any)["cache_file"].(map[string]any)["path"]
	if cache != "/tmp/c.db.fakeip-198_20_0_0_16_fc00___18" {
		t.Errorf("自定义池的缓存路径 = %v", cache)
	}
	if got, want := bhJSON(t, tunOf(t, custom)["route_exclude_address"]), bhJSON(t, tunOf(t, base)["route_exclude_address"]); got != want {
		t.Errorf("池子不在排除表里时排除段不该变:\n got %s\nwant %s", got, want)
	}

	// 池子落在 192.168.0.0/16 里(默认排除):要从排除段里挖掉,占位地址才进得了 TUN。
	inLan := tl(t, `{"dns":{"split":true,"fakeIpForProxy":true,"fakeIpRange":"192.168.77.0/24"}}`, "")
	if got := fakeIPServer(t, inLan)["inet4_range"]; got != "192.168.77.0/24" {
		t.Errorf("私网池 = %v", got)
	}
	exclude := tunOf(t, inLan)["route_exclude_address"].([]any)
	for _, c := range exclude {
		if ipx.CidrContains(c.(string), "192.168.77.5") {
			t.Fatalf("192.168.77.0/24 应被挖出排除段,实际被 %s 盖住", c)
		}
	}
	// 挖洞只挖这一个网段,同一私网的别的地址照旧排除。
	covered := false
	for _, c := range exclude {
		if ipx.CidrContains(c.(string), "192.168.1.5") {
			covered = true
		}
	}
	if !covered {
		t.Errorf("192.168.1.5 仍应被排除")
	}

	// 写坏的值:退回缺省池,不把非法网段写进配置。
	for _, bad := range []string{"198.20.0.0/14", "不是网段", "fd00::/16", ""} {
		res := tl(t, `{"dns":{"split":true,"fakeIpForProxy":true,"fakeIpRange":"`+bad+`"}}`, "")
		if got := fakeIPServer(t, res)["inet4_range"]; got != "198.19.0.0/16" {
			t.Errorf("写坏的 %q 该退回缺省池,得到 %v", bad, got)
		}
	}
}

func fakeIPServer(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	dns := bhCfg(res)["dns"].(map[string]any)
	for _, s := range dns["servers"].([]any) {
		m := s.(map[string]any)
		if m["type"] == "fakeip" {
			return m
		}
	}
	t.Fatalf("配置里没有 fakeip 服务器")
	return nil
}
