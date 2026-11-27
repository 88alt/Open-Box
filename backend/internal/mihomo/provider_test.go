package mihomo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// 期望值是用上游同款的 yaml 库(npm yaml 2.x,stringify 加 lineWidth:0)逐个生成的。
func TestYAMLScalarMatchesYamlLib(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a.com", "a.com"},
		{"+.a.com", "+.a.com"},
		{"DOMAIN,foo.com", "DOMAIN,foo.com"},
		{"DOMAIN-REGEX,^.+\\.example\\.com$", "DOMAIN-REGEX,^.+\\.example\\.com$"},
		{"DOMAIN-REGEX,(^[^.]+$|\\.(lan|local|home|internal|home\\.arpa)$)", "DOMAIN-REGEX,(^[^.]+$|\\.(lan|local|home|internal|home\\.arpa)$)"},
		{"123", "\"123\""},
		{"1.5", "\"1.5\""},
		{"1.", "\"1.\""},
		{"+1", "\"+1\""},
		{"0x1F", "\"0x1F\""},
		{"1e5", "\"1e5\""},
		{"true", "\"true\""},
		{"Null", "\"Null\""},
		{"~", "\"~\""},
		{"1.2.3.0/24", "1.2.3.0/24"},
		{"::/0", "::/0"},
		{"2001:db8::/32", "2001:db8::/32"},
		{"fe80::/10", "fe80::/10"},
		{"'x", "\"'x\""},
		{"\"x", "'\"x'"},
		{"x\"y", "x\"y"},
		{"x\"y'z", "x\"y'z"},
		{"#a", "\"#a\""},
		{"a #b", "\"a #b\""},
		{"a: b", "\"a: b\""},
		{"a:b", "a:b"},
		{"a:", "\"a:\""},
		{"- a", "\"- a\""},
		{"-", "\"-\""},
		{"?a", "?a"},
		{"&a", "\"&a\""},
		{"*a", "\"*a\""},
		{"%a", "\"%a\""},
		{"`a", "\"`a\""},
		{"{a}", "\"{a}\""},
		{"a,b", "a,b"},
		{"中文.com", "中文.com"},
		{"DOMAIN-REGEX,a: b", "\"DOMAIN-REGEX,a: b\""},
		{"\\", "\\"},
		{"+.123", "\"+.123\""},
		{"12:30", "12:30"},
		{"2001-01-01", "2001-01-01"},
	}
	for _, c := range cases {
		if got := yamlScalar(c.in); got != c.want {
			t.Errorf("yamlScalar(%q) = %s,想要 %s", c.in, got, c.want)
		}
	}
}

func mustParse(t *testing.T, s string) Source {
	t.Helper()
	src, err := ParseSource([]byte(s))
	if err != nil {
		t.Fatalf("ParseSource: %v", err)
	}
	return src
}

func TestBuildProviderDomainPlain(t *testing.T) {
	// 全是「纯域名 / 后缀」→ behavior domain,后缀写成 +.,条目顺序按源文件(字段顺序 + 值顺序),重复去掉
	src := mustParse(t, `{"version":3,"rules":[{"domain":["a.com","b.com"],"domain_suffix":["c.com","a.com"]},{"domain_suffix":["c.com","d-e.org"]}]}`)
	b, p, err := BuildProvider(src, "domain")
	if err != nil {
		t.Fatal(err)
	}
	want := "payload:\n  - a.com\n  - b.com\n  - +.c.com\n  - +.a.com\n  - +.d-e.org\n"
	if b != "domain" || p != want {
		t.Errorf("behavior=%s payload=%q", b, p)
	}
}

func TestBuildProviderClassical(t *testing.T) {
	// 带关键字 / 正则 / 非纯域名 → classical,条目写完整规则
	src := mustParse(t, `{"version":2,"rules":[{"domain":"x.com","domain_keyword":["ads"],"domain_regex":["^a,b$"]},{"domain_suffix":["中文.com"]}]}`)
	b, p, err := BuildProvider(src, "domain")
	if err != nil {
		t.Fatal(err)
	}
	want := "payload:\n  - DOMAIN,x.com\n  - DOMAIN-KEYWORD,ads\n  - DOMAIN-REGEX,^a,b$\n  - DOMAIN-SUFFIX,中文.com\n"
	if b != "classical" || p != want {
		t.Errorf("behavior=%s payload=%q", b, p)
	}
}

func TestBuildProviderIPCIDR(t *testing.T) {
	src := mustParse(t, `{"version":3,"rules":[{"ip_cidr":["1.2.3.0/24","::/0","1.2.3.0/24"]},{"ip_cidr":"10.0.0.0/8"}]}`)
	b, p, err := BuildProvider(src, "ipcidr")
	if err != nil {
		t.Fatal(err)
	}
	if b != "ipcidr" || p != "payload:\n  - 1.2.3.0/24\n  - ::/0\n  - 10.0.0.0/8\n" {
		t.Errorf("behavior=%s payload=%q", b, p)
	}
}

