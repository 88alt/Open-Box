package system

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsqlog"
)

// 真 dnsmasq 的查询日志端到端验证(没有 dnsmasq 可执行文件时跳过):
// 接管时加的 log-queries / log-facility 要真的让 dnsmasq 写出日志,日志行要能被 dnsqlog 认出来;
// 日志打不开时 dnsmasq 会直接退出,Apply 要退回「不带日志的接管」。

func (r *realDnsmasq) alive() bool {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

type realLogEnv struct {
	dir, conf, run string
	kernel         *fakeDNS
	dmAddr         string
	dm             *realDnsmasq
	tk             *DNSTakeover
}

func newRealLogEnv(t *testing.T) *realLogEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("后端只跑在 OpenWrt / Debian 上")
	}
	bin, err := exec.LookPath("dnsmasq")
	if err != nil {
		t.Skip("没有 dnsmasq 可执行文件,跳过端到端验证")
	}
	e := &realLogEnv{dir: t.TempDir(), kernel: startFakeDNS(t)}
	probe := startFakeDNS(t) // 只为拿一个空闲的 UDP 端口号
	e.dmAddr = probe.addr
	probe.conn.Close()
	e.conf = filepath.Join(e.dir, "dnsmasq.d")
	e.run = filepath.Join(e.dir, "run")
	for _, d := range []string{e.conf, e.run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	main := filepath.Join(e.dir, "dnsmasq.conf.cfg01")
	text := "port=" + port(e.dmAddr) + "\nlisten-address=127.0.0.1\nbind-interfaces\nuser=root\ngroup=root\n" +
		"no-resolv\ncache-size=0\nno-hosts\nconf-dir=" + e.conf + "\n"
	if err := os.WriteFile(main, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	e.dm = &realDnsmasq{t: t, bin: bin, conf: main}
	if err := e.dm.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.dm.stop)
	e.tk = &DNSTakeover{
		StateFile: filepath.Join(e.dir, "state", "dns-takeover.json"), Services: e.dm, KernelAddr: e.kernel.addr, DnsmasqAddr: e.dmAddr,
		MainConfigs: []string{main}, DefaultsFile: filepath.Join(e.dir, "none"), HealthWait: 8 * time.Second,
	}
	return e
}

// ask 向 dnsmasq 发一次 A 查询(只要它回包就行,内核那头的假 DNS 一律答 NXDOMAIN)。
func (e *realLogEnv) ask(t *testing.T, name string) {
	t.Helper()
	q := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(name, ".") {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1)
	c, err := net.Dial("udp", e.dmAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(q); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 512)); err != nil {
		t.Fatalf("dnsmasq 没回包: %v\n%s", err, e.dm.out.String())
	}
}

func TestTakeoverQueryLogWithRealDnsmasq(t *testing.T) {
	e := newRealLogEnv(t)
	lp := filepath.Join(e.run, "openbox-dnsmasq.log")
	e.tk.QueryLog = lp
	ctx := context.Background()

	if changed, err := e.tk.Apply(ctx); err != nil || !changed {
		t.Fatalf("Apply: changed=%v err=%v\n%s", changed, err, e.dm.out.String())
	}
	if path, skip := e.tk.QueryLogState(); path != lp || skip != "" {
		t.Fatalf("QueryLogState = %q %q\n%s", path, skip, e.dm.out.String())
	}
	e.ask(t, "blocked.example.com")

	// 日志里要出现这条查询;逐行交给 dnsqlog,要认得出域名、类型、来源和应答
	var text string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(lp)
		text = string(b)
		if strings.Contains(text, "reply blocked.example.com is NXDOMAIN") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(text, "query[A] blocked.example.com from 127.0.0.1") || !strings.Contains(text, "reply blocked.example.com is NXDOMAIN") {
		t.Fatalf("日志里没有预期的行:\n%s\n--- dnsmasq 输出 ---\n%s", text, e.dm.out.String())
	}
	l := dnsqlog.New()
	for _, line := range strings.Split(text, "\n") {
		l.Feed(line, time.Now())
	}
	rows := l.Records(dnsqlog.Query{Search: "blocked.example.com"}).Rows
	if len(rows) != 1 || rows[0].Domain != "blocked.example.com" || rows[0].QType != "A" || rows[0].Source != "127.0.0.1" || rows[0].Result != "allowed" {
		t.Fatalf("记录不对: %+v\n日志:\n%s", rows, text)
	}

	// 撤销:日志文件删掉,dnsmasq 照常答
	if err := e.tk.Revert(ctx); err != nil {
		t.Fatalf("Revert: %v\n%s", err, e.dm.out.String())
	}
	if _, err := os.Stat(lp); !os.IsNotExist(err) {
		t.Fatalf("撤销后日志文件应被删掉: %v", err)
	}
	if !e.dm.alive() { // 撤销后没有上游了(no-resolv),答不出来是正常的;只要 dnsmasq 还在运行
		t.Fatalf("撤销后 dnsmasq 没在运行\n%s", e.dm.out.String())
	}
}

func TestTakeoverQueryLogFallsBackWhenLogCannotBeOpened(t *testing.T) {
	e := newRealLogEnv(t)
	// 日志路径是个目录:dnsmasq 打不开它,会直接退出(cannot open log ...: Is a directory)
	bad := filepath.Join(e.run, "dir.log")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	e.tk.QueryLog = bad
	e.tk.HealthWait = 2 * time.Second // dnsmasq 已经退出了,不用等太久
	ctx := context.Background()

	if changed, err := e.tk.Apply(ctx); err != nil || !changed {
		t.Fatalf("接管本身应当成功: changed=%v err=%v\n%s", changed, err, e.dm.out.String())
	}
	if path, skip := e.tk.QueryLogState(); path != "" || skip != LogSkipFailed {
		t.Fatalf("QueryLogState = %q %q", path, skip)
	}
	b, _ := os.ReadFile(filepath.Join(e.conf, TakeoverFileName))
	if strings.Contains(string(b), "log-") {
		t.Fatalf("应已去掉日志行:\n%s", b)
	}
	e.ask(t, "still-works.example.com") // 接管照样生效:查询到了内核(假 DNS 答 NXDOMAIN)
	if e.kernel.hits.Load() == 0 {
		t.Fatal("接管后查询应到达内核")
	}
	if !strings.Contains(e.dm.out.String(), "cannot open log") {
		t.Logf("dnsmasq 的输出里没有「cannot open log」,说明它没有因为日志退出:\n%s", e.dm.out.String())
	}
	// 再部署一次:failed 记着,不再重启
	if changed, err := e.tk.Apply(ctx); err != nil || changed {
		t.Fatalf("不该重试: changed=%v err=%v", changed, err)
	}
}
