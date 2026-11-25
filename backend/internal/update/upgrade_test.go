package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/release"
)

// ---------- 造一个真的安装包(走 release 的打包代码,与发布时同一套) ----------

type pkg struct {
	name    string
	archive []byte
	sum     string
	root    string // 原始目录,给需要比内容的用例
}

func makePackage(t *testing.T, version, arch string, files map[string]string) pkg {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	m := &release.Manifest{
		Format: release.Format, Version: version, Upstream: 484, Arch: arch,
		BuiltAt: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	if err := release.WriteManifest(root, m); err != nil {
		t.Fatal(err)
	}
	name := release.ArchiveName(version, arch)
	out := filepath.Join(dir, name)
	if err := release.CreateArchive(root, out, m, release.TopDir(version, arch), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return pkg{name: name, archive: b, sum: hex.EncodeToString(sum[:]), root: root}
}

// ---------- 假的发布服务器 ----------

type fakeUpgrade struct {
	mu      sync.Mutex
	base    string
	tag     string
	archive pkg
	sums    string // 空 = 按真摘要给
	block   chan struct{}
	hits    []string
}

func (f *fakeUpgrade) set(fn func(*fakeUpgrade)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeUpgrade) releaseJSON(tag string) string {
	return fmt.Sprintf(`{"tag_name":%q,"draft":false,"assets":[`+
		`{"name":%q,"browser_download_url":%q,"size":%d},`+
		`{"name":"SHA256SUMS","browser_download_url":%q,"size":42}]}`,
		tag, f.archive.name, f.base+"/dl/"+f.archive.name, len(f.archive.archive), f.base+"/dl/SHA256SUMS")
}

func (f *fakeUpgrade) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	tag, sums, block, base := f.tag, f.sums, f.block, f.base
	f.hits = append(f.hits, r.URL.Path)
	f.mu.Unlock()
	if sums == "" {
		sums = f.archive.sum + "  " + f.archive.name + "\n"
	}
	switch {
	case r.URL.Path == "/repos/timeflysoon/Open-Box/releases":
		_, _ = io.WriteString(w, "["+f.releaseJSON(tag)+"]")
	case strings.HasPrefix(r.URL.Path, "/repos/timeflysoon/Open-Box/releases/tags/"):
		_, _ = io.WriteString(w, f.releaseJSON(tag))
	case r.URL.Path == "/dl/SHA256SUMS":
		_, _ = io.WriteString(w, sums)
	case r.URL.Path == "/dl/"+f.archive.name:
		if block != nil {
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(f.archive.archive)))
		_, _ = w.Write(f.archive.archive)
	case strings.HasPrefix(r.URL.Path, "/mirror/"):
		// 镜像前缀指向自己时,前缀会被拼在完整地址前面,这里只可能是 API 的镜像
		_, _ = io.WriteString(w, "["+f.releaseJSON(tag)+"]")
	default:
		_ = base
		w.WriteHeader(http.StatusNotFound)
	}
}

func newUpgradeService(t *testing.T, files map[string]string, opts ...func(*Service)) (*Service, *fakeUpgrade) {
	t.Helper()
	p := makePackage(t, "v0.1.484-main.2", "amd64", files)
	f := &fakeUpgrade{tag: "v0.1.484-main.2", archive: p}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.set(func(f *fakeUpgrade) { f.base = srv.URL })

	s := &Service{
		Checker:   &Checker{APIBase: srv.URL, Arch: "amd64", Client: srv.Client()},
		Version:   "v0.1.484-main.1",
		DataDir:   t.TempDir(),
		Client:    srv.Client(),
		Smoke:     func(context.Context, string) error { return nil },
		FreeSpace: func(string) (int64, error) { return 1 << 40, nil },
	}
	for _, o := range opts {
		o(s)
	}
	return s, f
}

