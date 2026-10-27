package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

func fakeProc(t *testing.T, comms map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, comm := range comms {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if comm != "" {
			if err := os.WriteFile(filepath.Join(d, "comm"), []byte(comm), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func TestFindProcs(t *testing.T) {
	dir := fakeProc(t, map[string]string{
		"100":                     "dnsmasq\n",
		"200":                     "dnsmasq\n",
		"300":                     "dnsmasq-extra\n", // 只认完全相同的名字
		"400":                     "sing-box\n",
		"1":                       "dnsmasq\n", // 不碰 init
		"self":                    "dnsmasq\n", // 非数字目录
		"500":                     "",          // 没有 comm 文件
		strconv.Itoa(os.Getpid()): "dnsmasq\n", // 不碰自己
	})
	got := FindProcs(dir, "dnsmasq")
	sort.Ints(got)
	if want := []int{100, 200}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if FindProcs(filepath.Join(dir, "none"), "dnsmasq") != nil {
		t.Fatal("目录不存在应为空")
	}
}

func TestFlushDnsmasq(t *testing.T) {
	dir := fakeProc(t, map[string]string{"100": "dnsmasq\n", "200": "dnsmasq\n"})
	var sent []int
	ok := FlushDnsmasq(dir, func(pid int) error { sent = append(sent, pid); return nil })
	sort.Ints(sent)
	if !ok || !reflect.DeepEqual(sent, []int{100, 200}) {
		t.Fatalf("%v %v", ok, sent)
	}
	// 一个失败一个成功:算成功
	n := 0
	if !FlushDnsmasq(dir, func(int) error {
		n++
		if n == 1 {
			return errors.New("EPERM")
		}
		return nil
	}) {
		t.Fatal("有一个成功就应为 true")
	}
	// 全失败 / 没有进程:false
	if FlushDnsmasq(dir, func(int) error { return errors.New("x") }) {
		t.Fatal("全失败应为 false")
	}
	if FlushDnsmasq(t.TempDir(), func(int) error { t.Fatal("没有进程不该发信号"); return nil }) {
		t.Fatal("没有进程应为 false")
	}
}

func TestFlushResolved(t *testing.T) {
	var gotName string
	var gotArgs []string
	mk := func(res RunResult, err error) Runner {
		return func(_ context.Context, name string, args ...string) (RunResult, error) {
			gotName, gotArgs = name, args
			return res, err
		}
	}
	if !FlushResolved(context.Background(), mk(RunResult{}, nil)) || gotName != "resolvectl" || !reflect.DeepEqual(gotArgs, []string{"flush-caches"}) {
		t.Fatalf("%s %v", gotName, gotArgs)
	}
	if FlushResolved(context.Background(), mk(RunResult{Code: 1, Stderr: "Failed"}, nil)) {
		t.Fatal("退出码非 0 应为 false")
	}
	if FlushResolved(context.Background(), mk(RunResult{}, errors.New("not found"))) {
		t.Fatal("命令不存在应为 false")
	}
}
