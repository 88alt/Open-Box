package subs

import (
	"context"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

func TestDumpReturnsRecordsInOrderWithOnlyReferencedNodes(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil)
	if recs, nodes, err := s.Dump(context.Background()); err != nil || len(recs) != 0 || len(nodes) != 0 || recs == nil {
		t.Fatalf("空的时候要是空数组:%v %v %v", recs, nodes, err)
	}
	node := func(tag string) sub.Node {
		return sub.Node{Tag: tag, Type: "trojan", Server: "a.test", ServerPort: 443, Fields: map[string]any{"password": "x"}}
	}
	if err := st.Save(fileSubs, subsFile{Subscriptions: []Record{{ID: "b", Name: "B", Enabled: true}, {ID: "a", Name: "A"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(fileNodes, nodesFile{Nodes: map[string][]sub.Node{"a": {node("n1"), node("n2")}, "b": {node("n3")}, "gone": {node("orphan")}}}); err != nil {
		t.Fatal(err)
	}
	recs, nodes, err := s.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].ID != "b" || recs[1].ID != "a" {
		t.Fatalf("应保持用户排序:%+v", recs)
	}
	if len(nodes["a"]) != 2 || len(nodes["b"]) != 1 {
		t.Fatalf("%v", nodes)
	}
	if _, ok := nodes["gone"]; ok {
		t.Fatal("没有订阅引用的节点不该带上")
	}
}