// waitJob 等到升级跑完,返回最终状态。
func waitJob(t *testing.T, s *Service) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := s.Status(context.Background())
		if !st.Status.Running {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("升级没在 10 秒内结束")
	return Status{}
}

func files() map[string]string {
	return map[string]string{
		"bin/openbox-backend":   "backend",
		"bin/openbox-core":      "core",
		"bin/sing-box":          "singbox",
		"panel/dist/index.html": "<html>",
	}
}

// ---------- Extract ----------

func TestExtractRoundTrip(t *testing.T) {
	p := makePackage(t, "v0.1.484-main.2", "amd64", files())
	dir := t.TempDir()
	var progress int64
	var seen *release.Manifest
	got, err := Extract(context.Background(), bytes.NewReader(p.archive), int64(len(p.archive)), dir, ExtractHooks{
		OnProgress: func(done, total int64) {
			progress = done
			if total != int64(len(p.archive)) {
				t.Errorf("total 应是压缩包长度: %d", total)
			}
		},
		OnManifest: func(m release.Manifest) error { seen = &m; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sum != p.sum || got.Size != int64(len(p.archive)) {
		t.Errorf("摘要或长度不对: %s %d", got.Sum, got.Size)
	}
	if seen == nil || seen.Version != "v0.1.484-main.2" || seen.Arch != "amd64" {
		t.Errorf("manifest 回调没拿到: %+v", seen)
	}
	if progress != int64(len(p.archive)) {
		t.Errorf("进度没走完: %d / %d", progress, len(p.archive))
	}
	if !strings.HasSuffix(got.Root, release.TopDir("v0.1.484-main.2", "amd64")) {
		t.Errorf("顶层目录不对: %s", got.Root)
	}
	b, err := os.ReadFile(filepath.Join(got.Root, "bin", "sing-box"))
	if err != nil || string(b) != "singbox" {
		t.Errorf("文件内容不对: %q %v", b, err)
	}
	// 权限按包头写:bin/* 要有执行位(Windows 上 chmod 是空操作,不比)
	if info, err := os.Stat(filepath.Join(got.Root, "bin", "sing-box")); err == nil && os.PathSeparator == '/' && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("bin/sing-box 没有执行位: %v", info.Mode())
	}
	// 解出来的东西要能过 Verify
	if err := got.Manifest.Verify(got.Root); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// tarEntry 是一条要写进压缩包的东西;kind 空 = 普通文件。
type tarEntry struct {
	name string
	body string
	kind byte
	link string
}

func buildRawTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body))}
		if e.kind != 0 {
			h.Typeflag = e.kind
			h.Size = 0
			h.Linkname = e.link
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractRejectsUnsafeArchives(t *testing.T) {
	manifest := `{"format":"open-box-release","version":"v0.1.484-main.2","upstream":484,"arch":"amd64","builtAt":"2026-11-25T00:00:00Z","components":{"backend":{"version":"x"},"core":{"version":"x"},"singbox":{"version":"x"},"panel":{"version":"x"},"geodata":{}},"files":[{"path":"bin/a","sha256":"` + strings.Repeat("0", 64) + `","size":1,"mode":"0755"}]}`
	top := release.TopDir("v0.1.484-main.2", "amd64")
	ok := tarEntry{name: top + "/" + release.ManifestName, body: manifest}
	for _, c := range []struct {
		name    string
		entries []tarEntry
		want    string
	}{
		{"跳出目录", []tarEntry{ok, {name: top + "/../evil", body: "x"}}, "不安全"},
		{"绝对路径", []tarEntry{ok, {name: "/etc/passwd", body: "x"}}, "不安全"},
		{"反斜杠", []tarEntry{ok, {name: top + `/bin\x`, body: "x"}}, "不安全"},
		{"符号链接", []tarEntry{ok, {name: top + "/bin/link", kind: tar.TypeSymlink, link: "/etc/passwd"}}, "不支持的条目类型"},
		{"硬链接", []tarEntry{ok, {name: top + "/bin/link", kind: tar.TypeLink, link: top + "/bin/a"}}, "不支持的条目类型"},
		{"两个顶层目录", []tarEntry{ok, {name: "other/x", body: "x"}}, "不止一个顶层目录"},
		{"没有 manifest", []tarEntry{{name: top + "/bin/a", body: "x"}}, "没有 manifest.json"},
		{"顶层目录与 manifest 不符", []tarEntry{{name: "wrong/manifest.json", body: manifest}}, "按 manifest 应该是"},
	} {
		dir := t.TempDir()
		raw := buildRawTar(t, c.entries)
		if _, err := Extract(context.Background(), bytes.NewReader(raw), int64(len(raw)), dir, ExtractHooks{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// 不是 gzip
	if _, err := Extract(context.Background(), strings.NewReader("not gzip"), 8, t.TempDir(), ExtractHooks{}); err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Errorf("不是 gzip: %v", err)
	}
}

func TestExtractOnManifestCanAbort(t *testing.T) {
	p := makePackage(t, "v0.1.484-main.2", "amd64", files())
	dir := t.TempDir()
	_, err := Extract(context.Background(), bytes.NewReader(p.archive), 0, dir, ExtractHooks{
		OnManifest: func(release.Manifest) error { return fmt.Errorf("空间不够") },
	})
	if err == nil || !strings.Contains(err.Error(), "空间不够") {
		t.Fatalf("%v", err)
	}
}

func TestCheckSpace(t *testing.T) {
	free := func(n int64) func(string) (int64, error) {
		return func(string) (int64, error) { return n, nil }
	}
	if err := checkSpace(free(100<<20), "/x", 50<<20); err != nil {
		t.Errorf("够用: %v", err)
	}
	if err := checkSpace(free(50<<20), "/x", 50<<20); err == nil || !strings.Contains(err.Error(), "磁盘空间不够") {
		t.Errorf("要留余量: %v", err)
	}
	// 量不出来就不拦
	if err := checkSpace(func(string) (int64, error) { return 0, fmt.Errorf("不支持") }, "/x", 1<<30); err != nil {
		t.Errorf("量不出来时不该拦: %v", err)
	}
}

// ---------- Plan ----------

func TestPlanPicksAssetAndChannel(t *testing.T) {
	s, f := newUpgradeService(t, files())
	ctx := context.Background()
	p, err := s.Checker.Plan(ctx, "direct", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Tag != "v0.1.484-main.2" || p.Arch != "amd64" || p.Via != "api" || p.Archive.Name != f.archive.name || p.Sums.Name != release.SumsName {
		t.Errorf("%+v", p)
	}
	// 指定版本走 tags 接口
	if p, err := s.Checker.Plan(ctx, "direct", "v0.1.484-main.2"); err != nil || p.Tag != "v0.1.484-main.2" {
		t.Errorf("%+v %v", p, err)
	}
	// 没有镜像前缀时不能用 mirror
	if _, err := s.Checker.Plan(ctx, "mirror", ""); err == nil || !strings.Contains(err.Error(), "镜像前缀") {
		t.Errorf("%v", err)
	}
	// 通道写错
	if _, err := s.Checker.Plan(ctx, "bogus", ""); err == nil || !strings.Contains(err.Error(), "通道只能是") {
		t.Errorf("%v", err)
	}
	// 本机架构没有包
	s.Checker.Arch = "386"
	if _, err := s.Checker.Plan(ctx, "direct", ""); err == nil || !strings.Contains(err.Error(), "没有安装包") {
		t.Errorf("%v", err)
	}
}

func TestPlanNeedsSums(t *testing.T) {
	p := makePackage(t, "v0.1.484-main.2", "amd64", files())
	f := &fakeUpgrade{tag: "v0.1.484-main.2", archive: p}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只有安装包,没有 SHA256SUMS
		fmt.Fprintf(w, `[{"tag_name":"v0.1.484-main.2","assets":[{"name":%q,"browser_download_url":%q,"size":1}]}]`,
			p.name, "https://dl.example/"+p.name)
	}))
	defer srv.Close()
	_ = f
	c := &Checker{APIBase: srv.URL, Arch: "amd64"}
	if _, err := c.Plan(context.Background(), "direct", ""); err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Errorf("没有校验和清单要拒绝: %v", err)
	}
}

