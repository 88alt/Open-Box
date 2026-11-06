package contentfilter

import (
	"errors"
	"strings"
	"testing"
)

func ruleText(host string) string {
	return "[URL Rewrite]\n^https://" + host + " reject\n"
}

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
	if err != nil || sn.Sets == nil || len(sn.Sets) != 0 || sn.Size <= 0 || len(sn.SHA256) != 64 {
		t.Fatalf("没有集合时应是空数组、size 0、有摘要: %+v %v", sn, err)
	}
	l, _ := e.svc.List(ctx())
	if sn.SHA256 != l.SHA256 {
		t.Fatal("空快照的摘要应与列表一致")
	}
}

func TestSnapshotEnabledOnlySorted(t *testing.T) {
	e := newEnv(t)
	idB := saveText(t, e, "乙", ruleText("b.com"))
	idA := saveText(t, e, "甲", ruleText("a.com"))
	idC := saveText(t, e, "丙", ruleText("c.com"))
	if _, err := e.svc.SetEnabled(ctx(), idC, false); err != nil {
		t.Fatal(err)
	}
	sn, err := e.svc.Snapshot(ctx(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sn.Sets) != 2 || sn.Sets[0].ID != idB || sn.Sets[1].ID != idA {
		t.Fatalf("应只含启用的两份、按 id 排序: %+v", sn.Sets)
	}
	if sn.Version != 1 || len(sn.Rules) < 2 {
		t.Fatalf("文档应含规则: %+v", sn)
	}
	l, _ := e.svc.List(ctx())
	if sn.SHA256 != l.SHA256 || sn.Size != l.Size {
		t.Fatalf("sha256 / size 应与列表同一口径: %s/%d vs %s/%d", sn.SHA256, sn.Size, l.SHA256, l.Size)
	}
}

func TestSnapshotTracksChanges(t *testing.T) {
	e := newEnv(t)
	id := saveText(t, e, "甲", ruleText("a.com"))
	s1, _ := e.svc.Snapshot(ctx(), 0)
	txt := ruleText("a.com") + ruleText("z.com")
	// two URL rewrite lines in one section
	txt = "[URL Rewrite]\n^https://a\\.com reject\n^https://z\\.com reject\n"
	if _, err := e.svc.Save(ctx(), SaveInput{ID: id, Name: "甲", Dialect: "surge", Text: &txt}); err != nil {
		t.Fatal(err)
	}
	s2, err := e.svc.Snapshot(ctx(), 0)
	if err != nil || s2.SHA256 == s1.SHA256 || len(s2.Rules) < 2 {
		t.Fatalf("改了文本,快照和摘要应跟着变: rules=%d %v", len(s2.Rules), err)
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
	saveText(t, e, "甲", ruleText("a.com"))
	saveText(t, e, "乙", ruleText("b.com"))
	l, _ := e.svc.List(ctx())
	sn, err := e.svc.Snapshot(ctx(), l.Size-1)
	var tl *TooLargeError
	if !errors.As(err, &tl) || tl.Size != l.Size || tl.Max != l.Size-1 {
		t.Fatalf("超上限应回 *TooLargeError: %+v %v", sn, err)
	}
	if !strings.Contains(err.Error(), "超过同步上限") {
		t.Fatalf("错误信息要让人看得懂: %v", err)
	}
	if _, err := e.svc.Snapshot(ctx(), l.Size); err != nil {
		t.Fatalf("等于上限不该报错: %v", err)
	}
}

func TestSnapshotOnlyEnabledCountsTowardLimit(t *testing.T) {
	e := newEnv(t)
	big := saveText(t, e, "大", "[URL Rewrite]\n"+strings.Repeat("^https://aaaaaaaaaaaaaaaaaaaa.com/"+strings.Repeat("x", 20)+" reject\n", 50))
	small := saveText(t, e, "小", ruleText("s.com"))
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
	saveText(t, e, "甲", ruleText("a.com"))
	for _, lim := range []int{0, -1} {
		if _, err := e.svc.Snapshot(ctx(), lim); err != nil {
			t.Fatalf("limit=%d: %v", lim, err)
		}
	}
	if MaxSnapshotSize != 2<<20 {
		t.Fatalf("默认上限应是 2 MiB,现在是 %d", MaxSnapshotSize)
	}
}
