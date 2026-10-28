package system

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- 假 DNS 服务器:扮演「内核的 DNS 入站」和「dnsmasq」 ----

type fakeDNS struct {
	conn net.PacketConn
	mode atomic.Int32 // 0 应答 1 不应答 2 应答但事务号不对
	hits atomic.Int64 // 收到的查询数
	addr string
}

func startFakeDNS(t *testing.T) *fakeDNS { return startFakeDNSAt(t, "127.0.0.1:0") }

func startFakeDNSAt(t *testing.T, listen string) *fakeDNS {
	t.Helper()
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		t.Skipf("开不了本机 UDP %s: %v", listen, err)
	}
	f := &fakeDNS{conn: pc, addr: pc.LocalAddr().String()}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			f.hits.Add(1)
			switch f.mode.Load() {
			case 0:
				resp := append([]byte{}, buf[:n]...)
				resp[2] |= 0x80                // QR
				resp[3] = (resp[3] & 0xf0) | 3 // NXDOMAIN:任何 RCODE 都算「有人答」
				_, _ = pc.WriteTo(resp, from)
			case 2:
				resp := append([]byte{}, buf[:n]...)
				resp[0] ^= 0xff
				resp[2] |= 0x80
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	t.Cleanup(func() { pc.Close() })
	return f
}

func (f *fakeDNS) setUp(up bool) {
	if up {
		f.mode.Store(0)
	} else {
		f.mode.Store(1)
	}
}

// ---- 假服务管理器 ----

type fakeSvc struct {
	mu        sync.Mutex
	running   bool
	statusErr error
	restarts  int
	failAt    map[int]bool // 第几次重启失败
	onRestart func(n int)
}

func (s *fakeSvc) Status(_ context.Context, name string) (ServiceInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "dnsmasq" {
		return ServiceInfo{}, errors.New("意外的服务名 " + name)
	}
	return ServiceInfo{Running: s.running}, s.statusErr
}

func (s *fakeSvc) Do(_ context.Context, name, action string) (ActionResult, error) {
	s.mu.Lock()
	s.restarts++
	n := s.restarts
	fail := s.failAt[n]
	cb := s.onRestart
	s.mu.Unlock()
	if name != "dnsmasq" || action != "restart" {
		return ActionResult{}, errors.New("意外的操作 " + name + " " + action)
	}
	if cb != nil {
		cb(n)
	}
	if fail {
		return ActionResult{OK: false, Code: 1, Stderr: "boom"}, nil
	}
	return ActionResult{OK: true}, nil
}

func (s *fakeSvc) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

type tkEnv struct {
	tk      *DNSTakeover
	svc     *fakeSvc
	kernel  *fakeDNS
	dnsmasq *fakeDNS
	conf    string // dnsmasq 的配置目录
	file    string // 接管文件
	state   string
	dir     string
}

func newTkEnv(t *testing.T) *tkEnv {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "dnsmasq.cfg01.d")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dnsmasq.conf.cfg01"), []byte("# 生成的主配置\nconf-dir="+conf+"\nresolv-file=/tmp/resolv.conf.auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &tkEnv{
		svc:     &fakeSvc{running: true, failAt: map[int]bool{}},
		kernel:  startFakeDNS(t),
		dnsmasq: startFakeDNS(t),
		conf:    conf, file: filepath.Join(conf, TakeoverFileName), state: filepath.Join(dir, "state", "dns-takeover.json"), dir: dir,
	}
	e.tk = &DNSTakeover{
		StateFile: e.state, Services: e.svc, KernelAddr: e.kernel.addr, DnsmasqAddr: e.dnsmasq.addr,
		MainConfigs: []string{filepath.Join(dir, "dnsmasq.conf.*")}, DefaultsFile: filepath.Join(dir, "no-defaults"),
		HealthWait: 600 * time.Millisecond,
	}
	return e
}

func (e *tkEnv) wantNoFile(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(e.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("接管文件不该存在: %v", err)
	}
}
func (e *tkEnv) wantNoState(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(e.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("状态文件不该存在: %v", err)
	}
}