// ---------- 整条流水线(M6-5:到冒烟为止,不碰系统) ----------

func TestRunStopsBeforeCommit(t *testing.T) {
	s, _ := newUpgradeService(t, files())
	var smoked string
	s.Smoke = func(_ context.Context, root string) error {
		smoked = root
		// 冒烟时文件必须已经落地且能读
		if _, err := os.Stat(filepath.Join(root, "bin", "openbox-backend")); err != nil {
			t.Errorf("冒烟时文件不在: %v", err)
		}
		return nil
	}
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	st := waitJob(t, s)
	if st.Status.Stage != StageFailed || !strings.Contains(st.Status.Message, "还没有实现") || !strings.Contains(st.Status.Message, "没有改动系统") {
		t.Fatalf("停在提交之前: %+v", st.Status)
	}
	if smoked == "" {
		t.Error("没跑冒烟")
	}
	// 暂存目录要清掉,不留半截状态
	if _, err := os.Stat(filepath.Join(s.DataDir, "update", "v0.1.484-main.2")); !os.IsNotExist(err) {
		t.Errorf("暂存目录应清掉: %v", err)
	}
	// 日志尾部要能看出走到哪了
	for _, want := range []string{StageProbing, StageDownloading, StageVerifying, StageExtracting, "冒烟检查通过"} {
		if !strings.Contains(st.LogTail, want) {
			t.Errorf("日志里没有 %q:\n%s", want, st.LogTail)
		}
	}
	// 进度里的字节数要填上
	if st.Status.Bytes == nil || *st.Status.Bytes == 0 {
		t.Errorf("没有下载进度: %+v", st.Status)
	}
}

