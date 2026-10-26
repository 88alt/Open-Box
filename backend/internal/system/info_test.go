package system

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadResolvers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "resolv.conf")
	body := "# c\nsearch lan\nnameserver 127.0.0.1\nnameserver 192.168.1.1\nnameserver ::1\nnameserver fe80::1%eth0\nnameserver 192.168.1.1\nnameserver 223.5.5.5 # x\nnameserver bad\nnameserver 0.0.0.0\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := ReadResolvers(p), []string{"192.168.1.1", "223.5.5.5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := ReadResolvers(filepath.Join(t.TempDir(), "none")); got != nil {
		t.Fatalf("读不到文件应为空: %v", got)
	}
}

func TestLocalSubnetsOnlyPrivate(t *testing.T) {
	for _, s := range LocalSubnets() {
		if s == "127.0.0.0/8" || s == "::1/128" {
			t.Fatalf("不该包含回环: %s", s)
		}
	}
	if !isTunName("tun0") || !isTunName("utun3") || !isTunName("singbox") || isTunName("br-lan") {
		t.Fatal("tun 接口名判断不对")
	}
}
