package singbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSingBox 写一个假的 sing-box:读 -c 指向的文件,内容含 "BAD" 就报错退出 1,否则退出 0。
func fakeSingBox(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本充当 sing-box,Windows 上跳过")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "sing-box")
	script := `#!/bin/sh
[ "$1" = "check" ] && [ "$2" = "-c" ] || { echo "bad args: $*" >&2; exit 2; }
if grep -q BAD "$3"; then echo "FATAL decode config at $3: unknown field BAD" >&2; exit 1; fi
exit 0
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckOKAndFail(t *testing.T) {
	bin := fakeSingBox(t)
	dir := t.TempDir()
	if err := Check(context.Background(), bin, dir, []byte(`{"log":{}}`)); err != nil {
		t.Fatalf("合法配置应通过: %v", err)
	}
	err := Check(context.Background(), bin, dir, []byte(`{"BAD":1}`))
	var ce *CheckError
	if !errors.As(err, &ce) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(ce.Output, "unknown field BAD") || !strings.Contains(ce.Output, "config.json") || strings.Contains(ce.Output, dir) {
		t.Fatalf("输出应带原因且不含临时路径: %q", ce.Output)
	}
	// 临时文件必须清理干净
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("残留临时文件: %v", left)
	}
}

func TestCheckMissingBinary(t *testing.T) {
	if err := Check(context.Background(), "/nonexistent/sing-box", t.TempDir(), []byte(`{}`)); !errors.Is(err, ErrNoSingBox) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckTempFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "sing-box")
	// 假 sing-box 检查文件权限:不是 0600 就报错
	script := "#!/bin/sh\n[ \"$(stat -c %a \"$3\")\" = \"600\" ] || { echo \"mode $(stat -c %a \"$3\")\" >&2; exit 1; }\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Check(context.Background(), bin, dir, []byte(`{}`)); err != nil {
		t.Fatalf("临时配置应是 0600: %v", err)
	}
}

func TestCheckOutputIsLimited(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	bin := filepath.Join(t.TempDir(), "sing-box")
	script := "#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' 'x' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Check(context.Background(), bin, t.TempDir(), []byte(`{}`))
	var ce *CheckError
	if !errors.As(err, &ce) || len(ce.Output) > maxCheckOut {
		t.Fatalf("输出应被截断到 %d: %v", maxCheckOut, err)
	}
}