func TestRunAlreadyLatest(t *testing.T) {
	s, _ := newUpgradeService(t, files())
	s.Version = "v0.1.484-main.2"
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	st := waitJob(t, s)
	if st.Status.Stage != StageDone || !strings.Contains(st.Status.Message, "已经是最新版") {
		t.Fatalf("%+v", st.Status)
	}
}

func TestRunChecksumMismatch(t *testing.T) {
	s, f := newUpgradeService(t, files())
	f.set(func(f *fakeUpgrade) {
		f.sums = strings.Repeat("0", 64) + "  " + f.archive.name + "\n"
	})
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	st := waitJob(t, s)
	if st.Status.Stage != StageFailed || !strings.Contains(st.Status.Message, "校验和对不上") {
		t.Fatalf("%+v", st.Status)
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, "update", f.tag)); !os.IsNotExist(err) {
		t.Errorf("校验失败要丢掉暂存目录: %v", err)
	}
}

func TestRunChecksumListMissingFile(t *testing.T) {
	s, f := newUpgradeService(t, files())
	f.set(func(f *fakeUpgrade) { f.sums = strings.Repeat("a", 64) + "  openbox-别的-linux-amd64.tar.gz\n" })
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	if st := waitJob(t, s); st.Status.Stage != StageFailed || !strings.Contains(st.Status.Message, "SHA256SUMS 里没有") {
		t.Fatalf("%+v", st.Status)
	}
}

func TestRunSmokeFailureLeavesSystemAlone(t *testing.T) {
	s, _ := newUpgradeService(t, files())
	s.Smoke = func(context.Context, string) error { return fmt.Errorf("新的 sing-box 起不来") }
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	st := waitJob(t, s)
	if st.Status.Stage != StageFailed || !strings.Contains(st.Status.Message, "起不来") {
		t.Fatalf("%+v", st.Status)
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, "update", "v0.1.484-main.2")); !os.IsNotExist(err) {
		t.Errorf("冒烟失败也要清掉暂存: %v", err)
	}
}

