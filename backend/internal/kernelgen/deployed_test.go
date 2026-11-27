package kernelgen

import (
	"os"
	"path/filepath"
	"testing"
)

// 上游 v0.1.493 起把内置规则集落成 .mrs(文件名带 64 位哈希)放 <DataDir>/mihomo/rules/,
// 官方安装不会同时留 .srs 源文件。这里把目录里已有的 provider 扫出来复用,
// 免得「官方装过、自研接管」时部署报「无法读取现有规则集文件」。
func TestScanDeployedProviders(t *testing.T) {
	dir := t.TempDir()
	const h = "e7cbf0abb6e8f0a328e5174c30c7db5c485bc67519aa8d32aa02f8143287ff62"
	files := []string{
		"geosite-cn-" + h + ".mrs",      // 官方 .mrs,带哈希
		"geoip-cn-" + h + ".mrs",        // 同上,ipcidr
		"geosite-google-" + h + ".txt",  // 文本格式
		"list-abc123-" + h + ".yaml",    // 用户规则集
		"list-abc123-ip-" + h + ".mrs",  // 用户 IP 规则集
		"ob-entry-direct-" + h + ".mrs", // 入口旁路
		"readme.md",                     // 不认识的扩展名,应跳过
		"random.mrs",                    // 没有 tag 前缀但能解析成 tag=random
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := scanDeployedProviders(dir)

	cases := map[string]struct{ format, behavior string }{
		"geosite-cn":      {"mrs", "domain"},
		"geoip-cn":        {"mrs", "ipcidr"},
		"geosite-google":  {"text", "domain"},
		"list-abc123":     {"yaml", "domain"},
		"list-abc123-ip":  {"mrs", "ipcidr"},
		"ob-entry-direct": {"mrs", "ipcidr"},
	}
	for tag, want := range cases {
		p, ok := got[tag]
		if !ok {
			t.Errorf("缺少 tag %q", tag)
			continue
		}
		if p.Format != want.format || p.Behavior != want.behavior || p.Type != "file" {
			t.Errorf("%s: %+v,想要 format=%s behavior=%s", tag, p, want.format, want.behavior)
		}
		if p.Path != "./rules/"+filepath.Base(p.Path) || p.Path == "./rules/" {
			t.Errorf("%s: path 不对 %q", tag, p.Path)
		}
	}
	// readme.md 不该出现
	for tag := range got {
		if tag == "readme" || tag == "readme.md" {
			t.Errorf("不该把 readme 当 provider: %v", got[tag])
		}
	}
}

// 目录不存在 / 空目录都返回 nil,不报错。
func TestScanDeployedProvidersEmpty(t *testing.T) {
	if got := scanDeployedProviders(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Fatalf("目录不存在应返回 nil,拿到 %v", got)
	}
	if got := scanDeployedProviders(t.TempDir()); got != nil {
		t.Fatalf("空目录应返回 nil,拿到 %v", got)
	}
}

// 文件名解析:带哈希 / 不带哈希 / 哈希长度不对。
func TestParseProviderFileName(t *testing.T) {
	const h = "e7cbf0abb6e8f0a328e5174c30c7db5c485bc67519aa8d32aa02f8143287ff62"
	cases := []struct {
		in, tag, format string
		ok              bool
	}{
		{"geosite-cn-" + h + ".mrs", "geosite-cn", "mrs", true},
		{"geosite-cn.mrs", "geosite-cn", "mrs", true},
		{"geosite-cn-deadbeef.mrs", "geosite-cn-deadbeef", "mrs", true}, // 不是 64 位十六进制,整段当 tag(保守,不误剥离)
		{"x.json", "", "", false},
		{".mrs", "", "", false},
	}
	for _, c := range cases {
		tag, format, ok := parseProviderFileName(c.in)
		if ok != c.ok || tag != c.tag || format != c.format {
			t.Errorf("%q → (%q,%q,%v),想要 (%q,%q,%v)", c.in, tag, format, ok, c.tag, c.format, c.ok)
		}
	}
}
