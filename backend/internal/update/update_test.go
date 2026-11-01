package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseAndCompareVersion(t *testing.T) {
	ok := []struct {
		s    string
		want Version
	}{
		{"v0.1.299", Version{0, 1, 299, 0}},
		{"0.1.299", Version{0, 1, 299, 0}},
		{"v0.1.382-main.3", Version{0, 1, 382, 3}},
	}
	for _, c := range ok {
		if got, o := ParseVersion(c.s); !o || got != c.want {
			t.Errorf("%q: %+v %v", c.s, got, o)
		}
	}
	for _, s := range []string{"", "dev", "v1.2", "v0.1.2-rc1", "v0.1.2-main", "latest"} {
		if _, o := ParseVersion(s); o {
			t.Errorf("%q 不该被认出来", s)
		}
	}
	cmp := []struct {
		a, b string
		want int
	}{
		{"v0.1.299", "v0.1.300", -1}, {"v0.1.300", "v0.1.299", 1}, {"v0.1.299", "v0.1.299", 0},
		{"v0.1.299", "v0.1.299-main.1", -1}, {"v0.1.299-main.2", "v0.1.299-main.10", -1},
		{"v0.1.299-main.9", "v0.1.300", -1}, {"v0.2.0", "v0.1.999", 1}, {"v1.0.0", "v0.9.9", 1},
	}
	for _, c := range cmp {
		a, _ := ParseVersion(c.a)
		b, _ := ParseVersion(c.b)
		if got := a.Compare(b); got != c.want {
			t.Errorf("%s vs %s: %d want %d", c.a, c.b, got, c.want)
		}
	}
}

type fakeGH struct {
	mu       sync.Mutex
	releases string
	tags     map[string]string
	status   int
	hits     []string
	auth     string
}

func (f *fakeGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.URL.RequestURI())
	f.auth = r.Header.Get("Authorization")
	status, rel, tags := f.status, f.releases, f.tags
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/repos/timeflysoon/Open-Box/releases/tags/"):
		tag := strings.TrimPrefix(r.URL.Path, "/repos/timeflysoon/Open-Box/releases/tags/")
		if b, ok := tags[tag]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case r.URL.Path == "/repos/timeflysoon/Open-Box/releases":
		_, _ = w.Write([]byte(rel))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

const relList = `[
 {"tag_name":"v0.1.390-main.1","draft":true,"published_at":"2026-11-01T00:00:00Z"},
 {"tag_name":"v0.1.382-main.2","draft":false,"published_at":"2026-10-30T00:00:00Z","body":"修了几个问题"},
 {"tag_name":"nightly","draft":false},
 {"tag_name":"v0.1.382-main.1","draft":false},
 {"tag_name":"v0.1.318","draft":false}]`

func newChecker(t *testing.T, f *fakeGH) *Checker {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Checker{APIBase: srv.URL}
}

func TestCheck(t *testing.T) {
	f := &fakeGH{releases: relList}
	c := newChecker(t, f)
	ctx := context.Background()
	r, err := c.Check(ctx, "v0.1.382-main.1")
	if err != nil || r.Latest != "v0.1.382-main.2" || !r.HasUpdate || r.Via != "api" || r.Current != "v0.1.382-main.1" {
		t.Fatalf("%+v %v", r, err)
	}
	if f.auth != "" || !strings.Contains(f.hits[0], "/repos/timeflysoon/Open-Box/releases?per_page=30") {
		t.Errorf("只读、不带凭据: %q %v", f.auth, f.hits)
	}
	if r, _ := c.Check(ctx, "v0.1.382-main.2"); r.HasUpdate {
		t.Error("已是最新")
	}
	if r, _ := c.Check(ctx, "v0.1.400"); r.HasUpdate {
		t.Error("本机比仓库新:不算有更新")
	}
	if r, err := c.Check(ctx, "dev"); err != nil || r.HasUpdate || r.Latest == "" {
		t.Errorf("开发构建没法比较,不催升级,但仍给出最新版: %+v %v", r, err)
	}
}

func TestCheckErrors(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		f    *fakeGH
		want string
	}{
		{&fakeGH{status: 403}, "限流"}, {&fakeGH{status: 500}, "状态码 500"}, {&fakeGH{releases: "[]"}, "没有找到能识别版本号"},
		{&fakeGH{releases: "{"}, "不是预期的格式"}, {&fakeGH{status: 404}, "没有找到"},
	} {
		if _, err := newChecker(t, c.f).Check(ctx, "v0.1.1"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.f, err)
		}
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if _, err := (&Checker{APIBase: dead.URL}).Check(ctx, "v0.1.1"); err == nil || !strings.Contains(err.Error(), "连不上") {
		t.Errorf("连不上: %v", err)
	}
}

