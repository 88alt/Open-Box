package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// 虚拟终端的网络部分(terminal-test 的第一步):一个独立的网络命名空间,里面放 veth 的一头,
// 另一头接到 LAN 网桥上,让这个命名空间像一台普通的 LAN 设备。本文件只管「建出来」和「拆干净」,
// 不取 IP(DHCP)、不发探测、不取证,那些在后面的步骤里做。
// 自己的设计,依据前端 OpenboxTerminalTest.source 的字段和 iproute2 公开的手册(ip-netns、ip-link),没有参照上游。
//
// 会改系统,所以按 DESIGN 第四节来:
//   - 不确定就不动:LAN 口不是网桥(读不出来也算)、名字已被占用而状态文件里没记是我们建的,都直接报错,
//     不去接管别人的网卡或命名空间;
//   - 先记账再动手:动第一个手之前,先把「我们要用这三个名字」原子写进状态文件(phase=creating),
//     中途崩溃也知道该清什么;全部建好、MAC 读到后才改成 ready;
//   - 建的过程任何一步失败,当场按反序拆掉已建的部分,再返回错误;拆不干净就保留状态文件,下次启动或下次建之前再拆;
//   - 拆只动自己的名字:状态文件里的名字必须和本程序配置的完全一致才拆,否则一个字都不动
//     (防止状态文件被改成 br-lan 之类让我们去删用户的网卡);
//   - 拆是幂等的、可重复执行:不存在算已拆;拆完要回头确认两个名字确实没了才删状态文件;
//   - Recover 给进程启动和退出时用:清掉上次异常退出遗留的命名空间和 veth。

// 默认名字:命名空间 / 接到网桥的一头 / 放进命名空间的一头。接口名最长 15 个字符。
const (
	VTermDefaultNamespace = "obterm"
	VTermDefaultHostIf    = "obtv-h"
	VTermDefaultPeerIf    = "obtv-t"
)

const (
	vtermPhaseCreating = "creating"
	vtermPhaseReady    = "ready"
)

var vtermNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

var macRe = regexp.MustCompile(`link/ether ([0-9a-f]{2}(?::[0-9a-f]{2}){5})`)

// VirtualTerminal 是建好的虚拟终端(对应前端 source 里的 name / mac / lanDevice / reused)。
type VirtualTerminal struct {
	Namespace string
	HostIf    string
	PeerIf    string
	LANDevice string
	MAC       string
	Reused    bool // 这次是沿用本进程之前建好的
}

type vtermState struct {
	Namespace string `json:"namespace"`
	HostIf    string `json:"hostIf"`
	PeerIf    string `json:"peerIf"`
	LANDevice string `json:"lanDevice"`
	Phase     string `json:"phase"`
	CreatedAt int64  `json:"createdAt"`
}

// VTermNet 管虚拟终端的命名空间和 veth。零值的函数字段用真实实现补上(见 normalize)。
type VTermNet struct {
	Run       Runner
	StateFile string // 记账文件(原子写,0600)
	Namespace string // 为空用 VTermDefaultNamespace
	HostIf    string
	PeerIf    string

	// IsBridge 判断一块网卡是不是网桥;默认看 /sys/class/net/<名字>/bridge 在不在。
	IsBridge  func(dev string) bool
	ReadFile  func(path string) ([]byte, error)
	WriteFile func(path string, data []byte, perm os.FileMode) error // 默认 store.WriteFileAtomic
	Remove    func(path string) error
	// Kill 给命名空间里残留的进程发信号;默认 os.Process.Signal。
	Kill  func(pid int, sig syscall.Signal) error
	Sleep func(time.Duration)
	Now   func() time.Time
	// CmdTimeout 是单条命令的超时,默认 10 秒。
	CmdTimeout time.Duration
	// LookPath 找 DHCP 客户端程序,默认 exec.LookPath;ScriptDir 是放取址回调脚本的目录,默认 os.TempDir()
	// (OpenWrt 上是内存盘)。见 vterm_dhcp.go。
	LookPath  func(file string) (string, error)
	ScriptDir string

	mu  sync.Mutex
	cur *VirtualTerminal // 本进程建好、仍在用的
}

