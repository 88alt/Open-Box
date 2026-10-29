package dnsqlog

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 10, 15, 0, 0, time.UTC)

func feed(l *Log, at time.Time, msg string) { l.Feed("Oct 28 10:15:00 dnsmasq[1]: "+msg, at) }

func TestQueryAnswerCorrelation(t *testing.T) {
	l := New()
	l.Classify = func(d string) (bool, string) { return d == "ads.example.com", "AdList" }
	feed(l, t0, "query[A] ok.example.com from 192.168.1.5")
	feed(l, t0.Add(20*time.Millisecond), "forwarded ok.example.com to 127.0.0.1")
	feed(l, t0.Add(48*time.Millisecond), "reply ok.example.com is 1.2.3.4")
	feed(l, t0.Add(time.Second), "query[A] ads.example.com from 192.168.1.5")
	feed(l, t0.Add(time.Second+10*time.Millisecond), "reply ads.example.com is NXDOMAIN")
	feed(l, t0.Add(2*time.Second), "query[A] nope.example.com from 192.168.1.6")
	feed(l, t0.Add(2*time.Second+5*time.Millisecond), "reply nope.example.com is NXDOMAIN")
	feed(l, t0.Add(3*time.Second), "query[A] ok.example.com from 192.168.1.5")
	feed(l, t0.Add(3*time.Second), "cached ok.example.com is 1.2.3.4")
	feed(l, t0.Add(4*time.Second), "query[A] router.lan from 192.168.1.5")
	feed(l, t0.Add(4*time.Second), "config router.lan is 192.168.1.1")
	feed(l, t0.Add(5*time.Second), "query[A] bad.example.com from 192.168.1.5")
	feed(l, t0.Add(5*time.Second), "reply bad.example.com is SERVFAIL")
	feed(l, t0.Add(6*time.Second), "query[A] lost.example.com from 192.168.1.5")

	p := l.Records(Query{})
	if p.Total != 7 || p.Page != 1 || p.PageSize != 20 {
		t.Fatalf("分页信息不对: %+v", p)
	}
	got := map[string]Record{}
	for _, r := range p.Rows {
		got[fmt.Sprintf("%s/%s", r.Domain, r.Result)] = r
	}
	for _, k := range []string{"ok.example.com/allowed", "ads.example.com/blocked", "nope.example.com/allowed", "ok.example.com/cached", "router.lan/local", "bad.example.com/error", "lost.example.com/unknown"} {
		if _, ok := got[k]; !ok {
			t.Errorf("缺少记录 %s;现有 %v", k, got)
		}
	}
	if r := got["ads.example.com/blocked"]; r.List != "AdList" || r.Via != "dnsmasq" || r.Source != "192.168.1.5" || r.QType != "A" {
		t.Errorf("被拦记录: %+v", r)
	}
	if r := got["ok.example.com/allowed"]; r.Elapsed == nil || *r.Elapsed != 48 {
		t.Errorf("上游应答的耗时应是 48ms: %+v", r.Elapsed)
	}
	if r := got["ok.example.com/cached"]; r.Elapsed != nil {
		t.Errorf("缓存应答不记耗时: %v", *r.Elapsed)
	}
	if p.Rows[0].Domain != "lost.example.com" {
		t.Errorf("新的应在前: %s", p.Rows[0].Domain)
	}
}

func TestRecordFilters(t *testing.T) {
	l := New()
	l.Classify = func(d string) (bool, string) { return d == "ads.example.com", "AdList" }
	for _, c := range []struct{ d, src, ans string }{
		{"ads.example.com", "192.168.1.5", "NXDOMAIN"}, {"ok.example.com", "192.168.1.6", "1.2.3.4"}, {"a.org", "192.168.1.5", "5.6.7.8"},
	} {
		feed(l, t0, "query[A] "+c.d+" from "+c.src)
		feed(l, t0.Add(time.Millisecond), "reply "+c.d+" is "+c.ans)
	}
	feed(l, t0, "query[A] a.org from 192.168.1.5")
	feed(l, t0, "cached a.org is 5.6.7.8")
	cases := []struct {
		q    Query
		want int
	}{
		{Query{Result: "blocked"}, 1}, {Query{Result: "allowed"}, 2}, {Query{Result: "dnsmasq"}, 1}, {Query{Result: "error"}, 0},
		{Query{Search: "EXAMPLE"}, 2}, {Query{Search: "192.168.1.6"}, 1}, {Query{Search: "adlist"}, 1}, {Query{Search: "zzz"}, 0},
	}
	for _, c := range cases {
		if got := l.Records(c.q).Total; got != c.want {
			t.Errorf("%+v: total=%d want %d", c.q, got, c.want)
		}
	}
	p := l.Records(Query{Page: 99, PageSize: 2})
	if p.Page != 2 || len(p.Rows) != 2 || p.PageSize != 2 {
		t.Errorf("页码应收回到最后一页: %+v", p)
	}
	if p := l.Records(Query{PageSize: 1000}); p.PageSize != 100 {
		t.Errorf("每页上限 100: %d", p.PageSize)
	}
	if p := l.Records(Query{Page: -3}); p.Page != 1 {
		t.Errorf("页码下限 1: %d", p.Page)
	}
}

