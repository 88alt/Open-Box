package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type doc struct {
	N    int      `json:"n"`
	Tags []string `json:"tags,omitempty"`
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := newStore(t)
	var got doc
	if ok, err := s.Load("a", &got); ok || err != nil {
		t.Fatalf("不存在应返回 (false, nil): %v %v", ok, err)
	}
	if err := s.Save("a", doc{N: 3, Tags: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Load("a", &got)
	if !ok || err != nil || got.N != 3 || len(got.Tags) != 1 {
		t.Fatalf("%v %v %+v", ok, err, got)
	}
}

func TestPermissionsAndNoTempLeft(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 5; i++ {
		if err := s.Save("a", doc{N: i}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 1 || entries[0].Name() != "a.json" {
		t.Fatalf("目录里应只有 a.json: %v", entries)
	}
	if info, _ := os.Stat(filepath.Join(s.dir, "a.json")); info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("权限应仅属主可访问: %v", info.Mode())
	}
}

func TestInvalidNames(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"", "../x", "a/b", `a\b`, ".hidden", "a.json", strings.Repeat("x", 65)} {
		if err := s.Save(name, doc{}); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q 应被拒绝, 得到 %v", name, err)
		}
		if _, err := s.Load(name, &doc{}); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q Load 应被拒绝", name)
		}
	}
}

func TestCorruptFileIsAnErrorNotDefault(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.dir, "bad.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	var d doc
	if ok, err := s.Load("bad", &d); ok || err == nil {
		t.Fatalf("损坏的文件必须报错: %v %v", ok, err)
	}
	// Update 遇到损坏文件也不能覆盖它
	err := s.Update("bad", &d, func() error { d.N = 1; return nil })
	if err == nil {
		t.Fatal("Update 不应覆盖损坏文件")
	}
	if data, _ := os.ReadFile(filepath.Join(s.dir, "bad.json")); string(data) != "{oops" {
		t.Fatalf("损坏文件被改动: %q", data)
	}
}

func TestUpdateErrorKeepsOldContent(t *testing.T) {
	s := newStore(t)
	_ = s.Save("a", doc{N: 1})
	var d doc
	boom := errors.New("boom")
	if err := s.Update("a", &d, func() error { d.N = 99; return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var again doc
	_, _ = s.Load("a", &again)
	if again.N != 1 {
		t.Fatalf("fn 失败时不应写回: %+v", again)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	_ = s.Save("a", doc{N: 1})
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("重复删除不应报错: %v", err)
	}
	if ok, _ := s.Load("a", &doc{}); ok {
		t.Fatal("删除后不应存在")
	}
}

// 并发读改写:50 个 goroutine 各加 20 次,结果必须正好是 1000。
func TestConcurrentUpdate(t *testing.T) {
	s := newStore(t)
	const workers, perWorker = 50, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				var d doc
				if err := s.Update("counter", &d, func() error { d.N++; return nil }); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var d doc
	if ok, err := s.Load("counter", &d); !ok || err != nil || d.N != workers*perWorker {
		t.Fatalf("计数应为 %d: %v %v %+v", workers*perWorker, ok, err, d)
	}
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 1 {
		t.Fatalf("不应残留临时文件: %v", entries)
	}
}

// 并发读写不同文件互不阻塞也不串数据。
func TestConcurrentDifferentFiles(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = s.Save(name, doc{N: i, Tags: []string{name}})
				var d doc
				if ok, err := s.Load(name, &d); err != nil || !ok || d.Tags[0] != name {
					t.Errorf("%s 读到了别的数据: %v %v %+v", name, ok, err, d)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestWriteFileAtomicOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	for _, content := range []string{"one", "two-longer", "3"} {
		if err := WriteFileAtomic(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(p); string(data) != content {
			t.Fatalf("得到 %q, 期望 %q", data, content)
		}
	}
}