func (n *VTermNet) names() (ns, host, peer string) {
	ns, host, peer = n.Namespace, n.HostIf, n.PeerIf
	if ns == "" {
		ns = VTermDefaultNamespace
	}
	if host == "" {
		host = VTermDefaultHostIf
	}
	if peer == "" {
		peer = VTermDefaultPeerIf
	}
	return
}

func (n *VTermNet) readFile(p string) ([]byte, error) {
	if n.ReadFile != nil {
		return n.ReadFile(p)
	}
	return os.ReadFile(p)
}

func (n *VTermNet) writeFile(p string, b []byte, perm os.FileMode) error {
	if n.WriteFile != nil {
		return n.WriteFile(p, b, perm)
	}
	return store.WriteFileAtomic(p, b, perm)
}

func (n *VTermNet) remove(p string) error {
	if n.Remove != nil {
		return n.Remove(p)
	}
	return os.Remove(p)
}

func (n *VTermNet) isBridge(dev string) bool {
	if n.IsBridge != nil {
		return n.IsBridge(dev)
	}
	_, err := os.Stat("/sys/class/net/" + dev + "/bridge")
	return err == nil
}

func (n *VTermNet) sleep(d time.Duration) {
	if n.Sleep != nil {
		n.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (n *VTermNet) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *VTermNet) kill(pid int, sig syscall.Signal) error {
	if n.Kill != nil {
		return n.Kill(pid, sig)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// run 运行一条命令。命令根本起不来是 error;退出码非 0 由调用方看 RunResult.Code。
func (n *VTermNet) run(ctx context.Context, name string, args ...string) (RunResult, error) {
	if n.Run == nil {
		return RunResult{}, errors.New("没有可用的命令运行器")
	}
	to := n.CmdTimeout
	if to <= 0 {
		to = 10 * time.Second
	}
	return n.runFor(ctx, to, name, args...)
}

// runFor 和 run 一样,只是单条命令的超时由调用方定(DHCP 取址要等服务器应答,比 ip 命令久)。
func (n *VTermNet) runFor(ctx context.Context, to time.Duration, name string, args ...string) (RunResult, error) {
	if n.Run == nil {
		return RunResult{}, errors.New("没有可用的命令运行器")
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	return n.Run(ctx, name, args...)
}

// ip 运行 `ip <args>`,退出码非 0 当作错误,错误里带上命令的标准错误。
func (n *VTermNet) ip(ctx context.Context, args ...string) (RunResult, error) {
	r, err := n.run(ctx, "ip", args...)
	if err != nil {
		return r, fmt.Errorf("运行 ip %s 失败:%w", strings.Join(args, " "), err)
	}
	if r.Code != 0 {
		return r, fmt.Errorf("ip %s 失败(退出码 %d):%s", strings.Join(args, " "), r.Code, strings.TrimSpace(r.Stderr))
	}
	return r, nil
}

func (n *VTermNet) checkNames() error {
	ns, host, peer := n.names()
	for _, s := range []string{ns, host, peer} {
		if !vtermNameRe.MatchString(s) {
			return fmt.Errorf("虚拟终端的名字 %q 不合法(只允许字母、数字、_ . -,最长 15 个字符,不能以符号开头)", s)
		}
	}
	if host == peer {
		return errors.New("虚拟终端两端的接口名不能相同")
	}
	return nil
}

// netnsExists:`ip netns list` 的每行第一个词是命名空间名。命令失败就报错(不猜)。
func (n *VTermNet) netnsExists(ctx context.Context, ns string) (bool, error) {
	r, err := n.ip(ctx, "netns", "list")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == ns {
			return true, nil
		}
	}
	return false, nil
}

// linkExists:`ip link show dev <名>` 退出码 0 = 在;非 0 且报错是「找不到这块网卡」= 不在;其它失败报错(不猜)。
func (n *VTermNet) linkExists(ctx context.Context, dev string) (bool, error) {
	r, err := n.run(ctx, "ip", "link", "show", "dev", dev)
	if err != nil {
		return false, fmt.Errorf("运行 ip link show dev %s 失败:%w", dev, err)
	}
	if r.Code == 0 {
		return true, nil
	}
	low := strings.ToLower(r.Stderr)
	if strings.Contains(low, "does not exist") || strings.Contains(low, "cannot find device") || strings.Contains(low, "can't find device") {
		return false, nil
	}
	return false, fmt.Errorf("ip link show dev %s 失败(退出码 %d):%s", dev, r.Code, strings.TrimSpace(r.Stderr))
}

// readState:文件不存在返回 (nil, nil);读不了或内容坏了报错(不猜、不覆盖)。
func (n *VTermNet) readState() (*vtermState, error) {
	b, err := n.readFile(n.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读虚拟终端状态文件失败:%w", err)
	}
	var st vtermState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("虚拟终端状态文件 %s 损坏:%w(确认命名空间和 veth 已清掉后手工删除这个文件)", n.StateFile, err)
	}
	return &st, nil
}

func (n *VTermNet) writeState(st vtermState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return n.writeFile(n.StateFile, b, 0o600)
}

// Ensure 保证有一个可用的虚拟终端并返回它:本进程已经建好且还在就沿用(Reused=true);
// 否则先拆掉遗留的,再重新建。lanDevice 必须是网桥(veth 要接上去)。
func (n *VTermNet) Ensure(ctx context.Context, lanDevice string) (VirtualTerminal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.checkNames(); err != nil {
		return VirtualTerminal{}, err
	}
	if n.StateFile == "" {
		return VirtualTerminal{}, errors.New("没有配置虚拟终端的状态文件路径")
	}
	if !vtermNameRe.MatchString(lanDevice) {
		return VirtualTerminal{}, fmt.Errorf("LAN 口名字 %q 不合法", lanDevice)
	}
	ns, host, peer := n.names()

	// 沿用:本进程建的、LAN 口没变、两端都还在。
	if n.cur != nil && n.cur.LANDevice == lanDevice {
		okNS, e1 := n.netnsExists(ctx, ns)
		okLink, e2 := n.linkExists(ctx, host)
		if e1 == nil && e2 == nil && okNS && okLink {
			t := *n.cur
			t.Reused = true
			return t, nil
		}
	}
	if n.cur != nil {
		n.cur = nil
	}

	// 先拆遗留(上次异常退出、或上面判断不能沿用的)。拆不干净就不继续建。
	if err := n.teardownLocked(ctx); err != nil {
		return VirtualTerminal{}, fmt.Errorf("拆除遗留的虚拟终端失败,没有新建:%w", err)
	}

	// 不确定就不动。
	if !n.isBridge(lanDevice) {
		return VirtualTerminal{}, fmt.Errorf("LAN 口 %s 不是网桥,虚拟终端接不上去", lanDevice)
	}
	if ok, err := n.netnsExists(ctx, ns); err != nil {
		return VirtualTerminal{}, err
	} else if ok {
		return VirtualTerminal{}, fmt.Errorf("命名空间名 %s 已被占用,且不是本程序建的,没有动它", ns)
	}
	for _, d := range []string{host, peer} {
		ok, err := n.linkExists(ctx, d)
		if err != nil {
			return VirtualTerminal{}, err
		}
		if ok {
			return VirtualTerminal{}, fmt.Errorf("网卡名 %s 已被占用,且不是本程序建的,没有动它", d)
		}
	}

	// 先记账,再动手。
	st := vtermState{Namespace: ns, HostIf: host, PeerIf: peer, LANDevice: lanDevice, Phase: vtermPhaseCreating, CreatedAt: n.now().Unix()}
	if err := n.writeState(st); err != nil {
		return VirtualTerminal{}, fmt.Errorf("写虚拟终端状态文件失败,没有新建:%w", err)
	}

	steps := [][]string{
		{"netns", "add", ns},
		{"link", "add", host, "type", "veth", "peer", "name", peer},
		{"link", "set", peer, "netns", ns},
		{"link", "set", host, "master", lanDevice},
		{"link", "set", host, "up"},
		{"-n", ns, "link", "set", "lo", "up"},
		{"-n", ns, "link", "set", peer, "up"},
	}
	for _, s := range steps {
		if _, err := n.ip(ctx, s...); err != nil {
			return VirtualTerminal{}, n.rollbackLocked(ctx, err)
		}
	}

	r, err := n.ip(ctx, "-n", ns, "-o", "link", "show", "dev", peer)
	if err != nil {
		return VirtualTerminal{}, n.rollbackLocked(ctx, err)
	}
	m := macRe.FindStringSubmatch(strings.ToLower(r.Stdout))
	if m == nil {
		return VirtualTerminal{}, n.rollbackLocked(ctx, fmt.Errorf("读不到命名空间里 %s 的 MAC 地址", peer))
	}

	st.Phase = vtermPhaseReady
	if err := n.writeState(st); err != nil {
		return VirtualTerminal{}, n.rollbackLocked(ctx, fmt.Errorf("写虚拟终端状态文件失败:%w", err))
	}
	t := VirtualTerminal{Namespace: ns, HostIf: host, PeerIf: peer, LANDevice: lanDevice, MAC: m[1]}
	n.cur = &t
	return t, nil
}

// rollbackLocked:建到一半失败,拆掉已建的部分。返回的错误带着原因;拆不干净会一并说明(状态文件保留,之后再拆)。
func (n *VTermNet) rollbackLocked(ctx context.Context, cause error) error {
	// 外面的 ctx 可能已经被取消,回滚不能因此放弃:另起一个有时限的。
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := n.teardownLocked(rctx); err != nil {
		return fmt.Errorf("%w;回滚也没拆干净(状态文件已保留,下次会再拆):%v", cause, err)
	}
	return cause
}

// Teardown 拆掉虚拟终端。可重复执行;没有遗留什么也不做。
func (n *VTermNet) Teardown(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cur = nil
	return n.teardownLocked(ctx)
}

// Recover 给进程启动和退出时用:状态文件还在就说明有遗留,拆掉。返回是否真的拆了东西。
func (n *VTermNet) Recover(ctx context.Context) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.StateFile == "" {
		return false, nil
	}
	st, err := n.readState()
	if err != nil {
		return false, err
	}
	if st == nil {
		return false, nil
	}
	n.cur = nil
	return true, n.teardownLocked(ctx)
}

// 启动清理和退出拆除各自的时限。命令本身有单条超时(CmdTimeout),这里是兜底,防止卡住进程的启动或退出。
const (
	vtermStartupTimeout  = 30 * time.Second
	vtermShutdownTimeout = 60 * time.Second
)

// Guard 把虚拟终端的清理接到进程的生命周期上:
//   - 返回之前先同步做一次 Recover,清掉上次异常退出遗留的命名空间和 veth。同步做是为了让它一定排在任何
//     Ensure 之前(服务还没开始接请求),不会出现「刚建好的被启动清理拆掉」。没有记账时不跑任何命令,
//     正常情况下不拖慢启动;有遗留才会多花几秒;
//   - ctx 结束(进程要退出)后再 Teardown 一次,拆掉本进程建的。拆除用不受 ctx 取消影响的新 ctx 加时限;
//   - 清理失败只记日志、不挡启动和退出:记账会保留,下次 Ensure 或下次启动再拆(见 teardownLocked)。
//
// 返回的 wait 等退出拆除做完。log 为空时不记日志。
func (n *VTermNet) Guard(ctx context.Context, log *slog.Logger) (wait func()) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vtermStartupTimeout)
	did, err := n.Recover(sctx)
	cancel()
	switch {
	case err != nil:
		log.Warn("清理上次遗留的虚拟终端失败,记账已保留,之后会再试", "err", err)
	case did:
		log.Info("已清理上次异常退出遗留的虚拟终端")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vtermShutdownTimeout)
		defer cancel()
		if err := n.Teardown(tctx); err != nil {
			log.Warn("退出时拆除虚拟终端失败,记账已保留,下次启动会再拆", "err", err)
		}
	}()
	return func() { <-done }
}

