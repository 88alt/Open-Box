package traffic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jsonFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestResetClearsEverythingAndKeepsRecording(t *testing.T) {
	s, _, dir := newStore(t, at("2026-10-28", 12, 0))
	seed(t, s)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(jsonFiles(t, dir)) == 0 || s.Usage().Rows == 0 {
		t.Fatal("前提:应已有数据落盘")
	}
	// 再记一笔但不落盘:重置要连内存里没落盘的也丢掉
	if err := s.Record(at("2026-10-28", 12, 5), Delta{Up: 1, Down: 2, Conns: 1, Items: []Item{item("1.1.1.1", "x", "late.com", 1, 2, 1, false)}}); err != nil {
		t.Fatal(err)
	}

	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if files := jsonFiles(t, dir); len(files) != 0 {
		t.Errorf("目录里不该还有 .json: %v", files)
	}
	if u := s.Usage(); u.Rows != 0 || u.Bytes != 0 {
		t.Errorf("用量应清零: %+v", u)
	}
	// 重置之后后台的落盘不能把旧数据写回去
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := jsonFiles(t, dir); len(files) != 0 {
		t.Errorf("Flush 之后目录里仍不该有 .json: %v", files)
	}

	// 从空库接着记
	if err := s.Record(at("2026-10-29", 9, 0), Delta{Up: 7, Down: 9, Conns: 1, Items: []Item{item("2.2.2.2", "y", "new.com", 7, 9, 1, false)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if u := s.Usage(); u.Days != 1 || u.OldestDay != "2026-10-29" || u.NewestDay != "2026-10-29" {
		t.Errorf("重置后应只剩新记的这一天: %+v", u)
	}
	day, err := s.DayView("2026-10-28", 10, nil, true)
	if err == nil && (day.Total.Up != 0 || day.Total.Down != 0) {
		t.Errorf("旧的一天应已没有数据: %+v", day)
	}
}

func TestResetOnEmptyAndIgnoresOtherFiles(t *testing.T) {
	s, _, dir := newStore(t, at("2026-10-28", 12, 0))
	if err := s.Reset(); err != nil {
		t.Fatalf("空库重置不该出错: %v", err)
	}
	keep := filepath.Join(dir, "README.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("不是 .json 的文件不该被删: %v", err)
	}
}

// 目录没了读不出来:报错,同时内存已经清掉(不会有旧数据留着被写回去)。
func TestResetMissingDirReportsError(t *testing.T) {
	s, _, dir := newStore(t, at("2026-10-28", 12, 0))
	seed(t, s)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err == nil {
		t.Fatal("读不了目录应返回错误")
	}
	if s.cur != nil || len(s.idx.Days) != 0 || s.dirtyDay || s.dirtyHrs {
		t.Error("内存里的数据应已清掉")
	}
}
