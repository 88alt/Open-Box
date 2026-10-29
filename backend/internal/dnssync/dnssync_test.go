package dnssync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

type fakeTaker struct {
	calls      []string
	syncErrs   []error // 依次返回;用完后返回 nil
	changed    bool
	revertErr  error
	recoverErr error
}

func (f *fakeTaker) Sync(ctx context.Context, mode string) (bool, error) {
	f.calls = append(f.calls, "sync:"+mode)
	if len(f.syncErrs) > 0 {
		e := f.syncErrs[0]
		f.syncErrs = f.syncErrs[1:]
		if e != nil {
			return false, e
		}
	}
	return f.changed, nil
}
func (f *fakeTaker) Revert(ctx context.Context) error {
	f.calls = append(f.calls, "revert")
	return f.revertErr
}
func (f *fakeTaker) Recover(ctx context.Context) error {
	f.calls = append(f.calls, "recover")
	return f.recoverErr
}

func mode(m string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return m, err }
}

func newC(tk *fakeTaker, m string) *Coordinator {
	return &Coordinator{Taker: tk, Mode: mode(m, nil), Wait: 200 * time.Millisecond, Poll: 2 * time.Millisecond}
}

func TestBeforeKernelStop(t *testing.T) {
	tk := &fakeTaker{}
	if err := newC(tk, "dnsmasq").BeforeKernelStop(context.Background()); err != nil || fmt.Sprint(tk.calls) != "[revert]" {
		t.Fatalf("%v %v", err, tk.calls)
	}
	tk = &fakeTaker{revertErr: errors.New("删不掉")}
	err := newC(tk, "dnsmasq").BeforeKernelStop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "撤销 DNS 接管失败") || !strings.Contains(err.Error(), "删不掉") {
		t.Fatalf("撤销失败必须报出来,调用方才知道不能再动内核: %v", err)
	}
}

func TestAfterKernelStartModes(t *testing.T) {
	for _, m := range []string{"dnsmasq", "hijack", "off"} {
		tk := &fakeTaker{changed: true}
		res, err := newC(tk, m).AfterKernelStart(context.Background())
		if err != nil || res.Mode != m || !res.Changed || fmt.Sprint(tk.calls) != "[sync:"+m+"]" {
			t.Fatalf("%s: %+v %v %v", m, res, err, tk.calls)
		}
	}
}

func TestAfterKernelStartWaitsForKernelDNS(t *testing.T) {
	down := fmt.Errorf("内核没应答: %w", system.ErrKernelDNSDown)
	tk := &fakeTaker{syncErrs: []error{down, down, nil}, changed: true}
	res, err := newC(tk, "dnsmasq").AfterKernelStart(context.Background())
	if err != nil || !res.Changed || len(tk.calls) != 3 {
		t.Fatalf("内核入站没起来时应重试直到成功: %+v %v %v", res, err, tk.calls)
	}
}

func TestAfterKernelStartGivesUpWhenKernelNeverAnswers(t *testing.T) {
	down := fmt.Errorf("内核没应答: %w", system.ErrKernelDNSDown)
	tk := &fakeTaker{}
	for i := 0; i < 1000; i++ {
		tk.syncErrs = append(tk.syncErrs, down)
	}
	c := newC(tk, "dnsmasq")
	c.Wait = 30 * time.Millisecond
	_, err := c.AfterKernelStart(context.Background())
	if !errors.Is(err, system.ErrKernelDNSDown) {
		t.Fatalf("等不到要把原因交回去: %v", err)
	}
	if len(tk.calls) < 2 || len(tk.calls) > 100 {
		t.Fatalf("应在时限内重试,不是一次就放弃也不是无限: %d 次", len(tk.calls))
	}
}

func TestAfterKernelStartDoesNotRetryOtherErrors(t *testing.T) {
	for _, e := range []error{
		fmt.Errorf("%w:dnsmasq 没在运行", system.ErrTakeoverUnsupported),
		errors.New("接管后 dnsmasq 不再应答 DNS(已自动撤销)"),
	} {
		tk := &fakeTaker{syncErrs: []error{e}}
		_, err := newC(tk, "dnsmasq").AfterKernelStart(context.Background())
		if err == nil || len(tk.calls) != 1 {
			t.Fatalf("不是「内核还没起来」就别重试: %v %v", err, tk.calls)
		}
	}
}

func TestAfterKernelStartModeUnreadableTouchesNothing(t *testing.T) {
	tk := &fakeTaker{}
	c := &Coordinator{Taker: tk, Mode: mode("", errors.New("档案坏了"))}
	_, err := c.AfterKernelStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "没有改 DNS") || len(tk.calls) != 0 {
		t.Fatalf("读不到模式时不能猜: %v %v", err, tk.calls)
	}
}

func TestAfterKernelStartContextCancelStopsWaiting(t *testing.T) {
	down := fmt.Errorf("x: %w", system.ErrKernelDNSDown)
	tk := &fakeTaker{}
	for i := 0; i < 1000; i++ {
		tk.syncErrs = append(tk.syncErrs, down)
	}
	c := newC(tk, "dnsmasq")
	c.Wait, c.Poll = time.Minute, 50*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.AfterKernelStart(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("取消后要马上停: %v %v", err, time.Since(start))
	}
}

func TestRecoverOnlyLogs(t *testing.T) {
	tk := &fakeTaker{recoverErr: errors.New("清不掉")}
	newC(tk, "dnsmasq").Recover(context.Background()) // 失败不 panic、不返回,只记日志
	if fmt.Sprint(tk.calls) != "[recover]" {
		t.Fatal(tk.calls)
	}
}

func TestNote(t *testing.T) {
	cases := []struct {
		res  Result
		err  error
		want string // 子串;空 = 必须返回空串
	}{
		{Result{Mode: "dnsmasq", Changed: true}, nil, "转发给内核"},
		{Result{Mode: "hijack", Changed: true}, nil, "已撤销"},
		{Result{Mode: "dnsmasq"}, nil, ""},
		{Result{Mode: "off"}, nil, ""},
		{Result{Mode: "dnsmasq"}, errors.New("没找到 conf-dir"), "没找到 conf-dir"},
	}
	for i, c := range cases {
		got := Note(c.res, c.err)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("#%d: %q 想要含 %q", i, got, c.want)
		}
	}
	if !strings.Contains(Note(Result{}, errors.New("x")), "DNS 保持原样") {
		t.Error("失败的话要明说 DNS 没动")
	}
}

func TestModeFromProfile(t *testing.T) {
	get := func(p map[string]any, err error) func(context.Context) (map[string]any, error) {
		return func(context.Context) (map[string]any, error) { return p, err }
	}
	for _, m := range []string{"dnsmasq", "hijack", "off"} {
		got, err := ModeFromProfile(get(map[string]any{"dns": map[string]any{"mode": m}}, nil))(context.Background())
		if err != nil || got != m {
			t.Fatalf("%s: %q %v", m, got, err)
		}
	}
	for name, g := range map[string]func(context.Context) (map[string]any, error){
		"读档案失败":      get(nil, errors.New("坏了")),
		"没有 dns":     get(map[string]any{}, nil),
		"没有 mode":    get(map[string]any{"dns": map[string]any{}}, nil),
		"mode 为空":    get(map[string]any{"dns": map[string]any{"mode": ""}}, nil),
		"mode 不是字符串": get(map[string]any{"dns": map[string]any{"mode": 3}}, nil),
	} {
		if got, err := ModeFromProfile(g)(context.Background()); err == nil || got != "" {
			t.Errorf("%s: 不确定就不动,必须返回错误: %q %v", name, got, err)
		}
	}
}
