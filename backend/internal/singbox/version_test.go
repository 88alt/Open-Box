package singbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func fakeBin(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadKernelVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 sing-box 是 /bin/sh 脚本,Windows 上跑不了")
	}
	v := ReadKernelVersion(context.Background(), fakeBin(t, `printf 'sing-box version 1.14.0-openbox-tcp5\n\nEnvironment: go1.25 linux/arm64\n'`))
	if !v.OK || v.Version != "1.14.0-openbox-tcp5" || v.Raw == "" {
		t.Fatalf("%+v", v)
	}
	// 能运行但输出不认识
	v = ReadKernelVersion(context.Background(), fakeBin(t, `echo hello`))
	if v.OK || v.Version != "" || v.Raw != "hello" {
		t.Fatalf("%+v", v)
	}
	// 退出码非 0
	v = ReadKernelVersion(context.Background(), fakeBin(t, `echo broken >&2; exit 2`))
	if v.OK || v.Raw != "broken" {
		t.Fatalf("%+v", v)
	}
	// 找不到
	if v = ReadKernelVersion(context.Background(), "no-such-sing-box"); v.OK || v.Version != "" || v.Raw != "" {
		t.Fatalf("%+v", v)
	}
}

func TestReadKernelVersionRealBinary(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	v := ReadKernelVersion(context.Background(), bin)
	if !v.OK || v.Version == "" {
		t.Fatalf("%+v", v)
	}
}

func TestReadMihomoVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 mihomo 是 /bin/sh 脚本,Windows 上跑不了")
	}
	ctx := context.Background()
	// 第一行整行就是版本(面板自己整理成 mihomo-1.19.0-openbox-2)
	v := ReadMihomoVersion(ctx, fakeBin(t, `printf 'Mihomo Meta v1.19.0-openbox.2 linux arm64 with go1.25 Fri Nov 20\nUse tags: with_gvisor\n'`))
	if !v.OK || v.Version != "Mihomo Meta v1.19.0-openbox.2 linux arm64 with go1.25 Fri Nov 20" || v.Raw == "" {
		t.Fatalf("%+v", v)
	}
	// 能运行但没有输出:不算读到
	if v = ReadMihomoVersion(ctx, fakeBin(t, `exit 0`)); v.OK || v.Version != "" {
		t.Fatalf("%+v", v)
	}
	// 退出码非 0:版本留空,原始输出(含标准错误)保留
	if v = ReadMihomoVersion(ctx, fakeBin(t, `echo Mihomo; echo broken >&2; exit 2`)); v.OK || v.Version != "" || v.Raw != "Mihomo\nbroken" {
		t.Fatalf("%+v", v)
	}
	// 找不到
	if v = ReadMihomoVersion(ctx, "no-such-mihomo"); v.OK || v.Version != "" || v.Raw != "" {
		t.Fatalf("%+v", v)
	}
}

func TestReadMihomoVersionRealBinary(t *testing.T) {
	bin, err := exec.LookPath("mihomo")
	if err != nil {
		t.Skip("没有 mihomo,跳过")
	}
	v := ReadMihomoVersion(context.Background(), bin)
	// 真实输出第一行形如「Mihomo Meta v1.19.34 linux amd64 with go1.26 …」,面板按这个格式整理成 mihomo-1.19.34
	if !v.OK || !regexp.MustCompile(`^Mihomo(?: Meta)? v?\d`).MatchString(v.Version) {
		t.Fatalf("%+v", v)
	}
}