func TestBuildProviderErrors(t *testing.T) {
	cases := []struct {
		name, json, kind, want string
	}{
		{"版本不对", `{"version":5,"rules":[{"domain":"a.com"}]}`, "domain", "无效的规则集源文件"},
		{"没版本", `{"rules":[{"domain":"a.com"}]}`, "domain", "无效的规则集源文件"},
		{"版本是字符串", `{"version":"3","rules":[{"domain":"a.com"}]}`, "domain", "无效的规则集源文件"},
		{"rules 不是数组", `{"version":3,"rules":{"domain":"a.com"}}`, "domain", "无效的规则集源文件"},
		{"rules 是 null", `{"version":3,"rules":null}`, "domain", "无效的规则集源文件"},
		{"不是对象", `[1,2]`, "domain", "无效的规则集源文件"},
		{"类别不对", `{"version":3,"rules":[{"domain":"a.com"}]}`, "other", "无效的规则集源文件"},
		{"空对象规则", `{"version":3,"rules":[{}]}`, "domain", "无法转换空规则"},
		{"数组规则", `{"version":3,"rules":[["a"]]}`, "domain", "无法转换空规则"},
		{"null 规则", `{"version":3,"rules":[null]}`, "domain", "无法转换空规则"},
		{"域名集里有别的字段", `{"version":3,"rules":[{"domain":"a.com","port":[80]}]}`, "domain", "无法转换规则字段 port"},
		{"网段集里有域名", `{"version":3,"rules":[{"domain":"a.com"}]}`, "ipcidr", "无法转换规则字段 domain"},
		{"域名集里有 ip_cidr", `{"version":3,"rules":[{"ip_cidr":"1.1.1.1/32"}]}`, "domain", "无法转换规则字段 ip_cidr"},
		{"空串", `{"version":3,"rules":[{"domain":[""]}]}`, "domain", "无效的规则值 domain"},
		{"首尾空白", `{"version":3,"rules":[{"domain_suffix":[" a.com"]}]}`, "domain", "无效的规则值 domain_suffix"},
		{"全角空格结尾", `{"version":3,"rules":[{"domain":["a.com\u3000"]}]}`, "domain", "无效的规则值 domain"},
		{"控制字符", `{"version":3,"rules":[{"domain":["a\u0001.com"]}]}`, "domain", "无效的规则值 domain"},
		{"值不是字符串", `{"version":3,"rules":[{"domain":[1]}]}`, "domain", "无效的规则值 domain"},
		{"域名里带逗号", `{"version":3,"rules":[{"domain":["a,b.com"]}]}`, "domain", "无效的规则值 domain"},
		{"网段没前缀", `{"version":3,"rules":[{"ip_cidr":["1.2.3.4"]}]}`, "ipcidr", "无效的 IP 网段"},
		{"网段前缀过大", `{"version":3,"rules":[{"ip_cidr":["1.2.3.4/33"]}]}`, "ipcidr", "无效的 IP 网段"},
		{"v6 前缀过大", `{"version":3,"rules":[{"ip_cidr":["::/129"]}]}`, "ipcidr", "无效的 IP 网段"},
		{"网段带区域", `{"version":3,"rules":[{"ip_cidr":["fe80::1%eth0/64"]}]}`, "ipcidr", "无效的 IP 网段"},
		{"网段多一段", `{"version":3,"rules":[{"ip_cidr":["1.2.3.4/24/1"]}]}`, "ipcidr", "无效的 IP 网段"},
		{"网段前缀不是数字", `{"version":3,"rules":[{"ip_cidr":["1.2.3.4/a"]}]}`, "ipcidr", "无效的 IP 网段"},
		{"不是地址", `{"version":3,"rules":[{"ip_cidr":["example.com/24"]}]}`, "ipcidr", "无效的 IP 网段"},
		{"没有条目", `{"version":3,"rules":[]}`, "domain", "规则集没有可用条目"},
		{"只有空数组字段", `{"version":3,"rules":[{"domain":[]}]}`, "domain", "规则集没有可用条目"},
	}
	for _, c := range cases {
		src := mustParse(t, c.json)
		_, _, err := BuildProvider(src, c.kind)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err=%v,想要 %q", c.name, err, c.want)
		}
	}
	if _, err := ParseSource([]byte("{oops")); err == nil {
		t.Errorf("坏 JSON 应该报错")
	}
}

