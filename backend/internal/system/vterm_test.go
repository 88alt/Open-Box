package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeIP 在内存里模拟 ip 命令用到的那几个子命令:命名空间、veth(成对)、网桥从属、命名空间里的进程。
type fakeIP struct {
	mu      sync.Mutex
	netns   map[string]bool
	links   map[string]*fakeLink // 主命名空间的网卡
	nsLinks map[string]map[string]*fakeLink
	pids    map[string][]int
	calls   []string
	// fail 返回非空就让这条命令以这个退出码和错误输出失败
	fail func(args string) (code int, stderr string)
}

type fakeLink struct {
	peer   string
	master string
	up     bool
	ns     string
}

func newFakeIP() *fakeIP {
	return &fakeIP{
		netns:   map[string]bool{},
		links:   map[string]*fakeLink{"br-lan": {}},
		nsLinks: map[string]map[string]*fakeLink{},
		pids:    map[string][]int{},
	}
}

func (f *fakeIP) run(_ context.Context, name string, args ...string) (RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != "ip" {
		return RunResult{}, fmt.Errorf("不该运行 %s", name)
	}
	line := strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if f.fail != nil {
		if code, se := f.fail(line); code != 0 {
			return RunResult{Code: code, Stderr: se}, nil
		}
	}
	bad := func(msg string) (RunResult, error) { return RunResult{Code: 1, Stderr: msg}, nil }
	ok := func(out string) (RunResult, error) { return RunResult{Stdout: out}, nil }

	switch {
	case line == "netns list":
		var names []string
		for n := range f.netns {
			names = append(names, n+" (id: 0)")
		}
		sort.Strings(names)
		return ok(strings.Join(names, "\n"))
	case len(args) == 3 && args[0] == "netns" && args[1] == "add":
		if f.netns[args[2]] {
			return bad("RTNETLINK answers: File exists")
		}
		f.netns[args[2]] = true
		f.nsLinks[args[2]] = map[string]*fakeLink{}
		return ok("")
	case len(args) == 3 && args[0] == "netns" && args[1] == "del":
		if !f.netns[args[2]] {
			return bad("Cannot remove namespace file: No such file or directory")
		}
		delete(f.netns, args[2])
		delete(f.nsLinks, args[2])
		return ok("")
	case len(args) == 3 && args[0] == "netns" && args[1] == "pids":
		var out []string
		for _, p := range f.pids[args[2]] {
			out = append(out, strconv.Itoa(p))
		}
		return ok(strings.Join(out, "\n"))
	case len(args) == 4 && args[0] == "link" && args[1] == "show" && args[2] == "dev":
		if _, in := f.links[args[3]]; in {
			return ok(args[3] + ": <BROADCAST,MULTICAST>")
		}
		return bad(`Device "` + args[3] + `" does not exist.`)
	case len(args) == 8 && args[0] == "link" && args[1] == "add" && args[3] == "type" && args[4] == "veth" && args[5] == "peer" && args[6] == "name":
		h, p := args[2], args[7]
		if _, in := f.links[h]; in {
			return bad("RTNETLINK answers: File exists")
		}
		if _, in := f.links[p]; in {
			return bad("RTNETLINK answers: File exists")
		}
		f.links[h] = &fakeLink{peer: p}
		f.links[p] = &fakeLink{peer: h}
		return ok("")
	case len(args) == 5 && args[0] == "link" && args[1] == "set" && args[3] == "netns":
		l, in := f.links[args[2]]
		if !in || !f.netns[args[4]] {
			return bad("Cannot find device")
		}
		delete(f.links, args[2])
		l.ns = args[4]
		f.nsLinks[args[4]][args[2]] = l
		return ok("")
	case len(args) == 5 && args[0] == "link" && args[1] == "set" && args[3] == "master":
		l, in := f.links[args[2]]
		if !in {
			return bad("Cannot find device")
		}
		if _, in := f.links[args[4]]; !in {
			return bad("Cannot find master")
		}
		l.master = args[4]
		return ok("")
	case len(args) == 4 && args[0] == "link" && args[1] == "set" && args[3] == "up":
		l, in := f.links[args[2]]
		if !in {
			return bad("Cannot find device")
		}
		l.up = true
		return ok("")
	case len(args) == 6 && args[0] == "-n" && args[2] == "link" && args[3] == "set" && args[5] == "up":
		l, in := f.nsLinks[args[1]][args[4]]
		if args[4] == "lo" {
			return ok("")
		}
		if !in {
			return bad("Cannot find device")
		}
		l.up = true
		return ok("")
	case len(args) == 7 && args[0] == "-n" && args[2] == "-o" && args[3] == "link" && args[4] == "show" && args[5] == "dev":
		if _, in := f.nsLinks[args[1]][args[6]]; !in {
			return bad("Cannot find device")
		}
		return ok("5: " + args[6] + "@if4: <BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\\    link/ether 02:aa:bb:cc:dd:ee brd ff:ff:ff:ff:ff:ff link-netnsid 0")
	case len(args) == 4 && args[0] == "link" && args[1] == "del" && args[2] == "dev":
		l, in := f.links[args[3]]
		if !in {
			return bad(`Cannot find device "` + args[3] + `"`)
		}
		delete(f.links, args[3])
		if l.peer != "" {
			delete(f.links, l.peer)
			for _, m := range f.nsLinks {
				delete(m, l.peer)
			}
		}
		return ok("")
	}
	return bad("fakeIP 不认识的命令:" + line)
}

