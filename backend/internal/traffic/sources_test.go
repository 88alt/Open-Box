package traffic

import (
	"reflect"
	"testing"
)

// RecentSources 取最近几天出现过的来源地址,去重、排序、跳过「未知」。
func TestRecentSources(t *testing.T) {
	s, _, _ := newStore(t, at("2026-10-28", 12, 0))
	seed(t, s)

	got := s.RecentSources(1)
	want := []string{"192.168.1.5", "192.168.1.6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("当天来源:%v,想要 %v", got, want)
	}

	// 往前多看几天也还是这两个(别的天没有数据),不会重复
	got = s.RecentSources(7)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("近 7 天来源:%v,想要 %v", got, want)
	}

	// days<=0 按 1 天处理(仍是当天,所以还是这两个)
	if got := s.RecentSources(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("days=0 应按 1 天处理:%v", got)
	}

	// 负数不崩(按 1 天处理,仍是当天)
	if got := s.RecentSources(-5); !reflect.DeepEqual(got, want) {
		t.Fatalf("负数应按 1 天处理:%v", got)
	}
}

// 「未知」不是真终端,不能进已知终端列表,否则用户会看到一个叫「未知」的可选来源。
func TestRecentSourcesSkipsUnknown(t *testing.T) {
	s, _, _ := newStore(t, at("2026-10-29", 9, 0))
	if err := s.Record(at("2026-10-29", 9, 0), Delta{Items: []Item{
		item(unknown, "x", "a.com", 1, 1, 1, false),
		item("192.168.1.9", "x", "a.com", 1, 1, 1, false),
	}}); err != nil {
		t.Fatal(err)
	}
	got := s.RecentSources(1)
	if !reflect.DeepEqual(got, []string{"192.168.1.9"}) {
		t.Fatalf("「未知」应被跳过:%v", got)
	}
}