func port(addr string) string { _, p, _ := net.SplitHostPort(addr); return p }

// ---- Apply ----

func TestApplyHappyPathAndIdempotent(t *testing.T) {
	e := newTkEnv(t)
	changed, err := e.tk.Apply(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, err := os.ReadFile(e.file)
	if err != nil {
		t.Fatal(err)
	}
	want := takeoverMarker + "\nno-resolv\nserver=127.0.0.1#" + port(e.kernel.addr) + "\n"
	if string(b) != want {
		t.Fatalf("内容不对:\n%s\n期望:\n%s", b, want)
	}
	if fi, _ := os.Stat(e.file); fi.Mode().Perm() != 0o644 {
		t.Errorf("权限 %v", fi.Mode().Perm())
	}
	if st, ok := e.tk.loadState(); !ok || st.Phase != phaseApplied || len(st.Files) != 1 || st.Files[0] != e.file {
		t.Fatalf("状态 %+v %v", st, ok)
	}
	if e.svc.count() != 1 {
		t.Fatalf("应重启 1 次,实际 %d", e.svc.count())
	}
	// 再来一次:已经是这个状态,不改、不重启
	changed, err = e.tk.Apply(context.Background())
	if err != nil || changed || e.svc.count() != 1 {
		t.Fatalf("幂等失败: changed=%v err=%v restarts=%d", changed, err, e.svc.count())
	}
	if s := e.tk.Status(context.Background()); !s.Supported || !s.Applied || !reflect.DeepEqual(s.Dirs, []string{e.conf}) {
		t.Fatalf("Status %+v", s)
	}
}

func TestApplyKernelNotAnswering(t *testing.T) {
	e := newTkEnv(t)
	e.kernel.setUp(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := e.tk.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "没有改任何设置") {
		t.Fatalf("err=%v", err)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if e.svc.count() != 0 {
		t.Fatal("内核没应答时不该重启 dnsmasq")
	}
}

func TestApplyUnsupported(t *testing.T) {
	t.Run("dnsmasq 没在运行", func(t *testing.T) {
		e := newTkEnv(t)
		e.svc.running = false
		_, err := e.tk.Apply(context.Background())
		if !errors.Is(err, ErrTakeoverUnsupported) || !strings.Contains(err.Error(), "hijack") {
			t.Fatalf("err=%v", err)
		}
		e.wantNoFile(t)
		e.wantNoState(t)
	})
	t.Run("查不到服务状态就不动", func(t *testing.T) {
		e := newTkEnv(t)
		e.svc.statusErr = errors.New("init 脚本不见了")
		if _, err := e.tk.Apply(context.Background()); err == nil || errors.Is(err, ErrTakeoverUnsupported) {
			t.Fatalf("err=%v", err)
		}
		e.wantNoFile(t)
		if e.svc.count() != 0 {
			t.Fatal("不该重启")
		}
	})
	t.Run("找不到配置", func(t *testing.T) {
		e := newTkEnv(t)
		e.tk.MainConfigs = []string{filepath.Join(e.dir, "nothing.*")}
		_, err := e.tk.Apply(context.Background())
		if !errors.Is(err, ErrTakeoverUnsupported) || !strings.Contains(err.Error(), "找不到 dnsmasq 的主配置") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("主配置里没有 conf-dir", func(t *testing.T) {
		e := newTkEnv(t)
		_ = os.WriteFile(filepath.Join(e.dir, "dnsmasq.conf.cfg01"), []byte("port=53\n"), 0o644)
		if _, err := e.tk.Apply(context.Background()); !errors.Is(err, ErrTakeoverUnsupported) || !strings.Contains(err.Error(), "没有 conf-dir") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("目录不存在", func(t *testing.T) {
		e := newTkEnv(t)
		_ = os.Remove(e.conf)
		if _, err := e.tk.Apply(context.Background()); !errors.Is(err, ErrTakeoverUnsupported) || !strings.Contains(err.Error(), "目录不存在") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("坏的内核地址", func(t *testing.T) {
		e := newTkEnv(t)
		e.tk.KernelAddr = "nonsense"
		if _, err := e.tk.Apply(context.Background()); err == nil {
			t.Fatal("应失败")
		}
		e.wantNoFile(t)
	})
}

func TestApplyHealthFailureRollsBack(t *testing.T) {
	e := newTkEnv(t)
	// 第 1 次重启(带着接管文件)之后 dnsmasq 不再应答;第 2 次(撤销后)恢复
	e.svc.onRestart = func(n int) { e.dnsmasq.setUp(n != 1) }
	changed, err := e.tk.Apply(context.Background())
	if err == nil || changed || !strings.Contains(err.Error(), "已自动撤销") || !strings.Contains(err.Error(), "不再应答") {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if e.svc.count() != 2 {
		t.Fatalf("应重启 2 次(应用、撤销),实际 %d", e.svc.count())
	}
}

func TestApplyRestartFailureRollsBack(t *testing.T) {
	e := newTkEnv(t)
	e.svc.failAt[1] = true
	_, err := e.tk.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "重启 dnsmasq 失败") || !strings.Contains(err.Error(), "已自动撤销") {
		t.Fatalf("err=%v", err)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if e.svc.count() != 2 {
		t.Fatalf("restarts=%d", e.svc.count())
	}
}

func TestApplyRollbackThatCannotRestartKeepsState(t *testing.T) {
	e := newTkEnv(t)
	e.svc.failAt[1], e.svc.failAt[2] = true, true
	_, err := e.tk.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请手动重启 dnsmasq") {
		t.Fatalf("err=%v", err)
	}
	e.wantNoFile(t) // 文件总是先删掉
	if _, ok := e.tk.loadState(); !ok {
		t.Fatal("撤销没做完,状态文件要留着让 Recover 重试")
	}
}

func TestApplyWriteFailureRollsBackWithoutRestart(t *testing.T) {
	e := newTkEnv(t)
	conf2 := filepath.Join(e.dir, "second.d")
	_ = os.MkdirAll(conf2, 0o755)
	_ = os.WriteFile(filepath.Join(e.dir, "dnsmasq.conf.cfg02"), []byte("conf-dir="+conf2+"\n"), 0o644)
	var wrote []string
	e.tk.WriteFile = func(p string, data []byte, perm os.FileMode) error {
		if strings.HasPrefix(p, conf2) {
			return errors.New("磁盘满了")
		}
		wrote = append(wrote, p)
		return os.WriteFile(p, data, perm)
	}
	_, err := e.tk.Apply(context.Background())
	// 两个目录按路径排序:dnsmasq.cfg01.d 先写成功,second.d 失败 → 前一个要被删掉
	if err == nil || !strings.Contains(err.Error(), "磁盘满了") || !strings.Contains(err.Error(), "已自动撤销") {
		t.Fatalf("err=%v", err)
	}
	if len(wrote) != 1 {
		t.Fatalf("wrote=%v", wrote)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if e.svc.count() != 0 {
		t.Fatal("dnsmasq 还没读过新文件,不该重启")
	}
}

func TestApplyMultipleInstances(t *testing.T) {
	e := newTkEnv(t)
	conf2 := filepath.Join(e.dir, "other.d")
	_ = os.MkdirAll(conf2, 0o755)
	_ = os.WriteFile(filepath.Join(e.dir, "dnsmasq.conf.cfg02"), []byte("conf-dir="+conf2+"\nconf-dir="+e.conf+"\n"), 0o644) // 重复目录只写一次
	if _, err := e.tk.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{e.file, filepath.Join(conf2, TakeoverFileName)} {
		if !isOurs(p) {
			t.Errorf("%s 应已写入", p)
		}
	}
	if e.svc.count() != 1 {
		t.Fatalf("多个实例也只重启一次(服务名是同一个),实际 %d", e.svc.count())
	}
}

// ---- 撤销 / 恢复 / 同步 ----

func TestRevert(t *testing.T) {
	e := newTkEnv(t)
	if _, err := e.tk.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.tk.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if e.svc.count() != 2 {
		t.Fatalf("restarts=%d", e.svc.count())
	}
	// 再撤销一次:什么都不做
	if err := e.tk.Revert(context.Background()); err != nil || e.svc.count() != 2 {
		t.Fatalf("err=%v restarts=%d", err, e.svc.count())
	}
}

func TestRevertLeavesForeignFile(t *testing.T) {
	e := newTkEnv(t)
	_ = os.WriteFile(e.file, []byte("# 这是用户自己的文件\nlog-queries\n"), 0o644)
	_ = os.MkdirAll(filepath.Dir(e.state), 0o700)
	_ = e.tk.saveState(takeoverState{Phase: phaseApplied, Files: []string{e.file}})
	if err := e.tk.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(e.file); !strings.Contains(string(b), "用户自己的") {
		t.Fatal("没有标记的同名文件不能删")
	}
	if e.svc.count() != 0 {
		t.Fatal("什么都没删就不该重启")
	}
	e.wantNoState(t)
}

func TestRevertWithoutStateCleansMarkedLeftover(t *testing.T) {
	e := newTkEnv(t)
	c, _ := e.tk.content()
	_ = os.WriteFile(e.file, []byte(c), 0o644)
	if err := e.tk.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.wantNoFile(t)
	if e.svc.count() != 1 {
		t.Fatalf("restarts=%d", e.svc.count())
	}
}

func TestRevertSkipsRestartWhenDnsmasqNotRunning(t *testing.T) {
	e := newTkEnv(t)
	if _, err := e.tk.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.svc.running = false
	if err := e.tk.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if e.svc.count() != 1 {
		t.Fatalf("dnsmasq 没在跑就不用重启,restarts=%d", e.svc.count())
	}
}

func TestRevertRestartFailureKeepsState(t *testing.T) {
	e := newTkEnv(t)
	if _, err := e.tk.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.svc.failAt[2] = true
	err := e.tk.Revert(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请手动重启 dnsmasq") {
		t.Fatalf("err=%v", err)
	}
	e.wantNoFile(t)
	if _, ok := e.tk.loadState(); !ok {
		t.Fatal("没做完要留着状态")
	}
	// 之后再撤销一次可以补上
	if err := e.tk.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.wantNoState(t)
}

func TestRecover(t *testing.T) {
	t.Run("什么都没有", func(t *testing.T) {
		e := newTkEnv(t)
		if err := e.tk.Recover(context.Background()); err != nil || e.svc.count() != 0 {
			t.Fatalf("err=%v restarts=%d", err, e.svc.count())
		}
	})
	t.Run("已应用且内核正常:保留", func(t *testing.T) {
		e := newTkEnv(t)
		if _, err := e.tk.Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := e.tk.Recover(context.Background()); err != nil || !isOurs(e.file) || e.svc.count() != 1 {
			t.Fatalf("err=%v restarts=%d", err, e.svc.count())
		}
	})
	t.Run("已应用但内核没了:撤销", func(t *testing.T) {
		e := newTkEnv(t)
		if _, err := e.tk.Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.kernel.setUp(false)
		if err := e.tk.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.wantNoFile(t)
		e.wantNoState(t)
	})
	t.Run("应用到一半崩了:撤销,即使内核正常", func(t *testing.T) {
		e := newTkEnv(t)
		c, _ := e.tk.content()
		_ = os.WriteFile(e.file, []byte(c), 0o644)
		_ = e.tk.saveState(takeoverState{Phase: phaseApplying, Files: []string{e.file}})
		if err := e.tk.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.wantNoFile(t)
		e.wantNoState(t)
	})
	t.Run("状态文件丢了但有带标记的遗留文件:撤销", func(t *testing.T) {
		e := newTkEnv(t)
		c, _ := e.tk.content()
		_ = os.WriteFile(e.file, []byte(c), 0o644)
		if err := e.tk.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		e.wantNoFile(t)
	})
}

func TestSync(t *testing.T) {
	e := newTkEnv(t)
	ctx := context.Background()
	if ch, err := e.tk.Sync(ctx, "dnsmasq"); err != nil || !ch || !isOurs(e.file) {
		t.Fatalf("dnsmasq: %v %v", ch, err)
	}
	if ch, err := e.tk.Sync(ctx, "dnsmasq"); err != nil || ch {
		t.Fatalf("重复 dnsmasq: %v %v", ch, err)
	}
	if ch, err := e.tk.Sync(ctx, "hijack"); err != nil || !ch {
		t.Fatalf("hijack: %v %v", ch, err)
	}
	e.wantNoFile(t)
	if ch, err := e.tk.Sync(ctx, "off"); err != nil || ch {
		t.Fatalf("off: %v %v", ch, err)
	}
	n := e.svc.count()
	if _, err := e.tk.Sync(ctx, "bogus"); err == nil || e.svc.count() != n {
		t.Fatalf("未知模式应报错且不动系统: %v", err)
	}
}

// ---- 找配置目录 ----

func TestParseConfDirsAndFilters(t *testing.T) {
	got := parseConfDirs("# conf-dir=/ignored\nconf-dir=/tmp/dnsmasq.d\n conf-dir = /etc/dnsmasq.d/,*.conf\nconf-dir=relative/dir\nconf-dir=/x,.bak,~\nport=53\n")
	want := []confDir{
		{Dir: "/tmp/dnsmasq.d"},
		{Dir: "/etc/dnsmasq.d", Filters: []string{"*.conf"}},
		{Dir: "/x", Filters: []string{".bak", "~"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	d := parseDefaultsConfigDir("# 注释\nENABLED=1\nCONFIG_DIR=/etc/dnsmasq.d,.dpkg-dist,.dpkg-old,.dpkg-new\n")
	if len(d) != 1 || d[0].Dir != "/etc/dnsmasq.d" || len(d[0].Filters) != 3 {
		t.Fatalf("defaults %+v", d)
	}
	if q := parseDefaultsConfigDir(`CONFIG_DIR="/etc/dnsmasq.d,.bak"`); len(q) != 1 || q[0].Dir != "/etc/dnsmasq.d" {
		t.Fatalf("带引号 %+v", q)
	}
	for _, c := range []struct {
		cd   confDir
		want bool
	}{
		{confDir{Dir: "/a"}, true},
		{confDir{Dir: "/a", Filters: []string{"*.conf"}}, true},
		{confDir{Dir: "/a", Filters: []string{"*.cfg"}}, false},
		{confDir{Dir: "/a", Filters: []string{".dpkg-dist", ".bak"}}, true},
		{confDir{Dir: "/a", Filters: []string{".conf"}}, false}, // 排除以 .conf 结尾的
		{confDir{Dir: "/a", Filters: []string{"*.conf", ".conf"}}, false},
	} {
		if got := c.cd.allows(TakeoverFileName); got != c.want {
			t.Errorf("%+v: got %v", c.cd, got)
		}
	}
}

func TestDiscoverDebianLayoutAndWarnings(t *testing.T) {
	e := newTkEnv(t)
	deb := filepath.Join(e.dir, "etc-dnsmasq.d")
	_ = os.MkdirAll(deb, 0o755)
	_ = os.WriteFile(filepath.Join(e.dir, "default-dnsmasq"), []byte("CONFIG_DIR="+deb+",.dpkg-dist\n"), 0o644)
	_ = os.WriteFile(filepath.Join(e.dir, "dnsmasq.conf.cfg01"), []byte("conf-dir="+e.conf+"\nserver=8.8.8.8\nserver=/example.com/1.1.1.1\nserver=8.8.8.8\n"), 0o644)
	e.tk.DefaultsFile = filepath.Join(e.dir, "default-dnsmasq")
	d := e.tk.discover()
	want := []string{filepath.Join(deb, TakeoverFileName), e.file}
	if !reflect.DeepEqual(d.Files, want) && !reflect.DeepEqual(d.Files, []string{want[1], want[0]}) {
		t.Fatalf("files=%v", d.Files)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "server=8.8.8.8") {
		t.Fatalf("warnings=%v", d.Warnings)
	}
	if s := e.tk.Status(context.Background()); len(s.Warnings) != 1 || len(s.Dirs) != 2 {
		t.Fatalf("status=%+v", s)
	}
}

func TestFilterThatExcludesOurFile(t *testing.T) {
	e := newTkEnv(t)
	_ = os.WriteFile(filepath.Join(e.dir, "dnsmasq.conf.cfg01"), []byte("conf-dir="+e.conf+",*.cfg\n"), 0o644)
	_, err := e.tk.Apply(context.Background())
	if !errors.Is(err, ErrTakeoverUnsupported) || !strings.Contains(err.Error(), "过滤规则") {
		t.Fatalf("err=%v", err)
	}
	e.wantNoFile(t)
}

// ---- ProbeDNS ----

func TestProbeDNS(t *testing.T) {
	f := startFakeDNS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ProbeDNS(ctx, f.addr); err != nil {
		t.Fatalf("应答时应成功: %v", err)
	}
	f.mode.Store(1)
	short, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if err := ProbeDNS(short, f.addr); err == nil {
		t.Fatal("不应答应失败")
	}
	f.mode.Store(2) // 事务号不对的应答要被忽略
	short2, cancel3 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel3()
	if err := ProbeDNS(short2, f.addr); err == nil {
		t.Fatal("事务号不对不算应答")
	}
}

func TestBuildProbeQuery(t *testing.T) {
	q, id, err := buildProbeQuery("a.b.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if q[0] != id[0] || q[1] != id[1] || q[4] != 0 || q[5] != 1 {
		t.Fatalf("头部不对: %v", q[:12])
	}
	want := []byte{1, 'a', 1, 'b', 7, 'i', 'n', 'v', 'a', 'l', 'i', 'd', 0, 0, 1, 0, 1}
	if !reflect.DeepEqual(q[12:], want) {
		t.Fatalf("问题段不对: %v", q[12:])
	}
	if _, _, err := buildProbeQuery("a..b"); err == nil {
		t.Fatal("空标签应报错")
	}
	if _, _, err := buildProbeQuery(strings.Repeat("a", 64) + ".invalid"); err == nil {
		t.Fatal("过长标签应报错")
	}
}

// ---- 真 dnsmasq 的端到端验证 ----

// realDnsmasq 把一个真的 dnsmasq 进程包装成 TakeoverServices:restart = 杀掉再按同样的参数拉起。
type realDnsmasq struct {
	t    *testing.T
	bin  string
	conf string
	mu   sync.Mutex
	cmd  *exec.Cmd
	out  *strings.Builder
	done chan struct{}
}

func (r *realDnsmasq) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out = &strings.Builder{}
	cmd := exec.Command(r.bin, "--keep-in-foreground", "--conf-file="+r.conf, "--pid-file=")
	cmd.Stdout, cmd.Stderr = r.out, r.out
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd = cmd
	r.done = make(chan struct{})
	go func(c *exec.Cmd, d chan struct{}) { _ = c.Wait(); close(d) }(cmd, r.done)
	return nil
}

func (r *realDnsmasq) stop() {
	r.mu.Lock()
	cmd, done := r.cmd, r.done
	r.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = cmd.Process.Kill()
	<-done
}

func (r *realDnsmasq) Status(_ context.Context, _ string) (ServiceInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return ServiceInfo{}, nil
	}
	select {
	case <-r.done:
		return ServiceInfo{}, nil
	default:
		return ServiceInfo{Running: true}, nil
	}
}

func (r *realDnsmasq) Do(_ context.Context, _, action string) (ActionResult, error) {
	if action != "restart" {
		return ActionResult{}, errors.New("只支持 restart")
	}
	r.stop()
	if err := r.start(); err != nil {
		return ActionResult{OK: false, Code: 1, Stderr: err.Error()}, nil
	}
	return ActionResult{OK: true}, nil
}

func TestTakeoverWithRealDnsmasq(t *testing.T) {
	bin, err := exec.LookPath("dnsmasq")
	if err != nil {
		t.Skip("没有 dnsmasq 可执行文件,跳过端到端验证")
	}
	// 模拟 OpenWrt 的布局:上游来自 resolv-file(「运营商 DNS」在 127.0.0.2:53),dnsmasq 监听另一个端口
	isp := startFakeDNSAt(t, "127.0.0.2:53") // 绑不了(没有 root)就跳过
	kernel := startFakeDNS(t)
	probe := startFakeDNS(t) // 只为拿一个空闲的 UDP 端口号
	dmAddr := probe.addr
	probe.conn.Close()

	dir := t.TempDir()
	conf := filepath.Join(dir, "dnsmasq.d")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	resolv := filepath.Join(dir, "resolv.conf.auto")
	_ = os.WriteFile(resolv, []byte("nameserver 127.0.0.2\n"), 0o644)
	main := filepath.Join(dir, "dnsmasq.conf.cfg01")
	_ = os.WriteFile(main, []byte("port="+port(dmAddr)+"\nlisten-address=127.0.0.1\nbind-interfaces\nuser=root\ngroup=root\n"+
		"resolv-file="+resolv+"\ncache-size=0\nno-hosts\nconf-dir="+conf+"\n"), 0o644)

	dm := &realDnsmasq{t: t, bin: bin, conf: main}
	if err := dm.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dm.stop)
	tk := &DNSTakeover{
		StateFile: filepath.Join(dir, "state", "dns-takeover.json"), Services: dm, KernelAddr: kernel.addr, DnsmasqAddr: dmAddr,
		MainConfigs: []string{filepath.Join(dir, "dnsmasq.conf.*")}, DefaultsFile: filepath.Join(dir, "none"), HealthWait: 8 * time.Second,
	}
	ctx := context.Background()
	waitAnswer := func() {
		t.Helper()
		var last error
		for i := 0; i < 40; i++ {
			if last = ProbeDNS(ctx, dmAddr); last == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("dnsmasq 一直不应答: %v\n%s", last, dm.out.String())
	}
	reset := func() { isp.hits.Store(0); kernel.hits.Store(0) }

	// 1) 接管之前:查询走 resolv-file 里的「运营商 DNS」
	waitAnswer()
	if isp.hits.Load() == 0 {
		t.Fatalf("接管前查询应到达原上游\n%s", dm.out.String())
	}

	// 2) 接管之后:查询只到内核,不再到原上游
	reset()
	if changed, err := tk.Apply(ctx); err != nil || !changed {
		t.Fatalf("Apply: changed=%v err=%v\n%s", changed, err, dm.out.String())
	}
	reset() // Apply 自己的探测不算
	for i := 0; i < 3; i++ {
		if err := ProbeDNS(ctx, dmAddr); err != nil {
			t.Fatalf("接管后 dnsmasq 应能答: %v", err)
		}
	}
	if kernel.hits.Load() == 0 || isp.hits.Load() != 0 {
		t.Fatalf("接管后查询应只到内核: kernel=%d isp=%d\n%s", kernel.hits.Load(), isp.hits.Load(), dm.out.String())
	}
	if !tk.Status(ctx).Applied {
		t.Fatal("Status 应显示已接管")
	}

	// 3) 撤销之后:恢复到原上游
	if err := tk.Revert(ctx); err != nil {
		t.Fatalf("Revert: %v\n%s", err, dm.out.String())
	}
	reset()
	waitAnswer()
	if isp.hits.Load() == 0 || kernel.hits.Load() != 0 {
		t.Fatalf("撤销后查询应回到原上游: kernel=%d isp=%d", kernel.hits.Load(), isp.hits.Load())
	}
	if _, err := os.Stat(filepath.Join(conf, TakeoverFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("撤销后文件应不在: %v", err)
	}

	// 4) 内核没了、dnsmasq 却指着它(模拟后端崩溃后重启):Recover 撤销
	if _, err := tk.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	kernel.setUp(false)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := tk.Recover(rctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	reset()
	waitAnswer()
	if isp.hits.Load() == 0 {
		t.Fatal("Recover 之后应回到原上游")
	}
}
