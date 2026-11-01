package factoryreset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// events 记录发生的事情和顺序,用来断言「先停内核、再清数据、密码最后」。
type events struct {
	mu sync.Mutex
	ev []string
}

func (e *events) add(s string) { e.mu.Lock(); e.ev = append(e.ev, s); e.mu.Unlock() }
func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ev...)
}

type fakeSvc struct {
	ev        *events
	running   bool
	statusErr error
	stopRes   system.ActionResult
	stopErr   error
	// stays:停止命令成功之后内核仍然在运行
	stays bool
	// afterErr:停止之后再读状态时报错
	afterErr error
	stopped  bool
}

func (f *fakeSvc) Status(ctx context.Context, name string) (system.ServiceInfo, error) {
	f.ev.add("status")
	if f.stopped && f.afterErr != nil {
		return system.ServiceInfo{}, f.afterErr
	}
	if f.statusErr != nil {
		return system.ServiceInfo{}, f.statusErr
	}
	return system.ServiceInfo{Running: f.running}, nil
}

func (f *fakeSvc) Do(ctx context.Context, name, action string) (system.ActionResult, error) {
	f.ev.add(action + ":" + name)
	if f.stopErr != nil {
		return system.ActionResult{}, f.stopErr
	}
	if f.stopRes.OK && !f.stays {
		f.running = false
	}
	f.stopped = true
	return f.stopRes, nil
}

type fakeDNS struct {
	ev  *events
	err error
}

func (d fakeDNS) BeforeKernelStop(context.Context) error { d.ev.add("dns-revert"); return d.err }

func step(ev *events, name string, err error) Step {
	return Step{Name: name, Run: func(context.Context) error { ev.add("wipe:" + name); return err }}
}

func newSvc(ev *events, svc *fakeSvc) *Service {
	s := &Service{
		CoreService: "sing-box",
		DNS:         fakeDNS{ev: ev},
		Wipes:       []Step{step(ev, "a", nil), step(ev, "b", nil), step(ev, "c", nil)},
		Final:       step(ev, "password", nil),
	}
	if svc != nil {
		s.Services = svc
	}
	return s
}

func okStop() system.ActionResult { return system.ActionResult{OK: true} }

func TestRunHappyPathOrder(t *testing.T) {
	ev := &events{}
	svc := &fakeSvc{ev: ev, running: true, stopRes: okStop()}
	res, err := newSvc(ev, svc).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (Result{OK: true, Seeded: 2, ProfileSeeded: true, KernelStopped: true}); res != want {
		t.Errorf("结果 = %+v,想要 %+v", res, want)
	}
	want := []string{"status", "dns-revert", "stop:sing-box", "status", "wipe:a", "wipe:b", "wipe:c", "wipe:password"}
	if got := ev.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("顺序不对:\n got  %v\n want %v", got, want)
	}
}

func TestRunKernelAlreadyStopped(t *testing.T) {
	ev := &events{}
	svc := &fakeSvc{ev: ev, running: false, stopRes: okStop()}
	res, err := newSvc(ev, svc).Run(context.Background())
	if err != nil || !res.OK || !res.KernelStopped {
		t.Fatalf("%+v %v", res, err)
	}
	got := ev.get()
	if got[0] != "status" || got[1] != "dns-revert" {
		t.Errorf("内核已停也要撤销 DNS 接管: %v", got)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "stop:") {
			t.Errorf("已经停了就不该再发停止命令: %v", got)
		}
	}
}