func (n *VTermNet) teardownLocked(ctx context.Context) error {
	if err := n.checkNames(); err != nil {
		return err
	}
	if n.StateFile == "" {
		return nil
	}
	st, err := n.readState()
	if err != nil {
		return err
	}
	if st == nil {
		return nil // 没有记账 = 没有我们建的东西,不动
	}
	ns, host, peer := n.names()
	if st.Namespace != ns || st.HostIf != host || st.PeerIf != peer {
		return fmt.Errorf("状态文件里的名字(%s / %s / %s)和本程序用的(%s / %s / %s)不一致,没有动任何东西", st.Namespace, st.HostIf, st.PeerIf, ns, host, peer)
	}

	// 先杀命名空间里残留的进程(后面的步骤会在里面跑 DHCP 客户端和探测),否则命名空间删了也会留着。
	if ok, err := n.netnsExists(ctx, ns); err != nil {
		return err
	} else if ok {
		if err := n.killNamespaceProcs(ctx, ns); err != nil {
			return err
		}
	}

	// 删 veth 的接网桥一头,里头那头会一起没。
	if ok, err := n.linkExists(ctx, host); err != nil {
		return err
	} else if ok {
		if _, err := n.ip(ctx, "link", "del", "dev", host); err != nil {
			return err
		}
	}
	if ok, err := n.netnsExists(ctx, ns); err != nil {
		return err
	} else if ok {
		if _, err := n.ip(ctx, "netns", "del", ns); err != nil {
			return err
		}
	}

	// 回头确认,确实没了才删记账。
	if ok, err := n.netnsExists(ctx, ns); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("命名空间 %s 删完还在", ns)
	}
	if ok, err := n.linkExists(ctx, host); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("网卡 %s 删完还在", host)
	}
	if err := n.remove(n.StateFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删虚拟终端状态文件失败:%w", err)
	}
	return nil
}

