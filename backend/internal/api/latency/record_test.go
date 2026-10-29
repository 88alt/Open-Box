package latency

import (
	"context"
	"errors"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
)

type fakeHist struct {
	got  []latencyhistory.Item
	hist latencyhistory.History
	err  error
}

func (f *fakeHist) Record(_ context.Context, items []latencyhistory.Item) (latencyhistory.History, error) {
	f.got = append(f.got, items...)
	return f.hist, f.err
}

func ptr(b bool) *bool { return &b }

func TestRecordMapsResultsToSamples(t *testing.T) {
	f := &fakeHist{hist: latencyhistory.History{"a": nil}}
	h := &handlers{Deps{History: f}}
	jobs := []storedJob{
		{Tag: "通"}, {Tag: "快到 0 毫秒"}, {Tag: "超时"}, {Tag: "被拒"}, {Tag: "失败"},
		{Tag: "不存在"}, {Tag: "无效"}, {Tag: "不记", Record: ptr(false)}, {Tag: "显式要记", Record: ptr(true)},
	}
	res := []probe.Result{
		{OK: true, Ms: 120}, {OK: true, Ms: 0}, {Reason: "timeout"}, {Reason: "refused"}, {Reason: "failed"},
		{Reason: "not-found"}, {Reason: "invalid"}, {OK: true, Ms: 50}, {OK: true, Ms: 70},
	}
	if hist := h.record(context.Background(), jobs, res); hist == nil {
		t.Fatal("有记录应返回历史")
	}
	want := map[string]int{"通": 120, "快到 0 毫秒": 1, "超时": 0, "被拒": 0, "失败": 0, "显式要记": 70}
	if len(f.got) != len(want) {
		t.Fatalf("记了 %d 条,应为 %d: %+v", len(f.got), len(want), f.got)
	}
	for _, it := range f.got {
		d, ok := want[it.Name]
		if !ok || it.Sample.Delay != d || it.Sample.Node != "" || it.Sample.Time == "" {
			t.Errorf("样本不对: %+v", it)
		}
	}
	for _, it := range f.got {
		if it.Name == "不存在" || it.Name == "无效" || it.Name == "不记" {
			t.Errorf("%s 不该记", it.Name)
		}
	}
}

func TestRecordSkipsWhenNothingToRecord(t *testing.T) {
	f := &fakeHist{hist: latencyhistory.History{}}
	h := &handlers{Deps{History: f}}
	ctx := context.Background()
	if h.record(ctx, nil, nil) != nil {
		t.Error("预览(没有已保存的 jobs)不记")
	}
	if h.record(ctx, []storedJob{{Tag: "a", Record: ptr(false)}}, []probe.Result{{OK: true, Ms: 5}}) != nil || len(f.got) != 0 {
		t.Error("全都 record=false 不记、不带 history")
	}
	if h.record(ctx, []storedJob{{Tag: "a"}}, []probe.Result{{Reason: "not-found"}}) != nil || len(f.got) != 0 {
		t.Error("全是没测的不记")
	}
	if h.record(ctx, []storedJob{{Tag: "a"}, {Tag: "b"}}, []probe.Result{{OK: true, Ms: 5}}) != nil {
		t.Error("结果数对不上不记(防御)")
	}
	if (&handlers{}).record(ctx, []storedJob{{Tag: "a"}}, []probe.Result{{OK: true, Ms: 5}}) != nil {
		t.Error("没接历史不记")
	}
}

func TestRecordFailureDoesNotBreakTest(t *testing.T) {
	h := &handlers{Deps{History: &fakeHist{err: errors.New("磁盘满了")}}}
	if got := h.record(context.Background(), []storedJob{{Tag: "a"}}, []probe.Result{{OK: true, Ms: 5}}); got != nil {
		t.Errorf("写历史失败应不带 history、测速结果照常: %v", got)
	}
	// 写成功但历史是 nil(没内容)要回空表而不是 nil,前端才会替换
	h = &handlers{Deps{History: &fakeHist{}}}
	if got := h.record(context.Background(), []storedJob{{Tag: "a"}}, []probe.Result{{OK: true, Ms: 5}}); got == nil || len(got) != 0 {
		t.Errorf("应回空表: %v", got)
	}
}
