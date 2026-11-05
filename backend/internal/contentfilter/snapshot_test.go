package contentfilter

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func saveText(t *testing.T, e *env, name, text string) string {
	t.Helper()
	l, err := e.svc.Save(ctx(), SaveInput{Name: name, Dialect: "surge", Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	return l.Sets[len(l.Sets)-1].ID
}

func TestSnapshotEmpty(t *testing.T) {
	e := newEnv(t)
	sn, err := e.svc.Snapshot(ctx(), 0)
	if err != nil || sn.Sets == nil || len(sn.Sets) != 0 || sn.Size != 0 || len(sn.SHA256) != 64 {
		t.Fatalf("没有集合时应是空数组、size 0、有摘要: %+v %v", sn, err)
	}
	l, _ := e.svc.List(ctx())
	if sn.SHA256 != l.SHA256 {
		t.Fatal("空快照的摘要应与列表一致")
	}
}

func TestSnapshotEnabledOnlySortedWithText(t *testing.T) {
	e := newEnv(t)
	idB := saveText(t, e, "乙", "[Rule]\nDOMAIN,b.com,REJECT\n")    // cf1
	idA := saveText(t, e, "甲", "[Rule]\nDOMAIN,a.com,REJECT\n")    // cf2
	idC := saveText(t, e, "丙", "[Rule]\nDOMAIN,c.com,REJECT\n")    // cf3
	if _, err := e.svc.SetEnabled(ctx(), idC, false); err != nil { // 停用的不进快照
		t.Fatal(err)
	}
	sn, err := e.svc.Snapshot(ctx(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sn.Sets) != 2 || sn.Sets[0].ID != idB || sn.Sets[1].ID != idA {
		t.Fatalf("应只含启用的两份、按 id 排序(cf1 在 cf2 前): %+v", sn.Sets)
	}
	got := sn.Sets[0]
	if got.Name != "乙" || got.Dialect != "surge" || got.RuleCount != 1 || got.UpdatedAt != "2026-11-04T08:00:00Z" ||
		got.Text != "[Rule]\nDOMAIN,b.com,REJECT\n" {
		t.Fatalf("字段不对: %+v", got)
	}
	l, _ := e.svc.List(ctx())
	if sn.SHA256 != l.SHA256 || sn.Size != l.Size {
		t.Fatalf("sha256 / size 应与 GET /content-filter 同一口径: %s/%d vs %s/%d", sn.SHA256, sn.Size, l.SHA256, l.Size)
	}
}

func TestSnapshotTracksChanges(t *testing.T) {
	e := newEnv(t)
	id := saveText(t, e, "甲", "[Rule]\nDOMAIN,a.com,REJECT\n")
	s1, _ := e.svc.Snapshot(ctx(), 0)
	txt := "[Rule]\nDOMAIN,a.com,REJECT\nDOMAIN,z.com,REJECT\n"
	if _, err := e.svc.Save(ctx(), SaveInput{ID: id, Name: "甲", Dialect: "surge", Text: &txt}); err != nil {
		t.Fatal(err)
	}
	s2, err := e.svc.Snapshot(ctx(), 0)
	if err != nil || s2.SHA256 == s1.SHA256 || s2.Sets[0].RuleCount != 2 || !strings.Contains(s2.Sets[0].Text, "z.com") {
		t.Fatalf("改了文本,快照和摘要应跟着变: %+v %v", s2, err)
	}
	if _, err := e.svc.Delete(ctx(), id); err != nil {
		t.Fatal(err)
	}
	s3, _ := e.svc.Snapshot(ctx(), 0)
	if len(s3.Sets) != 0 {
		t.Fatalf("删了就不该在快照里: %+v", s3)
	}
}

func TestSnapshotTooLargeIsReportedNotTruncated(t *testing.T) {
	e := newEnv(t)
	saveText(t, e, "甲", "[Rule]\nDOMAIN,a.com,REJECT\n")
	saveText(t, e, "乙", "[Rule]\nDOMAIN,b.com,REJECT\n")
	l, _ := e.svc.List(ctx())
	// 上限比总量小 1:一份都不给,不是给其中几份
	sn, err := e.svc.Snapshot(ctx(), l.Size-1)
	var tl *TooLargeError
	if !errors.As(err, &tl) || tl.Size != l.Size || tl.Max != l.Size-1 || len(sn.Sets) != 0 {
		t.Fatalf("超上限应回 *TooLargeError 且不给任何集合: %+v %v", sn, err)
	}
	if !strings.Contains(err.Error(), "超过同步上限") {
		t.Fatalf("错误信息要让人看得懂: %v", err)
	}
	// 刚好等于上限可以
	if _, err := e.svc.Snapshot(ctx(), l.Size); err != nil {
		t.Fatalf("等于上限不该报错: %v", err)
	}
}

func TestSnapshotOnlyEnabledCountsTowardLimit(t *testing.T) {
	e := newEnv(t)
	big := saveText(t, e, "大", "[Rule]\n"+strings.Repeat("DOMAIN,aaaaaaaaaaaaaaaaaaaa.com,REJECT\n", 200))
	small := saveText(t, e, "小", "[Rule]\nDOMAIN,s.com,REJECT\n")
	if _, err := e.svc.SetEnabled(ctx(), big, false); err != nil {
		t.Fatal(err)
	}
	l, _ := e.svc.List(ctx())
	sn, err := e.svc.Snapshot(ctx(), l.Size)
	if err != nil || len(sn.Sets) != 1 || sn.Sets[0].ID != small {
		t.Fatalf("停用的大集合不占上限: %+v %v", sn, err)
	}
}

func TestSnapshotDefaultLimit(t *testing.T) {
	e := newEnv(t)
	saveText(t, e, "甲", "[Rule]\nDOMAIN,a.com,REJECT\n")
	// limit <= 0 走默认上限(4 MiB),小数据当然通过
	for _, lim := range []int{0, -1} {
		if _, err := e.svc.Snapshot(ctx(), lim); err != nil {
			t.Fatalf("limit=%d: %v", lim, err)
		}
	}
	if MaxSnapshotSize != 4<<20 {
		t.Fatalf("默认上限应是 4 MiB,现在是 %d", MaxSnapshotSize)
	}
}

func TestSnapshotMissingTextFileReadsAsEmpty(t *testing.T) {
	// 文本文件丢了(例如手动清过目录)与摘要口径一致:读成空文本,不报错、不 panic
	e := newEnv(t)
	id := saveText(t, e, "甲", "[Rule]\nDOMAIN,a.com,REJECT\n")
	if err := removeText(e, id); err != nil {
		t.Fatal(err)
	}
	e.svc.cache = nil
	sn, err := e.svc.Snapshot(ctx(), 0)
	if err != nil || len(sn.Sets) != 1 || sn.Sets[0].Text != "" {
		t.Fatalf("文本文件不在时应读成空: %+v %v", sn, err)
	}
}

func removeText(e *env, id string) error { return os.Remove(e.svc.textPath(id)) }
