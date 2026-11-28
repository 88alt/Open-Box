package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 这组测试把 update/run 的**整条链路**跑完:探版本 → 下载 → 校验 sha256 →
// 解包 → 提交换位 → 请求重启。用的包不是手搓的,而是**真实打包脚本产出的那个**
// (环境变量 OPENBOX_REAL_PACKAGE 指定),假 GitHub 只负责按真实 API 的形状把
// 发布信息与资产吐出来。
//
// 这么做的意义:此前 TestRealPackageRoundTrip 验的是「拿包直接 Extract + Commit」,
// upgrade_test 里那些验的是「服务流程」但包里只有几个占位文件。两者之间那条缝
// ——「Service 真的能把一个真包从头装到尾」——一直没验过。这里把它补上。
//
// 用法:
//
//	OPENBOX_REAL_PACKAGE=/path/to/openbox-vX-linux-amd64.tar.gz go test ./internal/update/ -run TestRealPackageThroughService
func TestRealPackageThroughService(t *testing.T) {
	pkgPath := os.Getenv("OPENBOX_REAL_PACKAGE")
	if pkgPath == "" {
		t.Skip("没有指定 OPENBOX_REAL_PACKAGE(真实发布包路径)")
	}
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("发布包读不到:%v", err)
	}
	sum := sha256.Sum256(raw)
	sumHex := hex.EncodeToString(sum[:])
	base := filepath.Base(pkgPath)

	// 从包名里只取架构;版本改用**能被 ParseVersion 认出来**的号。
	// 真实包可能叫 v0.1.999-main.test1 这种带自定义后缀的名字,而升级器只认
	// vX.Y.Z 与 vX.Y.Z-main.N(见 versionRe),用原名会让测试卡在「探测最新版」这一步。
	// 这里验的是「Service 能把真包从头装到尾」,不是版本号解析,所以换一个合法 tag 即可;
	// 包内清单里的版本不影响解包与提交换位。
	var arch string
	{
		stripped := strings.TrimSuffix(strings.TrimSuffix(base, ".gz"), ".tar")
		parts := strings.Split(stripped, "-linux-")
		if len(parts) != 2 {
			t.Fatalf("包名不像 openbox-<版本>-linux-<架构>.tar.gz:%s", base)
		}
		arch = parts[1]
	}
	tag := "v0.1.999-main.1"
	assetName := "openbox-" + tag + "-linux-" + arch + ".tar.gz"

	// 假 GitHub:按真实 API 的形状回答 releases/latest 与资产下载
	var mu sync.Mutex
	var downloaded int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"draft":    false,
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": "http://" + r.Host + "/download/" + assetName},
					{"name": "SHA256SUMS", "browser_download_url": "http://" + r.Host + "/download/SHA256SUMS"},
				},
			})
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"draft":    false,
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": "http://" + r.Host + "/download/" + assetName},
					{"name": "SHA256SUMS", "browser_download_url": "http://" + r.Host + "/download/SHA256SUMS"},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode([]any{map[string]any{
				"tag_name": tag,
				"draft":    false,
				"assets": []map[string]any{
					{"name": assetName, "browser_download_url": "http://" + r.Host + "/download/" + assetName},
					{"name": "SHA256SUMS", "browser_download_url": "http://" + r.Host + "/download/SHA256SUMS"},
				},
			}})
		}
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case assetName:
			mu.Lock()
			downloaded++
			mu.Unlock()
			w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
			_, _ = w.Write(raw)
		case "SHA256SUMS":
			_, _ = w.Write([]byte(sumHex + "  " + assetName + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 模拟一台已经装了旧版本的机器
	root := t.TempDir()
	dataDir := t.TempDir()
	old := map[string]string{
		"bin/openbox-backend":     "OLD-BACKEND",
		"bin/openbox-core":        "OLD-CORE",
		"panel/dist/index.html":   "<html>OLD</html>",
		"data/state/profile.json": `{"old":true}`,
		"etc/router-kernel":       "mihomo",
	}
	for rel, body := range old {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	restarted := make(chan struct{}, 1)
	s := &Service{
		Checker:     &Checker{APIBase: srv.URL, Arch: arch, Client: srv.Client()},
		Version:     "v0.1.1-old",
		DataDir:     dataDir,
		Client:      srv.Client(),
		InstallRoot: root,
		StateDir:    filepath.Join(root, "data", "state"),
		Smoke:       func(context.Context, string) error { return nil },
		FreeSpace:   func(string) (int64, error) { return 1 << 40, nil },
		Restart: func(context.Context) error {
			select {
			case restarted <- struct{}{}:
			default:
			}
			return nil
		},
		// 看门狗在测试里不真起(它要 setsid 和 90 秒等待);提交与重启这两段是真跑的
		StartWatchdog: func(WatchdogRequest) (string, error) { return "test-watchdog", nil },
	}

	if err := s.Run("auto"); err != nil {
		t.Fatalf("启动升级失败:%v", err)
	}
	st := waitJob(t, s)
	if st.Status.Stage != StageDone {
		t.Fatalf("升级没走到 done:%+v\n%s", st.Status, st.LogTail)
	}
	mu.Lock()
	got := downloaded
	mu.Unlock()
	if got == 0 {
		t.Fatal("根本没有下载包")
	}
	select {
	case <-restarted:
	default:
		t.Fatal("升级完成后应请求重启服务")
	}

	// 装上去的必须是包里那份内容,而不是旧内容
	back, err := os.ReadFile(filepath.Join(root, "bin/openbox-backend"))
	if err != nil {
		t.Fatal(err)
	}
	if string(back) == "OLD-BACKEND" {
		t.Fatal("bin/openbox-backend 还是旧的,提交没生效")
	}
	// 提交是**按目录整块换位**的(替换清单只含 bin / panel/dist / geodata / openwrt / debian),
	// manifest.json 不在其中、不会被搬进安装根。所以这里核对「装上去的东西确实来自包」:
	// bin 目录里应当出现包里那几个文件,而且不再是旧内容。
	for _, rel := range []string{"bin/openbox-backend", "bin/openbox-core"} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("装上去的 %s 读不到:%v", rel, err)
		}
		if len(b) == 0 || string(b) == "OLD-BACKEND" || string(b) == "OLD-CORE" {
			t.Fatalf("%s 还是旧内容(长度 %d)", rel, len(b))
		}
	}
	// 用户数据与配置一律不能动
	if b, _ := os.ReadFile(filepath.Join(root, "data/state/profile.json")); string(b) != `{"old":true}` {
		t.Fatal("升级不该动 data/ 下的用户数据")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/router-kernel")); string(b) != "mihomo" {
		t.Fatal("升级不该动 etc/ 下的配置")
	}
}

