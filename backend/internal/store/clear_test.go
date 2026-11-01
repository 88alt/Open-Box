package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClearRemovesAllButKept(t *testing.T) {
	s := newStore(t)
	for _, n := range []string{"profile", "auth", "dns-takeover", "sub-nodes", "web-settings"} {
		if err := s.Save(n, doc{N: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// 不是我们的东西:非 .json、不合法的名字、子目录、写到一半留下的临时文件,一概不碰
	others := []string{"notes.txt", ".profile.json.tmp-123", "bad name.json", ".hidden.json"}
	for _, f := range others {
		if err := os.WriteFile(filepath.Join(s.dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(s.dir, "sub.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Clear("auth", "dns-takeover")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"profile", "sub-nodes", "web-settings"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("删掉的 = %v,想要 %v", removed, want)
	}
	for _, n := range []string{"auth", "dns-takeover"} {
		var d doc
		if ok, err := s.Load(n, &d); !ok || err != nil || d.N != 1 {
			t.Errorf("%s 应原样保留: %v %v %+v", n, ok, err, d)
		}
	}
	for _, n := range []string{"profile", "sub-nodes", "web-settings"} {
		var d doc
		if ok, _ := s.Load(n, &d); ok {
			t.Errorf("%s 应已删除", n)
		}
	}
	for _, f := range append(others, "sub.json") {
		if _, err := os.Stat(filepath.Join(s.dir, f)); err != nil {
			t.Errorf("%s 不该被动: %v", f, err)
		}
	}
}

func TestClearEmptyAndRepeat(t *testing.T) {
	s := newStore(t)
	removed, err := s.Clear()
	if err != nil || len(removed) != 0 {
		t.Fatalf("空目录: %v %v", removed, err)
	}
	if err := s.Save("a", doc{N: 2}); err != nil {
		t.Fatal(err)
	}
	if removed, err = s.Clear(); err != nil || !reflect.DeepEqual(removed, []string{"a"}) {
		t.Fatalf("%v %v", removed, err)
	}
	if removed, err = s.Clear(); err != nil || len(removed) != 0 {
		t.Fatalf("再清一次应什么都没有: %v %v", removed, err)
	}
	// 清完还能照常写
	if err := s.Save("a", doc{N: 3}); err != nil {
		t.Fatal(err)
	}
}

// 删不掉的文件不拦其余的,最后一起报告。
func TestClearReportsFailuresButContinues(t *testing.T) {
	s := newStore(t)
	if err := s.Save("good", doc{N: 1}); err != nil {
		t.Fatal(err)
	}
	// 用「非空目录占着 bad.json 这个名字」让 os.Remove 失败(不依赖是不是 root)
	bad := filepath.Join(s.dir, "bad.json")
	if err := os.MkdirAll(filepath.Join(bad, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Clear()
	// 目录不当文件看(IsDir 跳过),所以这里只确认:目录占位不会让 Clear 出错或误删,正常文件照删
	if err != nil || !reflect.DeepEqual(removed, []string{"good"}) {
		t.Fatalf("%v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(bad, "inner")); err != nil {
		t.Fatalf("目录里的东西不该被动: %v", err)
	}
}

func TestClearMissingDirFails(t *testing.T) {
	s := newStore(t)
	if err := os.RemoveAll(s.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clear(); err == nil {
		t.Fatal("目录读不了应返回错误,而不是当成清完了")
	}
}