func (f *fakeIP) mutating() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "netns add") || strings.HasPrefix(c, "netns del") || strings.HasPrefix(c, "link add") ||
			strings.HasPrefix(c, "link set") || strings.HasPrefix(c, "link del") || (strings.HasPrefix(c, "-n") && strings.Contains(c, "link set")) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeIP) clean() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.netns) != 0 {
		return false
	}
	for n := range f.links {
		if n != "br-lan" {
			return false
		}
	}
	return true
}

func newVTerm(t *testing.T) (*VTermNet, *fakeIP, string) {
	t.Helper()
	f := newFakeIP()
	dir := t.TempDir()
	state := filepath.Join(dir, "terminal-net.json")
	return &VTermNet{
		Run:       f.run,
		StateFile: state,
		IsBridge:  func(d string) bool { return d == "br-lan" },
		Sleep:     func(time.Duration) {},
		Kill:      func(int, syscall.Signal) error { return nil },
	}, f, state
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestVTermEnsureAndTeardown(t *testing.T) {
	n, f, state := newVTerm(t)
	ctx := context.Background()
	v, err := n.Ensure(ctx, "br-lan")
	if err != nil {
		t.Fatal(err)
	}
	if v.Namespace != "obterm" || v.HostIf != "obtv-h" || v.PeerIf != "obtv-t" || v.LANDevice != "br-lan" {
		t.Fatalf("名字不对:%+v", v)
	}
	if v.MAC != "02:aa:bb:cc:dd:ee" || v.Reused {
		t.Fatalf("MAC / Reused 不对:%+v", v)
	}
	if !f.netns["obterm"] || f.links["obtv-h"] == nil || f.links["obtv-h"].master != "br-lan" || !f.links["obtv-h"].up {
		t.Fatalf("没建好:netns=%v host=%+v", f.netns, f.links["obtv-h"])
	}
	if l := f.nsLinks["obterm"]["obtv-t"]; l == nil || !l.up {
		t.Fatalf("命名空间里的一头没起来:%+v", l)
	}
	b, err := os.ReadFile(state)
	if err != nil || !strings.Contains(string(b), `"phase":"ready"`) || !strings.Contains(string(b), `"lanDevice":"br-lan"`) {
		t.Fatalf("记账不对:%s %v", b, err)
	}

	// 再要一次:沿用,不再动系统。
	before := len(f.mutating())
	v2, err := n.Ensure(ctx, "br-lan")
	if err != nil || !v2.Reused || v2.MAC != v.MAC {
		t.Fatalf("应该沿用:%+v %v", v2, err)
	}
	if len(f.mutating()) != before {
		t.Fatalf("沿用时不该改系统:%v", f.mutating()[before:])
	}

	if err := n.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.clean() || exists(state) {
		t.Fatalf("没拆干净:netns=%v links=%v state=%v", f.netns, f.links, exists(state))
	}
	if err := n.Teardown(ctx); err != nil { // 幂等
		t.Fatalf("重复拆应该没事:%v", err)
	}
}

func TestVTermEnsureRebuildsWhenGone(t *testing.T) {
	n, f, _ := newVTerm(t)
	ctx := context.Background()
	if _, err := n.Ensure(ctx, "br-lan"); err != nil {
		t.Fatal(err)
	}
	// 有人(比如重启了网络)把命名空间弄没了:不能说「沿用」。
	f.mu.Lock()
	delete(f.netns, "obterm")
	f.mu.Unlock()
	v, err := n.Ensure(ctx, "br-lan")
	if err != nil {
		t.Fatal(err)
	}
	if v.Reused {
		t.Fatal("命名空间没了还说沿用")
	}
	if !f.netns["obterm"] {
		t.Fatal("没有重建")
	}
}

func TestVTermLANDeviceChangeRebuilds(t *testing.T) {
	n, f, _ := newVTerm(t)
	f.links["br-guest"] = &fakeLink{}
	n.IsBridge = func(d string) bool { return d == "br-lan" || d == "br-guest" }
	ctx := context.Background()
	if _, err := n.Ensure(ctx, "br-lan"); err != nil {
		t.Fatal(err)
	}
	v, err := n.Ensure(ctx, "br-guest")
	if err != nil || v.Reused || f.links["obtv-h"].master != "br-guest" {
		t.Fatalf("换 LAN 口应该重建:%+v %v", v, err)
	}
}

func TestVTermRefusesNonBridge(t *testing.T) {
	n, f, state := newVTerm(t)
	f.links["eth1"] = &fakeLink{}
	_, err := n.Ensure(context.Background(), "eth1")
	if err == nil || !strings.Contains(err.Error(), "不是网桥") {
		t.Fatalf("err=%v", err)
	}
	if len(f.mutating()) != 0 || exists(state) {
		t.Fatalf("不该动任何东西:%v state=%v", f.mutating(), exists(state))
	}
}

func TestVTermRefusesTakenNames(t *testing.T) {
	ctx := context.Background()
	t.Run("命名空间被别人占了", func(t *testing.T) {
		n, f, state := newVTerm(t)
		f.netns["obterm"] = true
		_, err := n.Ensure(ctx, "br-lan")
		if err == nil || !strings.Contains(err.Error(), "已被占用") {
			t.Fatalf("err=%v", err)
		}
		if !f.netns["obterm"] || len(f.mutating()) != 0 || exists(state) {
			t.Fatalf("别人的命名空间不能碰:%v", f.mutating())
		}
	})
	t.Run("网卡被别人占了", func(t *testing.T) {
		n, f, state := newVTerm(t)
		f.links["obtv-h"] = &fakeLink{}
		_, err := n.Ensure(ctx, "br-lan")
		if err == nil || !strings.Contains(err.Error(), "已被占用") {
			t.Fatalf("err=%v", err)
		}
		if f.links["obtv-h"] == nil || len(f.mutating()) != 0 || exists(state) {
			t.Fatalf("别人的网卡不能碰:%v", f.mutating())
		}
	})
}

// 每一步失败都要把已建的拆干净,并把原因带回去。
func TestVTermRollbackOnEveryStep(t *testing.T) {
	steps := []string{
		"netns add obterm",
		"link add obtv-h type veth peer name obtv-t",
		"link set obtv-t netns obterm",
		"link set obtv-h master br-lan",
		"link set obtv-h up",
		"-n obterm link set lo up",
		"-n obterm link set obtv-t up",
		"-n obterm -o link show dev obtv-t",
	}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			n, f, state := newVTerm(t)
			f.fail = func(a string) (int, string) {
				if a == step {
					return 2, "boom"
				}
				return 0, ""
			}
			_, err := n.Ensure(context.Background(), "br-lan")
			if err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("应带着原因失败:%v", err)
			}
			if !f.clean() || exists(state) {
				t.Fatalf("回滚后应该干净:netns=%v links=%v state=%v", f.netns, f.links, exists(state))
			}
		})
	}
}

