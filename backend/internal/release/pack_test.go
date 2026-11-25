package release

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stage 造一个假的暂存目录:一个可执行的 bin 文件、一个普通配置文件、一个前端文件。
func stage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("bin/openbox-backend", "#!/bin/sh\necho hi\n", 0o755)
	write("etc/config.json", "{}", 0o644)
	write("panel/dist/index.html", "<html></html>", 0o644)
	return root
}

func newManifest() *Manifest {
	return &Manifest{
		Format:   Format,
		Version:  "v0.1.484-main.1",
		Upstream: 484,
		Arch:     "amd64",
		BuiltAt:  "2026-11-25T02:00:00Z",
		Components: Components{
			Backend: Component{Version: "v0.1.484-main.1"},
			Core:    Component{Version: "abc123"},
			Singbox: Component{Version: "1.14.4-openbox-60"},
		},
	}
}

func TestWriteManifestRoundTrip(t *testing.T) {
	root := stage(t)
	m := newManifest()
	if err := WriteManifest(root, m); err != nil {
		t.Fatalf("写清单失败:%v", err)
	}
	if len(m.Files) != 3 {
		t.Fatalf("应该列 3 个文件,实际 %d:%+v", len(m.Files), m.Files)
	}
	// 排序稳定:按路径
	if m.Files[0].Path != "bin/openbox-backend" || m.Files[1].Path != "etc/config.json" || m.Files[2].Path != "panel/dist/index.html" {
		t.Fatalf("顺序不对:%+v", m.Files)
	}
	// 权限位只在类 Unix 上有意义:Windows 上 chmod 是空操作,Go 读回来一律 0666。
	// 打包本身在 Linux 上跑,这里只在非 Windows 断言具体值(与 STATUS 记的那个坑一致)。
	if runtime.GOOS != "windows" {
		if m.Files[0].Mode != "0755" || m.Files[1].Mode != "0644" {
			t.Fatalf("权限写错了:%+v", m.Files)
		}
	}

	// 读回来应当完全一致(Parse 会跑 Validate)
	got, err := ParseFile(filepath.Join(root, ManifestName))
	if err != nil {
		t.Fatalf("读回清单失败:%v", err)
	}
	if got.Version != m.Version || got.Upstream != m.Upstream || got.Arch != m.Arch || len(got.Files) != len(m.Files) {
		t.Fatalf("读回来的不一致:%+v", got)
	}
	if err := got.Verify(root); err != nil {
		t.Fatalf("Verify 该通过:%v", err)
	}

	// 改一个字节:Verify 要报 sha256 不对
	p := filepath.Join(root, "etc", "config.json")
	if err := os.WriteFile(p, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(root); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("改内容后应当报 sha256,实际 %v", err)
	}
}

func TestWriteManifestRejectsNonRegular(t *testing.T) {
	root := stage(t)
	// 造一个符号链接:打包目录里不该有
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "etc", "link")); err != nil {
		t.Skipf("这个平台不支持建符号链接:%v", err)
	}
	if err := WriteManifest(root, newManifest()); err == nil {
		t.Fatal("有符号链接时应当失败")
	}
}

func TestCreateArchive(t *testing.T) {
	root := stage(t)
	m := newManifest()
	if err := WriteManifest(root, m); err != nil {
		t.Fatal(err)
	}
	// 压缩包里的模式以清单为准,不读磁盘(Windows 上读不到 0755)。这里显式写成 Linux 上该有的值,
	// 断言的就是「tar 头跟清单走」这一条。
	for i := range m.Files {
		if m.Files[i].Path == "bin/openbox-backend" {
			m.Files[i].Mode = "0755"
		} else {
			m.Files[i].Mode = "0644"
		}
	}
	builtAt := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)
	out := filepath.Join(t.TempDir(), "pkg.tar.gz")
	if err := CreateArchive(root, out, m, "openbox-v0.1.484-main.1-linux-amd64", builtAt); err != nil {
		t.Fatalf("打包失败:%v", err)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)

	var names []string
	modes := map[string]int64{}
	first := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = hdr.Name
		}
		names = append(names, hdr.Name)
		modes[hdr.Name] = hdr.Mode
		if !hdr.ModTime.Equal(builtAt) {
			t.Fatalf("%s 的时间是 %v,应为 %v", hdr.Name, hdr.ModTime, builtAt)
		}
	}

	want := []string{
		"openbox-v0.1.484-main.1-linux-amd64/manifest.json",
		"openbox-v0.1.484-main.1-linux-amd64/bin/openbox-backend",
		"openbox-v0.1.484-main.1-linux-amd64/etc/config.json",
		"openbox-v0.1.484-main.1-linux-amd64/panel/dist/index.html",
	}
	if len(names) != len(want) {
		t.Fatalf("压缩包里应有 %d 个文件,实际 %d:%v", len(want), len(names), names)
	}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("第 %d 个应是 %s,实际 %s", i, w, names[i])
		}
	}
	if first != want[0] {
		t.Fatalf("manifest.json 应当排在最前面,实际第一个是 %s", first)
	}
	if modes[want[1]] != 0o755 {
		t.Fatalf("bin/openbox-backend 的模式应是 0755,实际 %o", modes[want[1]])
	}
	if modes[want[2]] != 0o644 {
		t.Fatalf("etc/config.json 的模式应是 0644,实际 %o", modes[want[2]])
	}
}

func TestCreateArchiveRejectsBadTopDir(t *testing.T) {
	root := stage(t)
	m := newManifest()
	if err := WriteManifest(root, m); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "a/b", `a\b`} {
		if err := CreateArchive(root, filepath.Join(t.TempDir(), "x.tar.gz"), m, bad, time.Now()); err == nil {
			t.Fatalf("顶层目录 %q 应当被拒绝", bad)
		}
	}
}

func TestWriteSumsFile(t *testing.T) {
	root := stage(t)
	m := newManifest()
	if err := WriteManifest(root, m); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "openbox-v0.1.484-main.1-linux-amd64.tar.gz")
	if err := CreateArchive(root, archive, m, "openbox-v0.1.484-main.1-linux-amd64", time.Now()); err != nil {
		t.Fatal(err)
	}
	sumsPath := filepath.Join(dir, SumsName)
	if err := WriteSumsFile(sumsPath, map[string]string{filepath.Base(archive): archive}); err != nil {
		t.Fatal(err)
	}
	sums, err := ParseSumsFile(sumsPath)
	if err != nil {
		t.Fatalf("写出来的 SHA256SUMS 自己都解析不了:%v", err)
	}
	got, err := FileSHA256(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := sums.Check(filepath.Base(archive), got); err != nil {
		t.Fatalf("校验和不对:%v", err)
	}
}