func (n *VTermNet) nsPids(ctx context.Context, ns string) ([]int, error) {
	r, err := n.ip(ctx, "netns", "pids", ns)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, f := range strings.Fields(r.Stdout) {
		p, err := strconv.Atoi(f)
		if err == nil && p > 1 {
			out = append(out, p)
		}
	}
	return out, nil
}

// killNamespaceProcs:先 SIGTERM,最多等 2 秒,还有就 SIGKILL。只动 `ip netns pids <我们的命名空间>` 列出的进程。
func (n *VTermNet) killNamespaceProcs(ctx context.Context, ns string) error {
	pids, err := n.nsPids(ctx, ns)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	self := os.Getpid()
	send := func(list []int, sig syscall.Signal) {
		for _, p := range list {
			if p == self {
				continue
			}
			_ = n.kill(p, sig)
		}
	}
	send(pids, syscall.SIGTERM)
	for i := 0; i < 8; i++ {
		n.sleep(250 * time.Millisecond)
		pids, err = n.nsPids(ctx, ns)
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			return nil
		}
	}
	send(pids, syscall.SIGKILL)
	n.sleep(250 * time.Millisecond)
	pids, err = n.nsPids(ctx, ns)
	if err != nil {
		return err
	}
	if len(pids) > 0 {
		return fmt.Errorf("命名空间 %s 里还有杀不掉的进程:%v", ns, pids)
	}
	return nil
}
