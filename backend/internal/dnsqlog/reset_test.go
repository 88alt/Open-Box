package dnsqlog

import (
	"testing"
	"time"
)

func TestResetDropsEverything(t *testing.T) {
	l := New()
	feed(l, t0, "query[A] a.example.com from 192.168.1.5")
	feed(l, t0.Add(10*time.Millisecond), "reply a.example.com is 1.2.3.4")
	feed(l, t0.Add(time.Second), "query[A] pending.example.com from 192.168.1.5") // 还没配上应答
	if l.Records(Query{}).Total == 0 {
		t.Fatal("前提:应已有记录")
	}

	l.Reset()
	if p := l.Records(Query{}); p.Total != 0 || len(p.Rows) != 0 {
		t.Errorf("记录应清空: %+v", p)
	}
	if s := l.Summarize(t0); s.Queries != 0 || len(s.TopDomains) != 0 {
		t.Errorf("统计应清空: %+v", s)
	}
	// 「有改动」标记也清了:后台落盘不会把旧内容写回去
	if _, ok := l.Snapshot(); ok {
		t.Error("重置后没有新内容,不该有要落盘的快照")
	}
	// 没配上应答的查询也丢了:迟到的应答不会凭空生出一条记录
	feed(l, t0.Add(2*time.Second), "reply pending.example.com is 5.6.7.8")
	if p := l.Records(Query{}); p.Total != 0 {
		t.Errorf("迟到的应答不该生成记录: %+v", p)
	}

	// 之后新读到的日志行照常记,编号从头开始
	feed(l, t0.Add(3*time.Second), "query[A] b.example.com from 192.168.1.6")
	feed(l, t0.Add(3*time.Second+5*time.Millisecond), "reply b.example.com is 9.9.9.9")
	p := l.Records(Query{})
	if p.Total != 1 || p.Rows[0].Domain != "b.example.com" {
		t.Fatalf("重置后应能继续记: %+v", p)
	}
	if _, ok := l.Snapshot(); !ok {
		t.Error("新记录应带「有改动」标记")
	}
}

func TestResetOnEmpty(t *testing.T) {
	l := New()
	l.Reset()
	if p := l.Records(Query{}); p.Total != 0 {
		t.Fatalf("%+v", p)
	}
}
