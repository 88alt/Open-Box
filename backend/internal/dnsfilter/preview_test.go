package dnsfilter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func sample(n int) []byte {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "||ad%d.example.com^\n", i)
	}
	b.WriteString("@@||ok.example.com^\n@@||ok2.example.com^$important\n##.x\n")
	return []byte(b.String())
}

func TestPreviewPagingFilteringSearch(t *testing.T) {
	calls := 0
	pv := NewPreviewer(func(context.Context, string) ([]byte, error) { calls++; return sample(45), nil })
	ctx := context.Background()

	r, err := pv.Preview(ctx, PreviewQuery{URL: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Count != 47 || r.Total != 47 || r.Page != 1 || r.PageSize != 20 || len(r.Rows) != 20 {
		t.Fatalf("%+v", r)
	}
	if r.RuleCount != 48 || r.Unsupported != 1 || r.Source != "url" || r.UpdatedAt != nil || r.UnsupportedExamples[0] != "##.x" {
		t.Fatalf("%+v", r)
	}
	if r.Rows[0].Value != "ad1.example.com" {
		t.Fatal("应保持名单里的先后顺序")
	}

	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Page: 3})
	if r.Page != 3 || len(r.Rows) != 7 {
		t.Fatalf("最后一页:%d 页 %d 条", r.Page, len(r.Rows))
	}
	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Page: 99}) // 越界收回到最后一页
	if r.Page != 3 {
		t.Fatalf("页 %d", r.Page)
	}
	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Page: -5, PageSize: 1000})
	if r.Page != 1 || r.PageSize != MaxPageSize {
		t.Fatalf("%d %d", r.Page, r.PageSize)
	}

	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Action: "allow"})
	if r.Total != 2 || r.Count != 47 || len(r.Rows) != 2 || r.Rows[1].Important != true {
		t.Fatalf("%+v", r)
	}
	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Action: "block", Search: " AD4 "})
	if r.Total != 7 || r.Rows[0].Value != "ad4.example.com" || r.Rows[1].Value != "ad40.example.com" {
		t.Fatalf("%+v", r.Rows)
	}
	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Search: "@@||ok2"}) // 搜规则原文
	if r.Total != 1 || r.Rows[0].Value != "ok2.example.com" {
		t.Fatalf("规则原文搜索:%+v", r)
	}
	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Search: "nomatch"})
	if r.Total != 0 || r.Page != 1 || len(r.Rows) != 0 || r.Rows == nil {
		t.Fatalf("没有匹配时 rows 要是空数组:%+v", r)
	}
	r, _ = pv.Preview(ctx, PreviewQuery{URL: "u", Action: "bogus"}) // 未知动作按全部
	if r.Total != 47 {
		t.Fatal(r.Total)
	}
	if calls != 1 {
		t.Fatalf("同一链接 60 秒内应只下载一次,实际 %d", calls)
	}
}

func TestPreviewCacheTTLAndEviction(t *testing.T) {
	calls := map[string]int{}
	pv := NewPreviewer(func(_ context.Context, u string) ([]byte, error) { calls[u]++; return sample(1), nil })
	now := time.Unix(1000, 0)
	pv.now = func() time.Time { return now }
	ctx := context.Background()
	pv.Preview(ctx, PreviewQuery{URL: "a"})
	pv.Preview(ctx, PreviewQuery{URL: "a"})
	if calls["a"] != 1 {
		t.Fatal(calls)
	}
	now = now.Add(61 * time.Second)
	pv.Preview(ctx, PreviewQuery{URL: "a"})
	if calls["a"] != 2 {
		t.Fatal("过期后应重新下载", calls)
	}
	// 三份各差 1 秒:同一时刻进缓存的两份并列「最旧」时,挤掉哪份取决于 map 遍历顺序,测试会偶发失败
	now = now.Add(time.Second)
	pv.Preview(ctx, PreviewQuery{URL: "b"})
	now = now.Add(time.Second)
	pv.Preview(ctx, PreviewQuery{URL: "c"}) // 超过缓存上限,挤掉最旧的 a
	if len(pv.cache) != previewCacheMax {
		t.Fatalf("缓存 %d 份", len(pv.cache))
	}
	if _, ok := pv.cache["a"]; ok {
		t.Fatal("最旧的应被挤掉")
	}
}

func TestPreviewErrorsAreNotCached(t *testing.T) {
	boom := errors.New("拉取失败")
	n := 0
	pv := NewPreviewer(func(context.Context, string) ([]byte, error) {
		n++
		if n == 1 {
			return nil, boom
		}
		return sample(1), nil
	})
	if _, err := pv.Preview(context.Background(), PreviewQuery{URL: "u"}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := pv.Preview(context.Background(), PreviewQuery{URL: "u"}); err != nil {
		t.Fatal("失败不该进缓存:", err)
	}
	pv2 := NewPreviewer(func(context.Context, string) ([]byte, error) { return []byte("! 空\n"), nil })
	if _, err := pv2.Preview(context.Background(), PreviewQuery{URL: "u"}); !errors.Is(err, ErrNoRules) {
		t.Fatal(err)
	}
	if len(pv2.cache) != 0 {
		t.Fatal("解析失败不该进缓存")
	}
}