// 内核没能停下来的各种情形:一份数据都不删、密码也不动,错误是 *StopError。
func TestRunStopFailuresWipeNothing(t *testing.T) {
	cases := []struct {
		name string
		svc  func(ev *events) *fakeSvc
		dns  error
		want string
	}{
		{"状态读不到", func(ev *events) *fakeSvc { return &fakeSvc{ev: ev, statusErr: errors.New("exec 失败")} }, nil, "读不到内核服务的状态"},
		{"撤销 DNS 接管失败", func(ev *events) *fakeSvc { return &fakeSvc{ev: ev, running: true, stopRes: okStop()} }, errors.New("dnsmasq 重启失败"), "dnsmasq 重启失败"},
		{"停止命令失败", func(ev *events) *fakeSvc {
			return &fakeSvc{ev: ev, running: true, stopRes: system.ActionResult{OK: false, Code: 1, Stderr: "no such service"}}
		}, nil, "no such service"},
		{"停止命令出错", func(ev *events) *fakeSvc {
			return &fakeSvc{ev: ev, running: true, stopErr: errors.New("超时")}
		}, nil, "超时"},
		{"停了还在运行", func(ev *events) *fakeSvc {
			return &fakeSvc{ev: ev, running: true, stopRes: okStop(), stays: true}
		}, nil, "仍在运行"},
		{"停了之后读不到状态", func(ev *events) *fakeSvc {
			return &fakeSvc{ev: ev, running: true, stopRes: okStop(), afterErr: errors.New("读状态失败")}
		}, nil, "读不到服务状态"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := &events{}
			s := newSvc(ev, c.svc(ev))
			s.DNS = fakeDNS{ev: ev, err: c.dns}
			res, err := s.Run(context.Background())
			var se *StopError
			if !errors.As(err, &se) {
				t.Fatalf("应是 *StopError: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "没有清除任何数据") {
				t.Errorf("错误信息 = %q,应含 %q 和「没有清除任何数据」", err, c.want)
			}
			if res.OK {
				t.Errorf("不该成功: %+v", res)
			}
			for _, e := range ev.get() {
				if strings.HasPrefix(e, "wipe:") {
					t.Fatalf("不该清任何东西: %v", ev.get())
				}
			}
		})
	}
}

// 管不了内核(没有服务管理器 / 没接服务):跳过停内核,照常清数据,kernelStopped 是 false。
func TestRunWithoutServiceManager(t *testing.T) {
	ev := &events{}
	s := newSvc(ev, &fakeSvc{ev: ev, statusErr: system.ErrNoInit})
	res, err := s.Run(context.Background())
	if err != nil || !res.OK || res.KernelStopped {
		t.Fatalf("%+v %v", res, err)
	}
	for _, e := range ev.get() {
		if e == "dns-revert" || strings.HasPrefix(e, "stop:") {
			t.Errorf("没有服务管理器时不该动 DNS 接管或内核: %v", ev.get())
		}
	}

	ev2 := &events{}
	res, err = newSvc(ev2, nil).Run(context.Background())
	if err != nil || !res.OK || res.KernelStopped {
		t.Fatalf("没接服务: %+v %v", res, err)
	}
	if want := []string{"wipe:a", "wipe:b", "wipe:c", "wipe:password"}; !reflect.DeepEqual(ev2.get(), want) {
		t.Errorf("%v", ev2.get())
	}
}

// 一项清不掉不拦后面的,但访问密码不能清:面板不能在旧数据还留着时变成没有密码。
func TestRunWipeFailureKeepsPassword(t *testing.T) {
	ev := &events{}
	svc := &fakeSvc{ev: ev, running: true, stopRes: okStop()}
	s := newSvc(ev, svc)
	s.Wipes = []Step{step(ev, "a", nil), step(ev, "b", errors.New("磁盘只读")), step(ev, "c", nil)}
	_, err := s.Run(context.Background())
	var pe *PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("应是 *PartialError: %v", err)
	}
	if !reflect.DeepEqual(pe.Done, []string{"a", "c"}) || len(pe.Failed) != 1 || !strings.Contains(pe.Failed[0], "b:磁盘只读") {
		t.Errorf("报告不对: %+v", pe)
	}
	if !strings.Contains(err.Error(), "访问密码没有清除") {
		t.Errorf("信息要说明密码没清: %q", err)
	}
	got := ev.get()
	if got[len(got)-1] == "wipe:password" {
		t.Errorf("有失败就不能清密码: %v", got)
	}
	if !reflect.DeepEqual(got[4:], []string{"wipe:a", "wipe:b", "wipe:c"}) {
		t.Errorf("后面的项仍要继续清: %v", got)
	}
}

