package probe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestValidDNSName(t *testing.T) {
	for s, want := range map[string]bool{
		"www.gstatic.com": true, "connectivitycheck.platform.hicloud.com": true, "a-b.c1": true,
		"": false, "1.2.3.4": false, "::1": false, "a..b": false, "-a.com": false, "a-.com": false,
		"a_b.com": false, "例子.com": false, strings.Repeat("a", 64) + ".com": false,
		strings.Repeat("a.", 130) + "com": false, "a b.com": false, "a.com/": false,
	} {
		if got := validDNSName(s); got != want {
			t.Errorf("validDNSName(%q) = %v,想要 %v", s, got, want)
		}
	}
}

func TestDNSQueryAndReply(t *testing.T) {
	q := buildDNSQuery(0xabcd, "www.baidu.com")
	if binary.BigEndian.Uint16(q[0:]) != 0xabcd || binary.BigEndian.Uint16(q[4:]) != 1 {
		t.Fatalf("头不对: % x", q[:12])
	}
	want := "\x03www\x05baidu\x03com\x00\x00\x01\x00\x01"
	if string(q[12:]) != want {
		t.Fatalf("问题段不对: %q", q[12:])
	}

	reply := func(id uint16, flags uint16, an uint16) []byte {
		b := make([]byte, 12)
		binary.BigEndian.PutUint16(b[0:], id)
		binary.BigEndian.PutUint16(b[2:], flags)
		binary.BigEndian.PutUint16(b[6:], an)
		return b
	}
	cases := []struct {
		name    string
		b       []byte
		rcode   int
		answers int
		wantErr bool
	}{
		{"正常", reply(7, 0x8180, 2), 0, 2, false},
		{"NXDOMAIN", reply(7, 0x8183, 0), 3, 0, false},
		{"ID 对不上", reply(8, 0x8180, 1), 0, 0, true},
		{"不是回复", reply(7, 0x0100, 0), 0, 0, true},
		{"太短", []byte{1, 2, 3}, 0, 0, true},
	}
	for _, c := range cases {
		rc, an, err := parseDNSReply(7, c.b)
		if (err != nil) != c.wantErr || rc != c.rcode || an != c.answers {
			t.Errorf("%s: rcode=%d an=%d err=%v", c.name, rc, an, err)
		}
	}
	if !strings.Contains(rcodeText(3), "NXDOMAIN") || !strings.Contains(rcodeText(9), "9") {
		t.Errorf("返回码文字不对: %q %q", rcodeText(3), rcodeText(9))
	}
}

func TestBuildDNSConfig(t *testing.T) {
	mat := Subset(testCfg(), []string{"组"})
	raw, err := buildDNSConfig(DNSJob{
		Server:   map[string]any{"type": "tcp", "server": "1.1.1.1", "tag": "别的", "detour": "旧的"},
		Detour:   "组",
		Material: mat,
		Name:     "www.gstatic.com",
	}, 18053)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	dns := cfg["dns"].(map[string]any)
	servers := dns["servers"].([]any)
	first := servers[0].(map[string]any)
	if first["tag"] != dnsTestTag || first["detour"] != "组" || first["type"] != "tcp" || first["server"] != "1.1.1.1" {
		t.Errorf("被测上游不对: %v", first)
	}
	if dns["final"] != dnsTestTag {
		t.Errorf("final = %v", dns["final"])
	}
	in := cfg["inbounds"].([]any)[0].(map[string]any)
	if in["type"] != "direct" || in["listen"] != "127.0.0.1" || in["listen_port"] != float64(18053) {
		t.Errorf("入站不对: %v", in)
	}
	var tags []string
	for _, o := range cfg["outbounds"].([]any) {
		tags = append(tags, o.(map[string]any)["tag"].(string))
	}
	for _, w := range []string{dnsDirectTag, "组", "直连", "拒绝"} {
		if !strings.Contains(strings.Join(tags, ","), w) {
			t.Errorf("出站里少了 %s: %v", w, tags)
		}
	}

	// 没有 detour 时不写 detour;传进来的旧 detour 也要去掉
	raw, _ = buildDNSConfig(DNSJob{Server: map[string]any{"type": "udp", "server": "223.5.5.5", "detour": "旧的"}, Name: "a.com"}, 1)
	if strings.Contains(string(raw), "detour") {
		t.Errorf("不该有 detour: %s", raw)
	}
}

// ---- 集成:本机假 DNS 服务器 + 真实 sing-box ----

// fakeDNS 的应答方式。
type fakeMode int

const (
	fakeAnswer fakeMode = iota
	fakeNXDomain
	fakeSilent
)