func TestVTermRollbackThatFailsKeepsLedger(t *testing.T) {
	n, f, state := newVTerm(t)
	f.fail = func(a string) (int, string) {
		if a == "link set obtv-h up" {
			return 2, "boom"
		}
		if a == "link del dev obtv-h" {
			return 3, "busy"
		}
		return 0, ""
	}
	_, err := n.Ensure(context.Background(), "br-lan")
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "没拆干净") {
		t.Fatalf("err=%v", err)
	}
	if !exists(state) {
		t.Fatal("拆不干净要保留记账")
	}
	// 故障消失后,Recover 能拆干净。
	f.fail = nil
	did, err := n.Recover(context.Background())
	if err != nil || !did || !f.clean() || exists(state) {
		t.Fatalf("did=%v err=%v clean=%v", did, err, f.clean())
	}
}

func TestVTermRollbackSurvivesCancelledContext(t *testing.T) {
	n, f, state := newVTerm(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.fail = func(a string) (int, string) {
		if a == "link set obtv-h up" {
			cancel() // 请求被取消了
			return 2, "boom"
		}
		return 0, ""
	}
	// 假 runner 不看 ctx,但回滚必须换一个没被取消的 ctx。
	var seenCancelled bool
	inner := n.Run
	n.Run = func(c context.Context, name string, args ...string) (RunResult, error) {
		if c.Err() != nil && strings.Join(args, " ") != "link set obtv-h up" {
			seenCancelled = true
		}
		return inner(c, name, args...)
	}
	_, _ = n.Ensure(ctx, "br-lan")
	if seenCancelled {
		t.Fatal("回滚用了已取消的 ctx")
	}
	if !f.clean() || exists(state) {
		t.Fatal("取消后回滚应该照样拆干净")
	}
}

func TestVTermRecover(t *testing.T) {
	t.Run("没有记账什么都不做", func(t *testing.T) {
		n, f, _ := newVTerm(t)
		did, err := n.Recover(context.Background())
		if err != nil || did || len(f.calls) != 0 {
			t.Fatalf("did=%v err=%v calls=%v", did, err, f.calls)
		}
	})
	t.Run("上次崩溃遗留的被拆掉", func(t *testing.T) {
		n, f, state := newVTerm(t)
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		// 模拟进程重启:新的管理器,同一个记账文件。
		n2 := &VTermNet{Run: f.run, StateFile: state, IsBridge: n.IsBridge, Sleep: n.Sleep, Kill: n.Kill}
		did, err := n2.Recover(context.Background())
		if err != nil || !did || !f.clean() || exists(state) {
			t.Fatalf("did=%v err=%v", did, err)
		}
		// 再 Recover 一次:没东西了。
		did, err = n2.Recover(context.Background())
		if err != nil || did {
			t.Fatalf("did=%v err=%v", did, err)
		}
	})
	t.Run("只记了账、什么都没建出来也能清", func(t *testing.T) {
		n, f, state := newVTerm(t)
		_ = os.WriteFile(state, []byte(`{"namespace":"obterm","hostIf":"obtv-h","peerIf":"obtv-t","lanDevice":"br-lan","phase":"creating"}`), 0o600)
		did, err := n.Recover(context.Background())
		if err != nil || !did || exists(state) || len(f.mutating()) != 0 {
			t.Fatalf("did=%v err=%v mutating=%v", did, err, f.mutating())
		}
	})
}

func TestVTermLedgerWithForeignNamesIsNotTrusted(t *testing.T) {
	n, f, state := newVTerm(t)
	// 记账被改成指向用户自己的网桥:一个字都不能动。
	_ = os.WriteFile(state, []byte(`{"namespace":"obterm","hostIf":"br-lan","peerIf":"obtv-t","lanDevice":"br-lan","phase":"ready"}`), 0o600)
	_, err := n.Recover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("err=%v", err)
	}
	if f.links["br-lan"] == nil || len(f.mutating()) != 0 || !exists(state) {
		t.Fatalf("不能动 br-lan:%v", f.mutating())
	}
	if _, err := n.Ensure(context.Background(), "br-lan"); err == nil {
		t.Fatal("记账不可信时也不该新建")
	}
}

