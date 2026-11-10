package routeget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func fake(out, errOut string, code int, err error, got *[]string) system.Runner {
	return func(_ context.Context, name string, args ...string) (system.RunResult, error) {
		if got != nil {
			*got = append([]string{name}, args...)
		}
		return system.RunResult{Stdout: out, Stderr: errOut, Code: code}, err
	}
}

func sysWithTun(t *testing.T, tunDev string) string {
	t.Helper()
	d := t.TempDir()
	for _, dev := range []string{"eth1", tunDev} {
		if err := os.MkdirAll(filepath.Join(d, dev), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, tunDev, "tun_flags"), []byte("0x1002\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGetViaWanDevice(t *testing.T) {
	var got []string
	g := &Getter{Run: fake("93.184.216.34 from 192.168.5.77 via 10.0.0.1 dev eth1 table main \\    cache iif br-lan\n", "", 0, nil, &got), SysClassNet: sysWithTun(t, "tun0")}
	info, err := g.Get(context.Background(), "93.184.216.34", "192.168.5.77", "br-lan")
	if err != nil || info.Dev != "eth1" || info.Via != "10.0.0.1" || info.Tun {
		t.Fatalf("%+v %v", info, err)
	}
	if strings.Join(got, " ") != "ip -o route get 93.184.216.34 from 192.168.5.77 iif br-lan" {
		t.Fatalf("命令:%v", got)
	}
	if strings.Contains(info.Line, `\`) || strings.Contains(info.Line, "  ") {
		t.Errorf("Line 要规整成一行:%q", info.Line)
	}
}

func TestGetIntoTun(t *testing.T) {
	g := &Getter{Run: fake("93.184.216.34 from 192.168.5.77 dev tun0 table 2022 cache iif br-lan\n", "", 0, nil, nil), SysClassNet: sysWithTun(t, "tun0")}
	info, err := g.Get(context.Background(), "93.184.216.34", "192.168.5.77", "br-lan")
	if err != nil || info.Dev != "tun0" || info.Via != "" || !info.Tun {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestGetErrorsAreErrors(t *testing.T) {
	ctx := context.Background()
	sys := sysWithTun(t, "tun0")
	cases := map[string]*Getter{
		"命令失败":     {Run: fake("", "RTNETLINK answers: Network is unreachable\n", 2, nil, nil), SysClassNet: sys},
		"起不来":      {Run: fake("", "", 0, errors.New("boom"), nil), SysClassNet: sys},
		"输出没有 dev": {Run: fake("unreachable 1.1.1.1\n", "", 0, nil, nil), SysClassNet: sys},
		"空输出":      {Run: fake("", "", 0, nil, nil), SysClassNet: sys},
	}
	for name, g := range cases {
		if info, err := g.Get(ctx, "1.1.1.1", "192.168.5.77", "br-lan"); err == nil {
			t.Errorf("%s:要报错,得到 %+v", name, info)
		}
	}
	// 错误里带原因
	if _, err := cases["命令失败"].Get(ctx, "1.1.1.1", "192.168.5.77", "br-lan"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("要带命令的原因:%v", err)
	}
}

func TestGetRejectsBadArguments(t *testing.T) {
	called := false
	g := &Getter{Run: func(context.Context, string, ...string) (system.RunResult, error) {
		called = true
		return system.RunResult{}, nil
	}}
	for _, c := range [][3]string{
		{"not-ip", "192.168.5.77", "br-lan"}, {"1.1.1.1", "x", "br-lan"}, {"1.1.1.1", "192.168.5.77", ""},
		{"1.1.1.1", "192.168.5.77", "-x"}, {"1.1.1.1", "192.168.5.77", "br lan"}, {"1.1.1.1", "192.168.5.77", "br-lan; reboot"},
	} {
		if _, err := g.Get(context.Background(), c[0], c[1], c[2]); err == nil {
			t.Errorf("%v 不该放行", c)
		}
	}
	if called {
		t.Fatal("参数不合法不该去跑命令")
	}
}
