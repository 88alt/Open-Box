package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 一份最小的合法清单:两个文件。
func sampleJSON() string {
	return `{
	  "format": "open-box-release",
	  "version": "v0.1.482-main.1",
	  "upstream": 482,
	  "arch": "amd64",
	  "builtAt": "2026-11-25T10:00:00Z",
	  "components": {
	    "backend": {"version": "v0.1.482-main.1"},
	    "core": {"version": "20"},
	    "singbox": {"version": "1.14.4"},
	    "panel": {"version": "v0.1.482"},
	    "geodata": {"version": "2026-11-20", "date": "2026-11-20"}
	  },
	  "files": [
	    {"path": "bin/openbox-backend", "sha256": "` + strings.Repeat("a", 64) + `", "size": 1234, "mode": "0755"},
	    {"path": "share/panel/index.html", "sha256": "` + strings.Repeat("b", 64) + `", "size": 10, "mode": "0644"}
	  ]
	}`
}

func mustParse(t *testing.T, s string) *Manifest {
	t.Helper()
	m, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("Parse 失败:%v", err)
	}
	return m
}

func TestParseOK(t *testing.T) {
	m := mustParse(t, sampleJSON())
	if m.Format != Format || m.Version != "v0.1.482-main.1" || m.Upstream != 482 || m.Arch != "amd64" {
		t.Fatalf("解析结果不对:%+v", m)
	}
	if len(m.Files) != 2 {
		t.Fatalf("files 应有 2 项,得到 %d", len(m.Files))
	}
	if f, ok := m.File("share/panel/index.html"); !ok || f.Size != 10 || f.Mode != "0644" {
		t.Fatalf("File 取到的不是预期的那一项:%+v %v", f, ok)
	}
	if _, ok := m.File("nope"); ok {
		t.Fatal("File 不该找到不存在的路径")
	}
	if got := m.TotalSize(); got != 1244 {
		t.Fatalf("TotalSize 应是 1244,得到 %d", got)
	}
	if m.Components.Geodata.Date != "2026-11-20" {
		t.Fatalf("geodata.date 没解析出来:%+v", m.Components.Geodata)
	}
}

// 未知字段要报错:manifest 与解析它的后端是同一份代码产出的,多出来的字段只可能是拼错。
func TestParseRejectsUnknownField(t *testing.T) {
	bad := strings.Replace(sampleJSON(), `"arch": "amd64",`, `"arch": "amd64", "archh": "amd64",`, 1)
	if _, err := Parse(strings.NewReader(bad)); err == nil {
		t.Fatal("多出来的字段应该报错")
	}
}

func TestParseRejectsTrailingContent(t *testing.T) {
	if _, err := Parse(strings.NewReader(sampleJSON() + "\n{}\n")); err == nil {
		t.Fatal("两个对象拼在一起应该报错")
	}
}

func TestValidateFieldErrors(t *testing.T) {
	cases := []struct {
		name string
		edit func(m *Manifest)
		want string
	}{
		{"format", func(m *Manifest) { m.Format = "open-box" }, "format"},
		{"version 空", func(m *Manifest) { m.Version = "" }, "缺 version"},
		{"version 带斜杠", func(m *Manifest) { m.Version = "v0.1.482/1" }, "斜杠"},
		{"upstream 负数", func(m *Manifest) { m.Upstream = -1 }, "负数"},
		{"arch", func(m *Manifest) { m.Arch = "x64" }, "arch"},
		{"builtAt", func(m *Manifest) { m.BuiltAt = "2026-11-25 10:00" }, "RFC3339"},
		{"files 空", func(m *Manifest) { m.Files = nil }, "files 是空的"},
		{"列了 manifest 自己", func(m *Manifest) { m.Files[0].Path = ManifestName }, "不该列"},
		{"重复路径", func(m *Manifest) { m.Files[1].Path = m.Files[0].Path }, "出现了两次"},
		{"sha 大写", func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("A", 64) }, "sha256"},
		{"sha 太短", func(m *Manifest) { m.Files[0].SHA256 = "abc" }, "sha256"},
		{"size 负数", func(m *Manifest) { m.Files[0].Size = -1 }, "size"},
		{"mode 非八进制", func(m *Manifest) { m.Files[0].Mode = "0999" }, "mode"},
		{"mode 带 setuid", func(m *Manifest) { m.Files[0].Mode = "4755" }, "mode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := mustParse(t, sampleJSON())
			c.edit(m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("应该报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误里应含 %q,得到:%v", c.want, err)
			}
		})
	}
}

