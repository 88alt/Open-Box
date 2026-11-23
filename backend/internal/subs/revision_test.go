package subs

import (
	"context"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

func newRevSvc(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, func(context.Context, string, string) (*sub.Fetched, error) { return nil, &sub.FetchError{Msg: "no"} })
}

// revision 要把节点也算进去:只有节点变、订阅元数据没变时 revision 也得变(nodesRevision 变则 revision 必变)。
func TestRevisionCoversNodesOnlyChange(t *testing.T) {
	s := newRevSvc(t)
	recs := []Record{{ID: "a", Name: "A", Enabled: true}}
	if err := s.persist(recs, map[string][]sub.Node{"a": mk("HK-1")}); err != nil {
		t.Fatal(err)
	}
	r0, err := s.Revisions()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.persist(recs, map[string][]sub.Node{"a": mk("HK-1", "US-1")}); err != nil {
		t.Fatal(err)
	}
	r1, _ := s.Revisions()
	if r1.NodesRevision == r0.NodesRevision || r1.Revision == r0.Revision {
		t.Errorf("只改节点两个版本都该变: %v → %v", r0, r1)
	}
	// 孤儿节点(没有订阅引用)不算变化;空订阅和没有订阅同一个版本
	if err := s.st.Save(fileNodes, nodesFile{Nodes: map[string][]sub.Node{"a": mk("HK-1", "US-1"), "ghost": mk("X")}}); err != nil {
		t.Fatal(err)
	}
	if r2, _ := s.Revisions(); r2 != r1 {
		t.Errorf("孤儿节点不该改版本: %v → %v", r1, r2)
	}
}

func TestRevisionStableAcrossServiceInstances(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	fetch := func(context.Context, string, string) (*sub.Fetched, error) { return nil, nil }
	a := New(st, fetch)
	if err := a.persist([]Record{{ID: "a", Name: "A"}}, map[string][]sub.Node{"a": mk("N1")}); err != nil {
		t.Fatal(err)
	}
	ra, _ := a.Revisions()
	st2, _ := store.Open(dir)
	rb, _ := New(st2, fetch).Revisions() // 后端重启后同样的数据还是同一个版本
	if ra != rb {
		t.Errorf("重启后版本变了: %v %v", ra, rb)
	}
}

func TestWaitRevisionWakesOnPersistAndDelete(t *testing.T) {
	s := newRevSvc(t)
	if err := s.persist([]Record{{ID: "a", Name: "A", Enabled: true}}, map[string][]sub.Node{"a": mk("N1")}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(){
		"persist": func() {
			_ = s.persist([]Record{{ID: "a", Name: "A2", Enabled: true}}, map[string][]sub.Node{"a": mk("N1")})
		},
		"delete": func() { _, _ = s.Delete(context.Background(), "a") },
	} {
		cur, _ := s.Revisions()
		done := make(chan Revision, 1)
		go func() {
			r, _ := s.WaitRevision(context.Background(), cur.Revision, 5*time.Second)
			done <- r
		}()
		time.Sleep(80 * time.Millisecond)
		change()
		select {
		case r := <-done:
			if r.Revision == cur.Revision {
				t.Errorf("%s: 醒了但版本没变", name)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("%s: 写盘后长轮询没醒", name)
		}
	}
}