func TestBuildProviderRegexLikeLocalDNS(t *testing.T) {
	// 上游 ob-local-dns:单条 domain_regex,behavior classical,载荷里的正则按 yaml 规则原样(纯文本)
	re := `(^[^.]+$|\.(lan|local|home|internal|home\.arpa)$)`
	b, p, err := BuildProvider(NewSource(3, Rule{Strs("domain_regex", re)}), "domain")
	if err != nil {
		t.Fatal(err)
	}
	if b != "classical" || p != "payload:\n  - DOMAIN-REGEX,"+re+"\n" {
		t.Errorf("behavior=%s payload=%q", b, p)
	}
}

func TestDuplicateKeysKeepFirstPositionLastValue(t *testing.T) {
	src := mustParse(t, `{"version":3,"rules":[{"domain":"a.com","domain_suffix":"b.com","domain":"c.com"}]}`)
	_, p, err := BuildProvider(src, "domain")
	if err != nil {
		t.Fatal(err)
	}
	if p != "payload:\n  - c.com\n  - +.b.com\n" {
		t.Errorf("payload=%q", p)
	}
}

func TestProviderFileName(t *testing.T) {
	sum := sha256.Sum256([]byte("domain\x00payload:\n  - a.com\n"))
	want := "geosite-x-" + hex.EncodeToString(sum[:]) + ".yaml"
	if got := ProviderFile("geosite-x", "domain", "payload:\n  - a.com\n"); got != want {
		t.Errorf("文件名 %s,想要 %s", got, want)
	}
}

type memFS struct {
	files  map[string][]byte
	modes  map[string]os.FileMode
	writes int
	dirs   []string
}

func newMemFS() *memFS { return &memFS{files: map[string][]byte{}, modes: map[string]os.FileMode{}} }

func (m *memFS) Exists(p string) bool { _, ok := m.files[p]; return ok }
func (m *memFS) WriteFile(p string, d []byte, mode os.FileMode) error {
	m.files[p] = d
	m.modes[p] = mode
	m.writes++
	return nil
}
func (m *memFS) ReadFile(p string) ([]byte, error) {
	d, ok := m.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return d, nil
}
func (m *memFS) MkdirAll(p string) error { m.dirs = append(m.dirs, p); return nil }
func (m *memFS) Remove(p string) error {
	if _, ok := m.files[p]; !ok {
		return os.ErrNotExist
	}
	delete(m.files, p)
	return nil
}

func TestWriteProviderIdempotent(t *testing.T) {
	fs := newMemFS()
	src := NewSource(3, Rule{Strs("domain_suffix", "a.com")})
	p1, err := WriteProvider(fs, "/d/mihomo/rules", "ob-x", src, "domain")
	if err != nil {
		t.Fatal(err)
	}
	if p1.Type != "file" || p1.Behavior != "domain" || p1.Format != "yaml" || !strings.HasPrefix(p1.Path, "./rules/ob-x-") || !strings.HasSuffix(p1.Path, ".yaml") {
		t.Fatalf("provider=%+v", p1)
	}
	full := "/d/mihomo/rules/" + strings.TrimPrefix(p1.Path, "./rules/")
	if string(fs.files[full]) != "payload:\n  - +.a.com\n" || fs.modes[full] != 0o600 {
		t.Errorf("落盘内容 %q 权限 %v", fs.files[full], fs.modes[full])
	}
	p2, err := WriteProvider(fs, "/d/mihomo/rules", "ob-x", src, "domain")
	if err != nil || p2 != p1 || fs.writes != 1 {
		t.Errorf("同内容应复用文件: %+v %v 写了 %d 次", p2, err, fs.writes)
	}
	// 内容不同 → 文件名不同
	p3, _ := WriteProvider(fs, "/d/mihomo/rules", "ob-x", NewSource(3, Rule{Strs("domain_suffix", "b.com")}), "domain")
	if p3.Path == p1.Path || fs.writes != 2 {
		t.Errorf("内容变了文件名应变: %s", p3.Path)
	}
	if _, err := WriteProvider(fs, "/d", "ob-x", NewSource(3), "domain"); err == nil {
		t.Errorf("没有条目应报错")
	}
}

func TestOSFSWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	var fs OSFS
	p := dir + "/a.yaml"
	if err := fs.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists(p) {
		t.Fatal("应存在")
	}
	b, _ := fs.ReadFile(p)
	st, _ := os.Stat(p)
	if string(b) != "x" || st.Mode().Perm() != 0o600 {
		t.Errorf("内容 %q 权限 %v", b, st.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("不应留下临时文件: %v", entries)
	}
	if err := fs.Remove(p); err != nil || fs.Exists(p) {
		t.Errorf("删除失败: %v", err)
	}
}
