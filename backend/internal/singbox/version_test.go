package singbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