func TestRunFinalFailureReported(t *testing.T) {
	ev := &events{}
	s := newSvc(ev, &fakeSvc{ev: ev, running: false, stopRes: okStop()})
	s.Final = step(ev, "password", errors.New("删不掉 auth.json"))
	_, err := s.Run(context.Background())
	var pe *PartialError
	if !errors.As(err, &pe) || !reflect.DeepEqual(pe.Done, []string{"a", "b", "c"}) || len(pe.Failed) != 1 || !strings.Contains(pe.Failed[0], "删不掉 auth.json") {
		t.Fatalf("%v %+v", err, pe)
	}
}

func TestRunNoFinalStep(t *testing.T) {
	ev := &events{}
	s := newSvc(ev, nil)
	s.Final = Step{}
	if res, err := s.Run(context.Background()); err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
}

// 同一时刻只做一次;和部署互斥。
func TestRunBusy(t *testing.T) {
	ev := &events{}
	entered, release := make(chan struct{}), make(chan struct{})
	s := newSvc(ev, nil)
	var once sync.Once
	s.Wipes = []Step{{Name: "slow", Run: func(context.Context) error { once.Do(func() { close(entered) }); <-release; return nil }}}
	done := make(chan error, 1)
	go func() { _, err := s.Run(context.Background()); done <- err }()
	<-entered
	if _, err := s.Run(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("第二次应是 ErrBusy: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("第一次结束后应能再做: %v", err)
	}
}

func TestRunExclusive(t *testing.T) {
	ev := &events{}
	s := newSvc(ev, nil)
	s.Exclusive = func(fn func() error) error { ev.add("lock"); defer ev.add("unlock"); return fn() }
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := ev.get()
	if got[0] != "lock" || got[len(got)-1] != "unlock" {
		t.Errorf("整个过程应在锁里: %v", got)
	}

	ev2 := &events{}
	s2 := newSvc(ev2, &fakeSvc{ev: ev2, running: true, stopRes: okStop()})
	s2.Exclusive = func(func() error) error { return ErrBusy } // 部署正在进行
	if _, err := s2.Run(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("部署在进行应是 ErrBusy: %v", err)
	}
	if len(ev2.get()) != 0 {
		t.Errorf("部署在进行时什么都不该做: %v", ev2.get())
	}
}

func TestRemoveFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	if err := os.WriteFile(a, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFiles("x", a, b).Run(context.Background()); err != nil { // b 不存在不算错
		t.Fatal(err)
	}
	if _, err := os.Stat(a); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a 应已删除: %v", err)
	}
	if err := RemoveFiles("x", "").Run(context.Background()); err == nil {
		t.Error("空路径应报错")
	}
	// 删不掉的(非空目录占着名字)要报错,但其余的照删
	c, d := filepath.Join(dir, "c.json"), filepath.Join(dir, "d.json")
	if err := os.MkdirAll(filepath.Join(c, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFiles("x", c, d).Run(context.Background()); err == nil {
		t.Error("删不掉应报错")
	}
	if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("出错不该拦住后面的文件: %v", err)
	}
}

func TestClearDirs(t *testing.T) {
	root := t.TempDir()
	rules, cache := filepath.Join(root, "rules"), filepath.Join(root, "dns-filter")
	for _, p := range []string{filepath.Join(rules, "list-1.srs"), filepath.Join(rules, "sub", "deep.srs"), filepath.Join(cache, "x.txt")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(root, "nope")
	if err := ClearDirs("x", rules, cache, missing).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{rules, cache} {
		entries, err := os.ReadDir(d)
		if err != nil || len(entries) != 0 {
			t.Errorf("%s 应还在且是空的: %v %v", d, entries, err)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("不存在的目录不该被创建: %v", err)
	}
}

func TestClearDirsRefusesDangerousPaths(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"", string(filepath.Separator), "/"} {
		if err := ClearDirs("x", d).Run(context.Background()); err == nil {
			t.Errorf("%q 应被拒绝", d)
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal(err)
	}
}