func TestRingKeepsNewest(t *testing.T) {
	l := New()
	for i := 0; i < MaxRecords+50; i++ {
		feed(l, t0, fmt.Sprintf("query[A] d%d.org from 1.1.1.1", i))
	}
	p := l.Records(Query{PageSize: 100})
	if p.Total != MaxRecords || p.Rows[0].Domain != fmt.Sprintf("d%d.org", MaxRecords+49) {
		t.Errorf("total=%d first=%s", p.Total, p.Rows[0].Domain)
	}
	// 统计不受记录条数上限影响
	if s := l.Summarize(t0); s.Queries != MaxRecords+50 {
		t.Errorf("统计的查询数应是全部: %d", s.Queries)
	}
}

func TestPendingExpires(t *testing.T) {
	l := New()
	feed(l, t0, "query[A] slow.org from 1.1.1.1")
	feed(l, t0.Add(pendingTTL+time.Second), "reply slow.org is 1.2.3.4") // 太晚,不再配给那条查询
	if r := l.Records(Query{}).Rows[0]; r.Result != ResultUnknown || r.Elapsed != nil {
		t.Errorf("超时的查询应保持 unknown: %+v", r)
	}
	l.mu.Lock()
	n := len(l.pending)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("过期的等待应被清掉: %d", n)
	}
}

func TestSummarize(t *testing.T) {
	l := New()
	l.Classify = func(d string) (bool, string) { return d == "ads.org", "L" }
	at := t0 // 10:15
	for i := 0; i < 3; i++ {
		feed(l, at, "query[A] ok.org from 1.1.1.1")
		feed(l, at.Add(40*time.Millisecond), "reply ok.org is 1.1.1.1")
	}
	feed(l, at, "query[A] ads.org from 1.1.1.1")
	feed(l, at.Add(10*time.Millisecond), "reply ads.org is NXDOMAIN")
	prev := at.Add(-3 * time.Hour) // 07:15
	feed(l, prev, "query[A] old.org from 1.1.1.1")
	feed(l, prev.Add(20*time.Millisecond), "reply old.org is 1.1.1.1")
	feed(l, at.Add(-30*time.Hour), "query[A] ancient.org from 1.1.1.1") // 超出 24 小时

	s := l.Summarize(at.Add(10 * time.Minute))
	if len(s.Hourly) != 24 {
		t.Fatalf("hourly 应恒为 24 项: %d", len(s.Hourly))
	}
	if s.Queries != 5 || s.Blocked != 1 {
		t.Errorf("queries=%d blocked=%d", s.Queries, s.Blocked)
	}
	last := s.Hourly[23]
	if last.Hour != time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC).UnixMilli() || last.Queries != 4 || last.Blocked != 1 || last.Timed != 4 {
		t.Errorf("最后一个桶: %+v", last)
	}
	if s.Hourly[20].Queries != 1 || s.Hourly[0].Queries != 0 {
		t.Errorf("早先的桶: %+v / %+v", s.Hourly[20], s.Hourly[0])
	}
	if s.AverageMs == nil || *s.AverageMs != (3*40+10+20)/5.0 {
		t.Errorf("平均耗时: %v", s.AverageMs)
	}
	if len(s.TopDomains) == 0 || s.TopDomains[0] != (Domain{"ok.org", 3}) {
		t.Errorf("热门域名: %+v", s.TopDomains)
	}
	for _, d := range s.TopDomains {
		if d.Domain == "ancient.org" {
			t.Errorf("24 小时之外的域名不应出现")
		}
	}
	if e := New().Summarize(at); e.AverageMs != nil || e.Queries != 0 || len(e.Hourly) != 24 || e.TopDomains == nil {
		t.Errorf("空统计: %+v", e)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	l := New()
	feed(l, t0, "query[A] a.org from 1.1.1.1")
	feed(l, t0.Add(time.Millisecond), "reply a.org is 1.1.1.1")
	s, ok := l.Snapshot()
	if !ok {
		t.Fatal("有变化应给出快照")
	}
	if _, ok := l.Snapshot(); ok {
		t.Error("没有新变化不应再给")
	}
	n := New()
	n.Restore(s)
	feed(n, t0, "query[A] b.org from 1.1.1.1")
	p := n.Records(Query{})
	if p.Total != 2 || p.Rows[0].ID <= p.Rows[1].ID {
		t.Errorf("恢复后 id 应接着往后: %+v", p.Rows)
	}
	if sm := n.Summarize(t0); sm.Queries != 2 {
		t.Errorf("恢复后的统计: %d", sm.Queries)
	}
	n.Restore(Snapshot{Version: 99, Records: []*Record{{Domain: "x"}}}) // 版本不认识就当没有
	if n.Records(Query{}).Total != 2 {
		t.Error("不认识的版本不应载入")
	}
}