func TestRunNotEnoughSpace(t *testing.T) {
	s, _ := newUpgradeService(t, files())
	s.FreeSpace = func(string) (int64, error) { return 1 << 10, nil }
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	if st := waitJob(t, s); st.Status.Stage != StageFailed || !strings.Contains(st.Status.Message, "磁盘空间不够") {
		t.Fatalf("%+v", st.Status)
	}
}

func TestRunBusyAndCancel(t *testing.T) {
	s, f := newUpgradeService(t, files())
	block := make(chan struct{})
	f.set(func(f *fakeUpgrade) { f.block = block })
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	// 等它进到下载阶段再试第二次
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && s.Status(context.Background()).Status.Stage != StageDownloading {
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Run("direct"); err != ErrBusy {
		t.Fatalf("同时只能一个升级: %v", err)
	}
	if got := s.Cancel(); got != "cancelled" {
		t.Fatalf("取消: %s", got)
	}
	close(block)
	st := waitJob(t, s)
	if st.Status.Stage != StageCancelled {
		t.Fatalf("%+v", st.Status)
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, "update", f.tag)); !os.IsNotExist(err) {
		t.Errorf("取消要清掉暂存: %v", err)
	}
	// 取消之后可以再开一次
	if err := s.Run("direct"); err != nil {
		t.Fatalf("取消后应能重开: %v", err)
	}
	waitJob(t, s)
}

func TestRunRequiresDataDir(t *testing.T) {
	s, _ := newUpgradeService(t, files())
	s.DataDir = ""
	if err := s.Run("direct"); err != nil {
		t.Fatal(err)
	}
	if st := waitJob(t, s); st.Status.Stage != StageFailed || !strings.Contains(st.Status.Message, "数据目录") {
		t.Fatalf("%+v", st.Status)
	}
}

// ---------- status 的规则库字段 ----------

func TestStatusGeo(t *testing.T) {
	dir := t.TempDir()
	s := &Service{Checker: &Checker{}, GeoDir: dir}
	if _, _, c := s.geo(); c != nil {
		t.Errorf("空目录不该给 counts: %+v", c)
	}
	// 没有 geodata.json 时数文件
	for _, n := range []string{"geosite-cn.srs", "geosite-geolocation-!cn.srs", "geoip-cn.srs", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	v, d, c := s.geo()
	if v != "" || d != "" || c == nil || c.Geosite != 2 || c.Geoip != 1 {
		t.Errorf("数文件: %q %q %+v", v, d, c)
	}
	// 有 geodata.json 时以它为准
	gj := `{"version":"825dc1aa","date":"2026-11-25","counts":{"geosite":7,"geoip":3}}`
	if err := os.WriteFile(filepath.Join(dir, release.GeoDataName), []byte(gj), 0o644); err != nil {
		t.Fatal(err)
	}
	v, d, c = s.geo()
	if v != "825dc1aa" || d != "2026-11-25" || c.Geosite != 7 || c.Geoip != 3 {
		t.Errorf("按清单: %q %q %+v", v, d, c)
	}
	// 坏清单退回数文件,不报错
	if err := os.WriteFile(filepath.Join(dir, release.GeoDataName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, c := s.geo(); c == nil || c.Geosite != 2 {
		t.Errorf("坏清单要退回数文件: %+v", c)
	}
	// Status 把它带出去,没值时不出现这些字段
	st := s.Status(context.Background())
	if st.GeoCounts == nil || st.GeoCounts.Geosite != 2 {
		t.Errorf("%+v", st)
	}
	empty, _ := json.Marshal((&Service{Checker: &Checker{}}).Status(context.Background()))
	if strings.Contains(string(empty), "geoCounts") {
		t.Errorf("没有规则库时不该出现 geoCounts: %s", empty)
	}
}