func TestVTermCorruptLedger(t *testing.T) {
	n, f, state := newVTerm(t)
	_ = os.WriteFile(state, []byte(`{not json`), 0o600)
	if _, err := n.Ensure(context.Background(), "br-lan"); err == nil || !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("err=%v", err)
	}
	if _, err := n.Recover(context.Background()); err == nil {
		t.Fatal("损坏的记账 Recover 也要报错")
	}
	if len(f.mutating()) != 0 || !exists(state) {
		t.Fatal("不能在记账损坏时动系统或删记账")
	}
}

func TestVTermLedgerWriteFailureMeansNoChange(t *testing.T) {
	n, f, _ := newVTerm(t)
	n.WriteFile = func(string, []byte, os.FileMode) error { return errors.New("磁盘满") }
	_, err := n.Ensure(context.Background(), "br-lan")
	if err == nil || !strings.Contains(err.Error(), "磁盘满") {
		t.Fatalf("err=%v", err)
	}
	if len(f.mutating()) != 0 {
		t.Fatalf("记不了账就不能动手:%v", f.mutating())
	}
}

func TestVTermTeardownVerifiesGone(t *testing.T) {
	n, f, state := newVTerm(t)
	if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
		t.Fatal(err)
	}
	// 删命令「成功」了但网卡还在(内核没删掉):不能当拆干净。
	f.fail = func(a string) (int, string) { return 0, "" }
	orig := n.Run
	n.Run = func(c context.Context, name string, args ...string) (RunResult, error) {
		if strings.Join(args, " ") == "link del dev obtv-h" {
			return RunResult{}, nil // 假装成功,什么都没删
		}
		return orig(c, name, args...)
	}
	err := n.Teardown(context.Background())
	if err == nil || !strings.Contains(err.Error(), "还在") {
		t.Fatalf("err=%v", err)
	}
	if !exists(state) {
		t.Fatal("没确认拆干净就不能删记账")
	}
}

