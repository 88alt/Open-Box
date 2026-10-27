package system

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadWANDNS(t *testing.T) {
	dir := t.TempDir()
	w := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	missing := filepath.Join(dir, "none")
	onlyLoop := w("loop", "nameserver 127.0.0.1\nnameserver ::1\n")
	empty := w("empty", "# 没有 nameserver\nsearch lan\n")
	auto := w("auto", "# Interface wan\nnameserver 202.96.128.86\nnameserver 202.96.134.133\nnameserver 202.96.128.86\n")
	other := w("other", "nameserver 8.8.8.8\n")

	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{"取第一份有结果的", []string{missing, onlyLoop, empty, auto, other}, []string{"202.96.128.86", "202.96.134.133"}},
		{"优先级靠前的赢,不合并", []string{other, auto}, []string{"8.8.8.8"}},
		{"只有回环的跳过", []string{onlyLoop, other}, []string{"8.8.8.8"}},
		{"全都读不出", []string{missing, onlyLoop, empty}, []string{}},
		{"没有候选", nil, []string{}},
	}
	for _, c := range cases {
		got := ReadWANDNS(c.files)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v want %#v", c.name, got, c.want)
		}
	}
}

func TestWANResolverFilesOrder(t *testing.T) {
	// 本机顶替的 /etc/resolv.conf 必须排在最后,否则会盖住真正的上游
	if n := len(WANResolverFiles); n == 0 || WANResolverFiles[n-1] != "/etc/resolv.conf" {
		t.Fatalf("候选文件顺序不对: %v", WANResolverFiles)
	}
}