// 校验和不对时必须拦下,且一个文件都不许动。这条用真包 + 假 GitHub 做,
// 比手搓 tar 更接近真实:能确认「下载 → 算 sha256 → 对 SHA256SUMS」这段真的在跑。
func TestRealPackageChecksumMismatchBlocks(t *testing.T) {
	pkgPath := os.Getenv("OPENBOX_REAL_PACKAGE")
	if pkgPath == "" {
		t.Skip("没有指定 OPENBOX_REAL_PACKAGE")
	}
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(pkgPath)
	stripped := strings.TrimSuffix(strings.TrimSuffix(base, ".gz"), ".tar")
	parts := strings.Split(stripped, "-linux-")
	if len(parts) != 2 {
		t.Fatalf("包名不对:%s", base)
	}
	tag, arch := strings.TrimPrefix(parts[0], "openbox-"), parts[1]

	mux := http.NewServeMux()
	asset := func(host string) []map[string]any {
		return []map[string]any{
			{"name": base, "browser_download_url": "http://" + host + "/download/" + base},
			{"name": "SHA256SUMS", "browser_download_url": "http://" + host + "/download/SHA256SUMS"},
		}
	}
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag, "draft": false, "assets": asset(r.Host),
		})
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == base {
			w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
			_, _ = w.Write(raw)
			return
		}
		// 故意给一个错的校验和
		_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  " + base + "\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin/openbox-backend"), []byte("OLD-BACKEND"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, "bin/openbox-backend"))

	s := &Service{
		Checker:     &Checker{APIBase: srv.URL, Arch: arch, Client: srv.Client()},
		Version:     "v0.1.1-old",
		DataDir:     t.TempDir(),
		Client:      srv.Client(),
		InstallRoot: root,
		StateDir:    filepath.Join(root, "data", "state"),
		Smoke:       func(context.Context, string) error { return nil },
		FreeSpace:   func(string) (int64, error) { return 1 << 40, nil },
		Restart:     func(context.Context) error { return nil },
		StartWatchdog: func(WatchdogRequest) (string, error) {
			t.Error("校验没过不该起看门狗")
			return "", nil
		},
	}
	if err := s.Run("auto"); err != nil {
		t.Fatalf("启动升级失败:%v", err)
	}
	st := waitJob(t, s)
	if st.Status.Stage == StageDone {
		t.Fatalf("校验和不对却升级成功了:%+v", st.Status)
	}
	after, _ := os.ReadFile(filepath.Join(root, "bin/openbox-backend"))
	if string(after) != string(before) {
		t.Fatal("校验失败时一个文件都不该动")
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); err == nil {
		t.Fatal("校验失败时不该把文件解到安装根里")
	}
	_ = time.Second
}