func fakeReply(q []byte, mode fakeMode) []byte {
	r := append([]byte{}, q...)
	switch mode {
	case fakeNXDomain:
		binary.BigEndian.PutUint16(r[2:], 0x8183)
	default:
		binary.BigEndian.PutUint16(r[2:], 0x8180)
		binary.BigEndian.PutUint16(r[6:], 1)
		r = append(r, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4)
	}
	return r
}

func startFakeUDP(t *testing.T, mode fakeMode) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if mode != fakeSilent && n >= 12 {
				_, _ = pc.WriteTo(fakeReply(buf[:n], mode), from)
			}
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func startFakeTCP(t *testing.T, mode fakeMode) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					var lb [2]byte
					if _, err := readFull(c, lb[:]); err != nil {
						return
					}
					q := make([]byte, binary.BigEndian.Uint16(lb[:]))
					if _, err := readFull(c, q); err != nil {
						return
					}
					r := fakeReply(q, mode)
					out := append([]byte{byte(len(r) >> 8), byte(len(r))}, r...)
					_, _ = c.Write(out)
				}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestQueryDNS_RealSingBox(t *testing.T) {
	bin := needSingBox(t)
	r := &Runner{Bin: bin, Dir: t.TempDir()}
	srv := func(typ string, port int) map[string]any {
		return map[string]any{"type": typ, "server": "127.0.0.1", "server_port": port}
	}
	ctx := context.Background()

	t.Run("udp 有答案", func(t *testing.T) {
		res, err := r.QueryDNS(ctx, DNSJob{Server: srv("udp", startFakeUDP(t, fakeAnswer)), Name: "www.baidu.com", Timeout: 5 * time.Second})
		if err != nil || !res.OK || res.Ms < 1 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("tcp 有答案", func(t *testing.T) {
		res, err := r.QueryDNS(ctx, DNSJob{Server: srv("tcp", startFakeTCP(t, fakeAnswer)), Name: "www.baidu.com", Timeout: 5 * time.Second})
		if err != nil || !res.OK {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("上游回 NXDOMAIN", func(t *testing.T) {
		res, err := r.QueryDNS(ctx, DNSJob{Server: srv("udp", startFakeUDP(t, fakeNXDomain)), Name: "www.baidu.com", Timeout: 5 * time.Second})
		if err != nil || res.OK || res.Reason != "failed" || !strings.Contains(res.Error, "NXDOMAIN") {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("上游不作声算超时", func(t *testing.T) {
		start := time.Now()
		res, err := r.QueryDNS(ctx, DNSJob{Server: srv("udp", startFakeUDP(t, fakeSilent)), Name: "www.baidu.com", Timeout: 2 * time.Second})
		if err != nil || res.OK || res.Reason != "timeout" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if d := time.Since(start); d > 6*time.Second {
			t.Errorf("等了 %v,超时没生效", d)
		}
	})
	t.Run("detour 指向不存在的出站,内核起不来,如实报 invalid", func(t *testing.T) {
		res, err := r.QueryDNS(ctx, DNSJob{Server: srv("udp", 53), Detour: "不存在", Name: "www.baidu.com", Timeout: 3 * time.Second})
		if err != nil || res.OK || res.Reason != "invalid" || res.Error == "" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("经出站组去连(detour 到 selector)", func(t *testing.T) {
		mat := Subset(testCfg(), []string{"组"}) // 组默认选「直连」
		res, err := r.QueryDNS(ctx, DNSJob{Server: srv("udp", startFakeUDP(t, fakeAnswer)), Detour: "组", Material: mat, Name: "www.baidu.com", Timeout: 5 * time.Second})
		if err != nil || !res.OK {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
}

func TestQueryDNS_Inputs(t *testing.T) {
	r := &Runner{Bin: "sing-box-不存在", Dir: t.TempDir()}
	ctx := context.Background()
	if res, _ := r.QueryDNS(ctx, DNSJob{Server: map[string]any{"type": "udp"}, Name: "1.2.3.4"}); res.Reason != "invalid" {
		t.Errorf("IP 当域名应被拒: %+v", res)
	}
	if res, _ := r.QueryDNS(ctx, DNSJob{Name: "a.com"}); res.Reason != "invalid" {
		t.Errorf("没有上游应被拒: %+v", res)
	}
	if _, err := r.QueryDNS(ctx, DNSJob{Server: map[string]any{"type": "udp", "server": "1.1.1.1"}, Name: "a.com"}); err != ErrNoSingBox {
		t.Errorf("找不到 sing-box 应返回 ErrNoSingBox: %v", err)
	}
}
