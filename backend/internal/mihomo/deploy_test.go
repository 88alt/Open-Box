package mihomo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWriteArtifacts(t *testing.T) {
	fs := newMemFS()
	res := &BuildResult{Config: NewOrdered().Set("mode", "rule").Set("mixed-port", 7891), Entry: "add rule"}
	paths := ArtifactPaths{
		Config: "/d/etc/mihomo.yaml", Inputs: "/d/etc/mihomo.inputs",
		EntryBypass: "/d/etc/mihomo-entry-bypass.nft", RouterKernel: "/d/etc/router-kernel",
		DNSMasq: "/d/etc/mihomo-dnsmasq",
	}
	at := time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC)
	err := WriteArtifacts(fs, paths, res, ArtifactMeta{
		Failover: json.RawMessage(`[{"group":"PROXY"}]`), DNSMasq: true, GeneratedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(fs.files["/d/etc/mihomo.yaml"]); got != "mode: rule\nmixed-port: 7891\n" {
		t.Errorf("mihomo.yaml = %q", got)
	}
	if got := string(fs.files["/d/etc/mihomo-entry-bypass.nft"]); got != "add rule\n" {
		t.Errorf("entry nft = %q", got)
	}
	if got := string(fs.files["/d/etc/router-kernel"]); got != "mihomo\n" {
		t.Errorf("router-kernel = %q", got)
	}
	if got := string(fs.files["/d/etc/mihomo-dnsmasq"]); got != "dnsmasq\n" {
		t.Errorf("mihomo-dnsmasq = %q", got)
	}
	var snap map[string]any
	if err := json.Unmarshal(fs.files["/d/etc/mihomo.inputs"], &snap); err != nil {
		t.Fatal(err)
	}
	if snap["kernel"] != "mihomo" || snap["generatedAt"] != "2026-11-25T01:02:03.000Z" {
		t.Errorf("inputs = %v", snap)
	}
	if _, ok := snap["monitoring"]; !ok {
		t.Error("monitoring 缺省也要写空数组")
	}
	filter, _ := snap["dnsFilter"].(map[string]any)
	if filter["enabled"] != false || filter["key"] != "" {
		t.Errorf("dnsFilter = %v", filter)
	}

	// 入口 nft 空串时要把旧文件删掉(上游同样 remove)。
	res2 := &BuildResult{Config: NewOrdered().Set("mode", "rule")}
	if err := WriteArtifacts(fs, paths, res2, ArtifactMeta{DNSMasq: false}); err != nil {
		t.Fatal(err)
	}
	if _, has := fs.files["/d/etc/mihomo-entry-bypass.nft"]; has {
		t.Error("没有入口 nft 时该删掉旧文件")
	}
	if _, has := fs.files["/d/etc/mihomo-dnsmasq"]; has {
		t.Error("不是 dnsmasq 模式时该删掉标记文件")
	}
}

func TestCheckArgs(t *testing.T) {
	got := strings.Join(CheckArgs("/d/etc/mihomo.yaml", "/d/mihomo"), " ")
	if got != "-t -d /d/mihomo -f /d/etc/mihomo.yaml" {
		t.Fatalf("参数 = %s", got)
	}
	got = strings.Join(CheckArgs("/c.yaml", ""), " ")
	if got != "-t -f /c.yaml" {
		t.Fatalf("没给工作目录时的参数 = %s", got)
	}
}