func TestVTermUnknownLinkErrorIsNotTreatedAsAbsent(t *testing.T) {
	n, f, state := newVTerm(t)
	if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
		t.Fatal(err)
	}
	f.fail = func(a string) (int, string) {
		if a == "link show dev obtv-h" {
			return 1, "RTNETLINK answers: Operation not permitted"
		}
		return 0, ""
	}
	if err := n.Teardown(context.Background()); err == nil {
		t.Fatal("查不清网卡在不在,不能当不在")
	}
	if !exists(state) || f.links["obtv-h"] == nil {
		t.Fatal("不确定就不动")
	}
}

func TestVTermKillsNamespaceProcesses(t *testing.T) {
	t.Run("TERM 后退出", func(t *testing.T) {
		n, f, _ := newVTerm(t)
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		f.pids["obterm"] = []int{4242}
		var sigs []string
		n.Kill = func(pid int, s syscall.Signal) error {
			sigs = append(sigs, fmt.Sprintf("%d:%d", pid, s))
			f.mu.Lock()
			f.pids["obterm"] = nil
			f.mu.Unlock()
			return nil
		}
		if err := n.Teardown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(sigs) != 1 || sigs[0] != fmt.Sprintf("4242:%d", syscall.SIGTERM) {
			t.Fatalf("sigs=%v", sigs)
		}
	})
	t.Run("不听话就 KILL", func(t *testing.T) {
		n, f, _ := newVTerm(t)
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		f.pids["obterm"] = []int{4242}
		var sigs []syscall.Signal
		n.Kill = func(pid int, s syscall.Signal) error {
			sigs = append(sigs, s)
			if s == syscall.SIGKILL {
				f.mu.Lock()
				f.pids["obterm"] = nil
				f.mu.Unlock()
			}
			return nil
		}
		if err := n.Teardown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(sigs) != 2 || sigs[0] != syscall.SIGTERM || sigs[1] != syscall.SIGKILL {
			t.Fatalf("sigs=%v", sigs)
		}
	})
	t.Run("杀不掉就报错且保留记账", func(t *testing.T) {
		n, f, state := newVTerm(t)
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		f.pids["obterm"] = []int{4242}
		err := n.Teardown(context.Background())
		if err == nil || !strings.Contains(err.Error(), "杀不掉") || !exists(state) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("不杀自己", func(t *testing.T) {
		n, f, _ := newVTerm(t)
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		f.pids["obterm"] = []int{os.Getpid()}
		n.Kill = func(pid int, s syscall.Signal) error {
			t.Fatalf("不该给自己发信号:%d", pid)
			return nil
		}
		_ = n.Teardown(context.Background()) // 自己一直在列表里,最终会报杀不掉,这里只关心没杀自己
	})
}

func TestVTermRejectsBadNames(t *testing.T) {
	for _, bad := range []string{"", "-x", "a b", "a/b", "this-name-is-too-long", "a;b"} {
		n, _, _ := newVTerm(t)
		if _, err := n.Ensure(context.Background(), bad); err == nil {
			t.Fatalf("LAN 口名 %q 应被拒绝", bad)
		}
	}
	n, f, _ := newVTerm(t)
	n.Namespace = "bad name"
	if _, err := n.Ensure(context.Background(), "br-lan"); err == nil || len(f.calls) != 0 {
		t.Fatalf("命名空间名不合法应在运行任何命令之前拒绝:%v %v", err, f.calls)
	}
	n2, f2, _ := newVTerm(t)
	n2.HostIf, n2.PeerIf = "same", "same"
	if _, err := n2.Ensure(context.Background(), "br-lan"); err == nil || len(f2.calls) != 0 {
		t.Fatalf("两端同名应拒绝:%v", err)
	}
}

func TestVTermNoRunnerOrStateFile(t *testing.T) {
	n := &VTermNet{StateFile: filepath.Join(t.TempDir(), "x.json"), IsBridge: func(string) bool { return true }}
	if _, err := n.Ensure(context.Background(), "br-lan"); err == nil {
		t.Fatal("没有命令运行器应报错")
	}
	n2, _, _ := newVTerm(t)
	n2.StateFile = ""
	if _, err := n2.Ensure(context.Background(), "br-lan"); err == nil {
		t.Fatal("没有状态文件路径应报错")
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Guard:启动时先清遗留(同步,Guard 返回时已经清完),退出(ctx 结束)时再拆本进程建的。
func TestVTermGuard(t *testing.T) {
	t.Run("没有记账:启动和退出都不跑任何命令", func(t *testing.T) {
		n, f, _ := newVTerm(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := n.Guard(ctx, discardLog())
		cancel()
		wait()
		if len(f.calls) != 0 {
			t.Fatalf("不该跑命令: %v", f.calls)
		}
	})
	t.Run("启动时清掉上次遗留的,Guard 返回时就已经清完", func(t *testing.T) {
		n, f, state := newVTerm(t)
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		// 模拟进程重启:新的管理器,同一个记账文件。
		n2 := &VTermNet{Run: f.run, StateFile: state, IsBridge: n.IsBridge, Sleep: n.Sleep, Kill: n.Kill}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		wait := n2.Guard(ctx, discardLog())
		if !f.clean() || exists(state) {
			t.Fatal("Guard 返回时遗留应已清掉")
		}
		cancel()
		wait()
	})
	t.Run("退出时拆掉本进程建的", func(t *testing.T) {
		n, f, state := newVTerm(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := n.Guard(ctx, discardLog())
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		if f.clean() || !exists(state) {
			t.Fatal("建好后应有东西和记账")
		}
		cancel() // 进程要退出:ctx 已经取消,拆除仍要做完
		wait()
		if !f.clean() || exists(state) {
			t.Fatal("退出后命名空间、veth、记账都该没了")
		}
	})
	t.Run("启动清理失败不挡启动,记账保留,退出时再试", func(t *testing.T) {
		n, f, state := newVTerm(t)
		if _, err := n.Ensure(context.Background(), "br-lan"); err != nil {
			t.Fatal(err)
		}
		n2 := &VTermNet{Run: f.run, StateFile: state, IsBridge: n.IsBridge, Sleep: n.Sleep, Kill: n.Kill}
		f.fail = func(a string) (int, string) {
			if a == "link del dev obtv-h" {
				return 3, "busy"
			}
			return 0, ""
		}
		ctx, cancel := context.WithCancel(context.Background())
		wait := n2.Guard(ctx, discardLog()) // 不能 panic、不能卡住
		if !exists(state) {
			t.Fatal("拆不干净要保留记账")
		}
		f.fail = nil // 故障消失
		cancel()
		wait()
		if !f.clean() || exists(state) {
			t.Fatal("退出时应把遗留拆干净")
		}
	})
	t.Run("日志为空也能用", func(t *testing.T) {
		n, _, _ := newVTerm(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := n.Guard(ctx, nil)
		cancel()
		wait()
	})
}

// ---- 真实命名空间的集成测试:要 root、iproute2、允许建命名空间 / veth / 网桥,缺一样就跳过。

func realIPReady(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux 且 root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("没有 ip 命令")
	}
	if out, err := exec.Command("ip", "netns", "list").CombinedOutput(); err != nil {
		t.Skipf("ip netns 不可用:%v %s", err, out)
	}
	if out, err := exec.Command("ip", "link", "add", "obtprobe", "type", "bridge").CombinedOutput(); err != nil {
		t.Skipf("不允许建网桥:%v %s", err, out)
	}
	_ = exec.Command("ip", "link", "del", "obtprobe").Run()
}

func TestVTermRealNamespace(t *testing.T) {
	realIPReady(t)
	br := "obtbr-t"
	if out, err := exec.Command("ip", "link", "add", br, "type", "bridge").CombinedOutput(); err != nil {
		t.Skipf("建网桥失败:%v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", br).Run() })
	_ = exec.Command("ip", "link", "set", br, "up").Run()

	state := filepath.Join(t.TempDir(), "terminal-net.json")
	newNet := func() *VTermNet {
		return &VTermNet{Run: ExecRunner, StateFile: state, Namespace: "obtt-ns", HostIf: "obtt-h", PeerIf: "obtt-p"}
	}
	n := newNet()
	t.Cleanup(func() { _, _ = newNet().Recover(context.Background()) })
	ctx := context.Background()

	v, err := n.Ensure(ctx, br)
	if err != nil {
		t.Fatal(err)
	}
	if !macRe.MatchString("link/ether " + v.MAC) {
		t.Fatalf("MAC 不对:%q", v.MAC)
	}
	// 外面看:命名空间在、接网桥的一头在且从属于网桥。
	if out, _ := exec.Command("ip", "netns", "list").CombinedOutput(); !strings.Contains(string(out), "obtt-ns") {
		t.Fatalf("命名空间没建出来:%s", out)
	}
	out, err := exec.Command("ip", "-o", "link", "show", "dev", "obtt-h").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "master "+br) {
		t.Fatalf("接网桥的一头不对:%v %s", err, out)
	}
	// 命名空间里:对端在、已起来、MAC 与返回的一致。
	out, err = exec.Command("ip", "-n", "obtt-ns", "-o", "link", "show", "dev", "obtt-p").CombinedOutput()
	if err != nil || !strings.Contains(strings.ToLower(string(out)), v.MAC) {
		t.Fatalf("命名空间里的一头不对:%v %s", err, out)
	}

	// 模拟进程异常退出:新管理器 Recover 能拆干净。
	did, err := newNet().Recover(ctx)
	if err != nil || !did {
		t.Fatalf("did=%v err=%v", did, err)
	}
	if out, _ := exec.Command("ip", "netns", "list").CombinedOutput(); strings.Contains(string(out), "obtt-ns") {
		t.Fatalf("命名空间还在:%s", out)
	}
	if err := exec.Command("ip", "link", "show", "dev", "obtt-h").Run(); err == nil {
		t.Fatal("接网桥的一头还在")
	}
	if exists(state) {
		t.Fatal("记账没删")
	}
	if err := exec.Command("ip", "link", "show", "dev", br).Run(); err != nil {
		t.Fatalf("不该动用户的网桥:%v", err)
	}

	// 再建一次、再正常拆一次。
	if _, err := newNet().Ensure(ctx, br); err != nil {
		t.Fatal(err)
	}
	m := newNet()
	if _, err := m.Ensure(ctx, br); err == nil {
		// 新管理器没有「本进程建的」记忆,会先拆遗留再重建,成功是对的。
		_ = m.Teardown(ctx)
	}
}

func TestVTermRealRefusesNonBridge(t *testing.T) {
	realIPReady(t)
	// 用一对 veth 里的一头当「不是网桥的网卡」。
	if out, err := exec.Command("ip", "link", "add", "obtdum-t", "type", "veth", "peer", "name", "obtdum-u").CombinedOutput(); err != nil {
		t.Skipf("不允许建 veth:%v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", "obtdum-t").Run() })
	n := &VTermNet{Run: ExecRunner, StateFile: filepath.Join(t.TempDir(), "s.json"), Namespace: "obtt-ns2", HostIf: "obtt-h2", PeerIf: "obtt-p2"}
	_, err := n.Ensure(context.Background(), "obtdum-t")
	if err == nil || !strings.Contains(err.Error(), "不是网桥") {
		t.Fatalf("err=%v", err)
	}
	if out, _ := exec.Command("ip", "netns", "list").CombinedOutput(); strings.Contains(string(out), "obtt-ns2") {
		t.Fatal("拒绝后不该留下命名空间")
	}
}
