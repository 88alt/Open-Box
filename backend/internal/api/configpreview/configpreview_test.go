package configpreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

var (
	once sync.Once
	path string
	berr error
)

func realCore(t *testing.T) string {
	t.Helper()
	once.Do(func() {
		dir, err := os.MkdirTemp("", "obcore")
		if err != nil {
			berr = err
			return
		}
		path = filepath.Join(dir, "openbox-core")
		cmd := exec.Command("go", "build", "-o", path, "./cmd/openbox-core")
		cmd.Dir = filepath.Join("..", "..", "..", "..", "core")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			berr = &failure{string(out), err}
		}
	})
	if berr != nil {
		t.Skipf("编译不了 core,跳过: %v", berr)
	}
	return path
}

type failure struct {
	out string
	err error
}

func (f *failure) Error() string { return f.err.Error() + ": " + f.out }

func handler(t *testing.T, withCore bool) (http.Handler, *subs.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	sv := subs.New(st, nil)
	var caller singbox.Caller
	var gc nodegroups.Core
	var pc profile.Core
	if withCore {
		cc := core.New(realCore(t))
		caller, gc, pc = cc, nodegroups.CoreAdapter{C: cc}, profile.CoreAdapter{C: cc}
	}
	prof := profile.New(st, pc, nil)
	groups := nodegroups.New(st, gc, sv, nodegroups.ChainsFromProfile(prof))
	gen := singbox.NewGenerator(caller, st, prof, groups, sv, singbox.System{}, singbox.Options{RulesetDir: "/r", CacheFile: "/c.db"})
	mux := http.NewServeMux()
	Register(mux, Deps{Gen: gen})
	return mux, sv
}

func get(h http.Handler, method string) (int, map[string]any) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "/api/openbox/config/preview", nil))
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestPreviewShapeAndRedaction(t *testing.T) {
	h, sv := handler(t, true)
	nm := "A"
	if _, err := sv.Create(context.Background(), subs.Input{Name: &nm, Source: &subs.Source{Content: "trojan://pw@h.example.com:443#HK 01\n"}, Rename: map[string]any{"enabled": false}}); err != nil {
		t.Fatal(err)
	}
	code, m := get(h, "GET")
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	cfg := m["config"].(map[string]any)
	tags := map[string]bool{}
	for _, o := range cfg["outbounds"].([]any) {
		tags[o.(map[string]any)["tag"].(string)] = true
	}
	if !tags["HK 01"] || !tags["直连"] || !tags["其他"] {
		t.Fatalf("tags=%v", tags)
	}
	if s := cfg["experimental"].(map[string]any)["clash_api"].(map[string]any)["secret"]; s != "***" {
		t.Fatalf("密钥应被遮住: %v", s)
	}
}

func TestPreviewCoreUnavailable(t *testing.T) {
	h, _ := handler(t, false)
	if code, m := get(h, "GET"); code != 503 || m["error"] == nil {
		t.Fatalf("%d %v", code, m)
	}
}

func TestPreviewMethod(t *testing.T) {
	h, _ := handler(t, false)
	if code, _ := get(h, "POST"); code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d", code)
	}
}
