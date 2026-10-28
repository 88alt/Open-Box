package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const netHead = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func writeNet(t *testing.T, dir, name string, rows ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(netHead+strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadListeners(t *testing.T) {
	d := t.TempDir()
	writeNet(t, d, "tcp",
		"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0", // 8080 LISTEN
		"   1: 0100007F:0050 0100007F:C350 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0", // 80 已建立,不算
		"   2: 0100007F:1BB5 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0000000000000000 100 0 0 10 0", // 7093 只听回环,也算占用
	)
	writeNet(t, d, "tcp6",
		"   0: 00000000000000000000000000000000:20FB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 4 1 0000000000000000 100 0 0 10 0", // 8443
	)
	writeNet(t, d, "udp",
		"   0: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 5 2 0000000000000000 0", // 53 绑定着
		"   1: 0100A8C0:9C40 0101A8C0:0035 01 00000000:00000000 00:00000000 00000000     0        0 6 2 0000000000000000 0", // 客户端已连接,不算
	)
	// udp6 不存在:关了 IPv6,不是错误
	l, err := ReadListeners(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{8080, 7093, 8443} {
		if !l.TCP[p] {
			t.Errorf("TCP %d 应算占用", p)
		}
	}
	if l.TCP[80] || l.TCP[22] {
		t.Errorf("已建立的连接 / 没出现的端口不该算: %v", l.TCP)
	}
	if !l.UDP[53] || l.UDP[0x9C40] || len(l.UDP) != 1 {
		t.Errorf("UDP: %v", l.UDP)
	}
}

func TestReadListenersErrors(t *testing.T) {
	d := t.TempDir()
	if _, err := ReadListeners(d); err == nil {
		t.Fatal("tcp 读不了应报错,不能猜")
	}
	writeNet(t, d, "tcp")
	if _, err := ReadListeners(d); err == nil {
		t.Fatal("udp 读不了应报错")
	}
	writeNet(t, d, "udp")
	if l, err := ReadListeners(d); err != nil || len(l.TCP)+len(l.UDP) != 0 {
		t.Fatalf("空表 = 没有占用: %+v %v", l, err)
	}
	// tcp6 存在但读不了(是目录)→ 报错;只有「不存在」才算关了 IPv6
	if err := os.Mkdir(filepath.Join(d, "tcp6"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadListeners(d); err == nil {
		t.Fatal("tcp6 存在却读不了应报错")
	}
	// 坏行被跳过
	d2 := t.TempDir()
	writeNet(t, d2, "tcp", "垃圾", "   0: 00000000 00000000:0000 0A", "   1: 00000000:ZZZZ 00000000:0000 0A x", "   2: 00000000:0000 00000000:0000 0A x")
	writeNet(t, d2, "udp")
	if l, err := ReadListeners(d2); err != nil || len(l.TCP) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
}