func TestMirrorFallback(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`[{"tag_name":"v0.1.5"}]`))
	}))
	defer srv.Close()
	// 直连地址连不上,镜像前缀(这里借用同一台假服务器)接得住:请求是 <前缀><原地址>
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c := &Checker{APIBase: dead.URL, MirrorPrefix: srv.URL + "/"}
	r, err := c.Check(context.Background(), "v0.1.1")
	if err != nil || r.Via != srv.URL+"/" || r.Latest != "v0.1.5" || !r.HasUpdate {
		t.Fatalf("%+v %v", r, err)
	}
	if len(paths) != 1 || !strings.HasPrefix(paths[0], "//http") && !strings.HasPrefix(paths[0], "/http") {
		t.Errorf("镜像请求应是 <前缀><原地址>: %v", paths)
	}
}

func TestNotes(t *testing.T) {
	long := strings.Repeat("字", maxNoteRunes+50)
	f := &fakeGH{releases: relList, tags: map[string]string{
		"v0.1.382-main.2": `{"tag_name":"v0.1.382-main.2","published_at":"2026-10-30T00:00:00Z","body":"修了几个问题"}`,
		"v0.1.382-main.1": `{"tag_name":"v0.1.382-main.1","body":` + mustJSON(long) + `}`,
	}}
	c := newChecker(t, f)
	ctx := context.Background()
	n := c.Notes(ctx, "v0.1.382-main.2")
	if n.Note == nil || n.Note.Body != "修了几个问题" || n.Note.Version != "v0.1.382-main.2" || n.Note.Date == "" || n.Via != "api" || n.Error != "" || !strings.HasSuffix(n.URL, "/releases") {
		t.Fatalf("%+v", n)
	}
	if n := c.Notes(ctx, "v0.1.382-main.1"); n.Note == nil || len([]rune(n.Note.Body)) > maxNoteRunes+40 || !strings.Contains(n.Note.Body, "已截断") {
		t.Errorf("说明太长应截断")
	}
	// 写法不对 / 不给:先探最新版
	if n := c.Notes(ctx, "latest"); n.Note == nil || n.Note.Version != "v0.1.382-main.2" {
		t.Errorf("写法不对应改探最新版: %+v", n)
	}
	// 取不到:永远有结果,note 为 null、error 写原因
	if n := c.Notes(ctx, "v0.1.999"); n.Note != nil || n.Error == "" || n.URL == "" {
		t.Errorf("%+v", n)
	}
	b, _ := json.Marshal(c.Notes(ctx, "v0.1.999"))
	if !strings.Contains(string(b), `"note":null`) {
		t.Errorf("note 要是 null: %s", b)
	}
	if n := newChecker(t, &fakeGH{status: 500}).Notes(ctx, ""); n.Note != nil || n.Error == "" {
		t.Errorf("探最新版也失败: %+v", n)
	}
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestValidTag(t *testing.T) {
	for s, want := range map[string]bool{"v0.1.299": true, "v0.1.382-main.2": true, "0.1.299": false, "v0.1": false, "v0.1.2-rc1": false, "../x": false, "": false} {
		if ValidTag(s) != want {
			t.Errorf("%q", s)
		}
	}
}

func TestServiceStatusRunCancel(t *testing.T) {
	s := &Service{Checker: &Checker{}, Version: "v0.1.382-main.1", BuiltAt: "2026-10-30 12:00", KernelVersion: func(context.Context) string { return "1.14.4" }}
	st := s.Status(context.Background())
	if st.Version != "v0.1.382-main.1" || st.SingboxVersion != "1.14.4" || st.BuiltAt == "" || st.Channel != (Channel{"direct", ""}) || st.Status.Running || st.Status.Stage != "idle" {
		t.Errorf("%+v", st)
	}
	s.Checker.MirrorPrefix = " https://m.example/ "
	if c := s.Status(context.Background()).Channel; c.Mode != "mirror" || c.Prefix != "https://m.example/" {
		t.Errorf("%+v", c)
	}
	if err := s.Run("direct"); err == nil || err != ErrNotReady {
		t.Errorf("%v", err)
	}
	if err := s.Run("bogus"); err == nil || err == ErrNotReady {
		t.Errorf("%v", err)
	}
	if s.Cancel() != "not-running" {
		t.Error("没有在升级")
	}
	if (&Service{Checker: &Checker{}}).Status(context.Background()).SingboxVersion != "" {
		t.Error("没接内核版本读取时是空串")
	}
	_ = time.Second
}
