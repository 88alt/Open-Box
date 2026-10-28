package traffic

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

func freeAddr(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return "127.0.0.1", l.Addr().(*net.TCPAddr).Port
}

// 真实 sing-box:经 mixed 入站连一个本机 TCP 服务,采集器应记到「直连」出口、来源 127.0.0.1、访问目标 localhost。
// 没有 sing-box 可执行文件时跳过。
func TestCollectorRealKernel(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box 可执行文件")
	}
	_, inPort := freeAddr(t)
	_, apiPort := freeAddr(t)
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")
	cfg := fmt.Sprintf(`{"log":{"level":"error"},
	 "inbounds":[{"type":"mixed","tag":"in","listen":"127.0.0.1","listen_port":%d}],
	 "outbounds":[{"type":"direct","tag":"直连"},{"type":"selector","tag":"其他","outbounds":["直连"],"default":"直连"}],
	 "route":{"final":"其他"},
	 "experimental":{"clash_api":{"external_controller":"127.0.0.1:%d"}}}`, inPort, apiPort)
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "-c", cfgFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	waitUp := func(addr string) {
		for i := 0; i < 100; i++ {
			if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
				c.Close()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s 没起来", addr)
	}
	waitUp(fmt.Sprintf("127.0.0.1:%d", apiPort))
	waitUp(fmt.Sprintf("127.0.0.1:%d", inPort))

	s, ck, _ := newStore(t, at("2026-10-28", 12, 0))
	c := &Collector{Store: s, Now: ck.Now, Source: func(ctx context.Context) (singbox.KernelSnapshot, error) {
		return singbox.KernelConnections(ctx, cfgFile)
	}}
	if err := c.Tick(ctx); err != nil { // 建基线
		t.Fatal(err)
	}

	srv, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		conn, err := srv.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 65536)
		_, _ = conn.Read(buf)
		_, _ = conn.Write(make([]byte, 5000))
		time.Sleep(3 * time.Second)
	}()
	port := srv.Addr().(*net.TCPAddr).Port
	pc, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", inPort))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	fmt.Fprintf(pc, "CONNECT localhost:%d HTTP/1.1\r\nHost: localhost:%d\r\n\r\n", port, port)
	hdr := make([]byte, 64)
	if _, err := pc.Read(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.Write(make([]byte, 1234)); err != nil {
		t.Fatal(err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = pc.Read(make([]byte, 65536))
	time.Sleep(500 * time.Millisecond)

	ck.t = at("2026-10-28", 12, 1)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := s.DayView("2026-10-28", 0, nil, true)
	if r.Total.Up < 1234 || r.Total.Down < 5000 || r.Total.Conns != 1 {
		t.Fatalf("总量: %+v", r.Total)
	}
	if len(r.Nodes) != 1 || r.Nodes[0].Key != "直连" || len(r.Clients) != 1 || r.Clients[0].Key != "127.0.0.1" || len(r.Hosts) != 1 || r.Hosts[0].Key != "localhost" {
		t.Errorf("三个维度: nodes=%+v clients=%+v hosts=%+v", r.Nodes, r.Clients, r.Hosts)
	}
	if r.Direct.Tag != "直连" {
		t.Errorf("直连出站名: %+v", r.Direct)
	}
	if nd, _ := s.DayView("2026-10-28", 0, nil, false); nd.Total.Up != 0 || len(nd.Nodes) != 0 { // 走的是 direct 类型出站:不统计直连时扣光
		t.Errorf("扣直连: %+v", nd)
	}
}