func TestCheckPath(t *testing.T) {
	good := []string{"bin/openbox-backend", "share/panel/index.html", "lib/sing-box", "init/openwrt/openbox-backend"}
	for _, p := range good {
		if err := CheckPath(p); err != nil {
			t.Fatalf("%q 应该合法:%v", p, err)
		}
	}
	bad := map[string]string{
		"":                         "路径为空",
		"/etc/passwd":              "相对路径",
		"../escape":                "跳出解包目录",
		"bin/../../escape":         "跳出解包目录",
		"bin//x":                   "不规范",
		"./bin/x":                  "不规范",
		"bin/":                     "不规范",
		"bin\\x":                   "反斜杠",
		"C:/x":                     "冒号",
		"bin/x\x00y":               "NUL",
		"share/./panel/x.js":       "不规范",
		"bin/..":                   "跳出解包目录",
		"share/panel/../../../etc": "跳出解包目录",
	}
	for p, want := range bad {
		err := CheckPath(p)
		if err == nil {
			t.Fatalf("%q 应该报错", p)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q 的错误应含 %q,得到:%v", p, want, err)
		}
	}
}

// buildTree 在 root 下按 map 造文件,返回每个文件的 sha256 与大小。
func buildTree(t *testing.T, root string, files map[string]string) []File {
	t.Helper()
	var out []File
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		out = append(out, File{Path: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)), Mode: "0644"})
	}
	return out
}

