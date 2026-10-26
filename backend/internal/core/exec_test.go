package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 子进程辅助:测试二进制自己充当假的 openbox-core(经典的 TestHelperProcess 写法)。
func TestMain(m *testing.M) {
	if mode := os.Getenv("CORE_TEST_HELPER"); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

func helperMain(mode string) {
	switch mode {
	case "ok":
		fmt.Println(`{"protocol":1,"ok":true,"value":"pong"}`)
	case "sleep":
		time.Sleep(time.Minute)
	case "crash":
		fmt.Fprintln(os.Stderr, "boom: something broke")
		os.Exit(3)
	case "flood": // 输出超过上限
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < MaxOutputBytes>>20+2; i++ {
			if _, err := fmt.Print(chunk); err != nil {
				os.Exit(0)
			}
		}
	case "env":
		fmt.Printf(`{"protocol":1,"ok":true,"value":%d}`+"\n", len(os.Environ()))
	}
	os.Exit(0)
}

func helperClient(t *testing.T, mode string, opts ...Option) *Client {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := New(exe, opts...)
	c.env = []string{"CORE_TEST_HELPER=" + mode}
	return c
}

func TestExecSuccess(t *testing.T) {
	var s string
	if err := helperClient(t, "ok").CallInto(context.Background(), "x", nil, &s); err != nil || s != "pong" {
		t.Fatalf("%q %v", s, err)
	}
}

func TestExecTimeoutKillsProcess(t *testing.T) {
	c := helperClient(t, "sleep", WithTimeout(300*time.Millisecond))
	start := time.Now()
	_, err := c.Call(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("应超时: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("进程没有被及时杀掉: %v", d)
	}
}

func TestExecCrashKeepsStderrTail(t *testing.T) {
	_, err := helperClient(t, "crash").Call(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "boom: something broke") {
		t.Fatalf("错误里应带 stderr: %v", err)
	}
}

func TestExecOutputTooLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过 64 MiB 输出测试")
	}
	_, err := helperClient(t, "flood", WithTimeout(60*time.Second)).Call(context.Background(), "x", nil)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("应是 ErrOutputTooLarge: %v", err)
	}
}

func TestExecDoesNotInheritEnvironment(t *testing.T) {
	t.Setenv("SOME_SECRET", "hunter2")
	exe, _ := os.Executable()
	c := New(exe)
	c.env = append(c.env, "CORE_TEST_HELPER=env")
	var n int
	if err := c.CallInto(context.Background(), "x", nil, &n); err != nil || n != 2 {
		t.Fatalf("子进程应只看到我们给的 2 个环境变量(含 LANG=C): n=%d err=%v", n, err)
	}
	if c2 := New("x"); len(c2.env) != 1 || c2.env[0] != "LANG=C" {
		t.Fatalf("默认环境: %v", c2.env)
	}
}

func TestExecMissingBinary(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "nope")).Call(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("可执行文件不存在应报错")
	}
}

// 集成测试:编译真正的 openbox-core,验证协议、hello 和一个真实方法。
// 没有 go 工具链或编译失败时跳过(云端 Actions 里会跑)。
func TestRealCore(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过集成测试")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("没有 go 工具链")
	}
	coreDir, err := filepath.Abs(filepath.Join("..", "..", "..", "core"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(coreDir, "go.mod")); err != nil {
		t.Skip("找不到 core/ 目录")
	}
	bin := filepath.Join(t.TempDir(), "openbox-core")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/openbox-core")
	build.Dir = coreDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("编译 core 失败,跳过: %v\n%s", err, out)
	}

	c := New(bin)
	ctx := context.Background()

	info, err := c.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("hello: %+v", info)

	// 真实方法:builtinTags(groups) → {direct, block, directEnabled, blockEnabled}
	var tags map[string]any
	if err := c.CallInto(ctx, "builtinTags", []any{[]any{}}, &tags); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"direct", "block", "directEnabled", "blockEnabled"} {
		if _, ok := tags[k]; !ok {
			t.Errorf("builtinTags 缺少 %s: %v", k, tags)
		}
	}

	// 核心自己的错误要变成 *CoreError
	_, err = c.Call(ctx, "noSuchMethod", nil)
	var ce *CoreError
	if !errors.As(err, &ce) || !strings.Contains(ce.Message, "未知方法") {
		t.Fatalf("未知方法应是 *CoreError: %v", err)
	}

	// 15 个方法都已注册:调用它们不应得到「未知方法」(参数不对导致的其它错误没关系)
	for _, m := range Methods {
		_, err := c.Call(ctx, m, nil)
		if errors.As(err, &ce) && strings.Contains(ce.Message, "未知方法") {
			t.Errorf("核心没有注册方法 %s", m)
		}
	}
}
