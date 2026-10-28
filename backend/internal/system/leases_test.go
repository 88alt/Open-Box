package system

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var leaseNow = time.Unix(1_800_000_000, 0)

func TestParseDHCPLeases(t *testing.T) {
	in := strings.Join([]string{
		"1800003600 AA:BB:CC:DD:EE:01 192.168.1.20 phone 01:aa:bb:cc:dd:ee:01", // 正常,MAC 转小写
		"0 aa:bb:cc:dd:ee:02 192.168.1.5 * *",                                  // 永不过期,没有主机名
		"1799999999 aa:bb:cc:dd:ee:03 192.168.1.30 old *",                      // 已过期
		"1800000000 aa:bb:cc:dd:ee:04 192.168.1.31 edge *",                     // 刚好到期,按过期算
		"duid 00:01:00:01:aa:bb",                                               // duid 行
		"1800003600 12345 fd00::1234 v6host 00:01:00:01",                       // IPv6 租约
		"1800003600 aa:bb:cc:dd:ee:05 fd00::5 v6mac *",                         // MAC 对但地址是 IPv6
		"1800003600 not-a-mac 192.168.1.40 bad *",                              // MAC 坏
		"abc aa:bb:cc:dd:ee:06 192.168.1.41 bad *",                             // 到期时间坏
		"1800003600 aa:bb:cc:dd:ee:07 999.1.1.1 bad *",                         // IP 坏
		"1800003600 aa:bb:cc:dd:ee:08 192.168.1.42",                            // 列数不够
		"",
		"   ",
	}, "\n")
	got, exp := ParseDHCPLeases(strings.NewReader(in), leaseNow)
	want := []Client{
		{IP: "192.168.1.20", Name: "phone", MAC: "aa:bb:cc:dd:ee:01"},
		{IP: "192.168.1.5", Name: "", MAC: "aa:bb:cc:dd:ee:02"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if !reflect.DeepEqual(exp, []int64{1800003600, 0}) {
		t.Errorf("expiry = %v", exp)
	}
}

func TestParseDHCPLeasesRespectsSizeLimit(t *testing.T) {
	// 超过上限的部分不读:第一条在上限内,上限之后的行被忽略。
	line := "0 aa:bb:cc:dd:ee:01 192.168.1.1 h *\n"
	pad := strings.Repeat("# pad\n", maxLeaseFile/6+10)
	got, _ := ParseDHCPLeases(strings.NewReader(line+pad+"0 aa:bb:cc:dd:ee:02 192.168.1.2 h2 *\n"), leaseNow)
	if len(got) != 1 || got[0].IP != "192.168.1.1" {
		t.Fatalf("got %+v", got)
	}
}

func TestReadDHCPLeasesMergesSortsAndDedupes(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.leases")
	b := filepath.Join(dir, "b.leases")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(a, "1800000100 aa:bb:cc:dd:ee:01 192.168.1.100 first *\n"+
		"1800000100 aa:bb:cc:dd:ee:02 192.168.1.9 nine *\n"+
		"0 aa:bb:cc:dd:ee:03 192.168.1.50 forever *\n")
	write(b, "1800009999 aa:bb:cc:dd:ee:04 192.168.1.100 second *\n"+ // 同 IP,到期更晚,取这条
		"1800009999 aa:bb:cc:dd:ee:05 192.168.1.50 shortlived *\n") // 同 IP,永不过期的那条更晚,不被替换
	got := ReadDHCPLeases([]string{a, filepath.Join(dir, "missing"), b}, leaseNow)
	want := []Client{
		{IP: "192.168.1.9", Name: "nine", MAC: "aa:bb:cc:dd:ee:02"},
		{IP: "192.168.1.50", Name: "forever", MAC: "aa:bb:cc:dd:ee:03"},
		{IP: "192.168.1.100", Name: "second", MAC: "aa:bb:cc:dd:ee:04"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestReadDHCPLeasesNothingReadableIsEmptySlice(t *testing.T) {
	got := ReadDHCPLeases([]string{filepath.Join(t.TempDir(), "nope")}, leaseNow)
	if got == nil || len(got) != 0 {
		t.Fatalf("应为非 nil 的空切片,got %#v", got)
	}
}