func TestVerifyOK(t *testing.T) {
	root := t.TempDir()
	files := buildTree(t, root, map[string]string{
		"bin/openbox-backend":     "backend",
		"share/panel/index.html":  "<html></html>",
		"share/panel/assets/a.js": "console.log(1)",
		"lib/sing-box":            "kernel",
	})
	m := &Manifest{Format: Format, Version: "v0.1.482-main.1", Arch: "amd64", Files: files}
	// manifest.json 自己也在目录里,Verify 要跳过它
	if err := os.WriteFile(filepath.Join(root, ManifestName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 空目录不影响
	if err := os.MkdirAll(filepath.Join(root, "share/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(root); err != nil {
		t.Fatalf("Verify 应该通过:%v", err)
	}
}

func TestVerifyFailures(t *testing.T) {
	setup := func(t *testing.T) (string, *Manifest) {
		root := t.TempDir()
		files := buildTree(t, root, map[string]string{"bin/a": "AAA", "share/b": "BBB"})
		return root, &Manifest{Format: Format, Version: "v1", Arch: "amd64", Files: files}
	}

	t.Run("内容被改", func(t *testing.T) {
		root, m := setup(t)
		if err := os.WriteFile(filepath.Join(root, "bin/a"), []byte("XXX"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := m.Verify(root); err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("内容被改应该报 sha256 不对,得到:%v", err)
		}
	})

	t.Run("大小不对", func(t *testing.T) {
		root, m := setup(t)
		m.Files[0].Size = 99
		if err := m.Verify(root); err == nil || !strings.Contains(err.Error(), "大小") {
			t.Fatalf("大小不对应该报出来,得到:%v", err)
		}
	})

	t.Run("少一个文件", func(t *testing.T) {
		root, m := setup(t)
		if err := os.Remove(filepath.Join(root, "share/b")); err != nil {
			t.Fatal(err)
		}
		if err := m.Verify(root); err == nil || !strings.Contains(err.Error(), "不存在") {
			t.Fatalf("少文件应该报出来,得到:%v", err)
		}
	})

	t.Run("多一个文件", func(t *testing.T) {
		root, m := setup(t)
		if err := os.WriteFile(filepath.Join(root, "share/extra"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := m.Verify(root); err == nil || !strings.Contains(err.Error(), "多出") {
			t.Fatalf("多文件应该报出来,得到:%v", err)
		}
	})

	t.Run("符号链接", func(t *testing.T) {
		root, m := setup(t)
		link := filepath.Join(root, "share/link")
		if err := os.Symlink("/etc/passwd", link); err != nil {
			t.Skipf("这个平台建不了符号链接:%v", err)
		}
		if err := m.Verify(root); err == nil || !strings.Contains(err.Error(), "不是普通文件") {
			t.Fatalf("符号链接应该报出来,得到:%v", err)
		}
	})
}

func TestArchiveName(t *testing.T) {
	name := ArchiveName("v0.1.482-main.1", "arm64")
	if name != "openbox-v0.1.482-main.1-linux-arm64.tar.gz" {
		t.Fatalf("文件名不对:%s", name)
	}
	if TopDir("v0.1.482-main.1", "arm64") != "openbox-v0.1.482-main.1-linux-arm64" {
		t.Fatalf("顶层目录不对:%s", TopDir("v0.1.482-main.1", "arm64"))
	}
	v, a, ok := ParseArchiveName(name)
	if !ok || v != "v0.1.482-main.1" || a != "arm64" {
		t.Fatalf("反解不对:%q %q %v", v, a, ok)
	}
	// 版本号里本身带 -linux- 也不能解错:从最后一段切
	v, a, ok = ParseArchiveName("openbox-v1.0.0-linux-test-linux-amd64.tar.gz")
	if !ok || v != "v1.0.0-linux-test" || a != "amd64" {
		t.Fatalf("反解带 -linux- 的版本号不对:%q %q %v", v, a, ok)
	}
	for _, n := range []string{
		"SHA256SUMS",
		"install.sh",
		"openbox-v1.0.0-linux-riscv64.tar.gz", // 架构不认
		"openbox--linux-amd64.tar.gz",         // 版本为空
		"openbox-v1.0.0-linux-amd64.zip",
		"other-v1.0.0-linux-amd64.tar.gz",
	} {
		if _, _, ok := ParseArchiveName(n); ok {
			t.Fatalf("%q 不该认成安装包", n)
		}
	}
}

func TestParseSums(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	body := "# 由 build-package.sh 生成\r\n" + a + "  openbox-v1-linux-amd64.tar.gz\n" + b + " *install.sh\n\n"
	s, err := ParseSums(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseSums 失败:%v", err)
	}
	if len(s) != 2 {
		t.Fatalf("应有 2 项,得到 %d:%v", len(s), s)
	}
	if err := s.Check("openbox-v1-linux-amd64.tar.gz", a); err != nil {
		t.Fatalf("Check 应该通过:%v", err)
	}
	if err := s.Check("openbox-v1-linux-amd64.tar.gz", strings.Repeat("c", 64)); err == nil {
		t.Fatal("校验和不一致应该报错")
	}
	if err := s.Check("openbox-v1-linux-amd64.tar.gz", ""); err == nil {
		t.Fatal("没算出校验和应该报错")
	}
	if err := s.Check("nope", a); err == nil {
		t.Fatal("SHA256SUMS 里没有的文件应该报错")
	}
	// 大小写不敏感(sha256sum 自己输出小写,但手抄的可能大写)
	if err := s.Check("install.sh", strings.ToUpper(b)); err != nil {
		t.Fatalf("大写校验和应该也认:%v", err)
	}
}

func TestParseSumsErrors(t *testing.T) {
	a := strings.Repeat("a", 64)
	bad := []string{
		"",
		"\n\n",
		a + "\n",                // 只有校验和
		a + " openbox.tar.gz\n", // 分隔符只有一个空格
		strings.Repeat("z", 64) + "  x\n",
		a + "  x\n" + a + "  x\n", // 同一个文件两次
		a + "  \n",                // 没有文件名
	}
	for _, body := range bad {
		if _, err := ParseSums(strings.NewReader(body)); err == nil {
			t.Fatalf("%q 应该报错", body)
		}
	}
}

func TestParseGeoData(t *testing.T) {
	g, err := ParseGeoData(strings.NewReader(`{"version":"2026-11-20","date":"2026-11-20","counts":{"geosite":12,"geoip":3}}`))
	if err != nil {
		t.Fatalf("ParseGeoData 失败:%v", err)
	}
	if g.Counts.Geosite != 12 || g.Counts.Geoip != 3 || g.Date != "2026-11-20" {
		t.Fatalf("解析结果不对:%+v", g)
	}
	// 没随包规则库:counts 都是 0、date 空
	if g, err := ParseGeoData(strings.NewReader(`{"version":"","date":"","counts":{"geosite":0,"geoip":0}}`)); err != nil || g.Counts.Geosite != 0 {
		t.Fatalf("空规则库应该合法:%+v %v", g, err)
	}
	if _, err := ParseGeoData(strings.NewReader(`{"date":"2026/11/20","counts":{}}`)); err == nil {
		t.Fatal("date 格式不对应该报错")
	}
	if _, err := ParseGeoData(strings.NewReader(`{"counts":{"geosite":-1}}`)); err == nil {
		t.Fatal("负数应该报错")
	}
	if _, err := ParseGeoData(strings.NewReader(`{"counts":{},"extra":1}`)); err == nil {
		t.Fatal("未知字段应该报错")
	}
}

// 生成脚本与解析器对同一份 JSON 的理解必须一致:这里把 Manifest 重新序列化再解析一遍。
func TestRoundTrip(t *testing.T) {
	m := mustParse(t, sampleJSON())
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("重新序列化后解析失败:%v", err)
	}
	if len(again.Files) != len(m.Files) || again.Version != m.Version || again.Arch != m.Arch {
		t.Fatalf("来回一趟不一致:%+v vs %+v", again, m)
	}
}
