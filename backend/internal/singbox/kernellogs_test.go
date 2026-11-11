package singbox

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func wsFrame(op byte, fin bool, payload []byte) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	f := []byte{b0}
	switch n := len(payload); {
	case n < 126:
		f = append(f, byte(n))
	case n < 65536:
		f = append(f, 126, 0, 0)
		binary.BigEndian.PutUint16(f[2:], uint16(n))
	default:
		f = append(f, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(f[2:], uint64(n))
	}
	return append(f, payload...)
}

// fakeLogServer 模拟内核的 /logs:握手成功后把 frames 依次发出去,再等客户端断开。
type fakeLogServer struct {
	srv       *httptest.Server
	gotAuth   string
	gotLevel  string
	subscribe chan struct{}
}

func newFakeLogServer(t *testing.T, frames [][]byte, mutate func(w http.ResponseWriter, r *http.Request) bool) *fakeLogServer {
	t.Helper()
	f := &fakeLogServer{subscribe: make(chan struct{}, 1)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth, f.gotLevel = r.Header.Get("Authorization"), r.URL.Query().Get("level")
		if mutate != nil && mutate(w, r) {
			return
		}
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		_ = rw.Flush()
		f.subscribe <- struct{}{}
		for _, fr := range frames {
			_, _ = c.Write(fr)
		}
		_, _ = bufio.NewReader(c).ReadByte() // 等客户端断开
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLogServer) addr() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func TestLogTapReadsFramesInOrder(t *testing.T) {
	big := strings.Repeat("x", 300) // 走 16 位长度
	j := func(level, p string) []byte { return []byte(`{"type":"` + level + `","payload":"` + p + `"}`) }
	frames := [][]byte{
		wsFrame(1, true, j("debug", "[1 0ms] dns: exchange a.test. IN A")),
		wsFrame(9, true, nil), // ping:忽略
		wsFrame(1, true, j("info", big)),
		wsFrame(1, false, j("error", "[2 1ms] dn")[:20]), // 分片:前半
		wsFrame(0, true, j("error", "[2 1ms] dn")[20:]),  // 分片:后半
		wsFrame(1, true, []byte("not json")),             // 坏帧跳过
	}
	f := newFakeLogServer(t, frames, nil)
	tap, err := openLogTap(context.Background(), f.addr(), "s3cret", "debug")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.subscribe:
	case <-time.After(2 * time.Second):
		t.Fatal("没握手")
	}
	time.Sleep(200 * time.Millisecond)
	got := tap.Stop()
	if len(got) != 3 || got[0] != (LogEntry{"debug", "[1 0ms] dns: exchange a.test. IN A"}) || got[1].Level != "info" || got[1].Payload != big || got[2].Level != "error" || got[2].Payload != "[2 1ms] dn" {
		t.Fatalf("%+v", got)
	}
	if f.gotAuth != "Bearer s3cret" || f.gotLevel != "debug" {
		t.Fatalf("auth=%q level=%q", f.gotAuth, f.gotLevel)
	}
	if again := tap.Stop(); len(again) != 3 {
		t.Fatalf("Stop 可以重复调用:%d", len(again))
	}
}

func TestLogTapOpenErrors(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, r *http.Request) bool{
		"鉴权失败": func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(401); return true },
		"没有订阅": func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(204); return true },
		"不升级":  func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(200); return true },
		"握手应答不对": func(w http.ResponseWriter, r *http.Request) bool {
			c, rw, _ := w.(http.Hijacker).Hijack()
			defer c.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: bogus\r\n\r\n")
			_ = rw.Flush()
			return true
		},
	}
	for name, m := range cases {
		f := newFakeLogServer(t, nil, m)
		if tap, err := openLogTap(context.Background(), f.addr(), "", "debug"); err == nil {
			tap.Stop()
			t.Errorf("%s:要报错", name)
		}
	}
	// 连不上
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	if _, err := openLogTap(context.Background(), addr, "", "debug"); err == nil || !strings.Contains(err.Error(), "连不上") {
		t.Errorf("连不上:%v", err)
	}
	// 配置里没开控制接口
	if _, err := OpenKernelLogs(context.Background(), "/nonexistent/config.json"); err == nil {
		t.Error("读不到配置要报错")
	}
}

func TestLogTapStopUnblocksWhenServerSilent(t *testing.T) {
	f := newFakeLogServer(t, nil, nil)
	tap, err := openLogTap(context.Background(), f.addr(), "", "debug")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { tap.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("服务端没发东西时 Stop 也要能返回")
	}
}
