package bandwidth

import (
	"context"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func TestNormalizeSettings(t *testing.T) {
	s, err := NormalizeSettings(map[string]any{"intervalHours": float64(12), "maxDownloadMB": float64(100)})
	if err != nil || s.IntervalHours != 12 || s.MaxDownloadMB != 100 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := NormalizeSettings(map[string]any{"intervalHours": float64(0)}); err == nil {
		t.Fatal("expected error")
	}
}

func TestQueue(t *testing.T) {
	svc := &Service{
		Nodes: func(ctx context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "a", SubscriptionID: "s1", Enabled: true}}, nil
		},
	}
	st, err := svc.Status(context.Background())
	if err != nil || st.Nodes != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	q, err := svc.QueueSubscription(context.Background(), "s1")
	if err != nil || q.Queued != 1 {
		t.Fatalf("%+v %v", q, err)
	}
	st, _ = svc.Status(context.Background())
	if st.PendingBySubscription["s1"] != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestRunOnceWithMeasurer(t *testing.T) {
	svc := &Service{
		Nodes: func(ctx context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "n1", SubscriptionID: "s1"}}, nil
		},
		Measure: func(ctx context.Context, node string, maxBytes int64) MeasureResult {
			return MeasureResult{Mbps: 12.5}
		},
	}
	_, _ = svc.QueueNode(context.Background(), "n1")
	svc.RunOnce(context.Background())
	st, _ := svc.Status(context.Background())
	m := st.Measurements["n1"]
	if m.Pending || m.Mbps != 12.5 || m.CompletedAt == 0 {
		t.Fatalf("%+v", m)
	}
}

func TestEnqueueDueAndPersist(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Store: st,
		Profile: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"bandwidth": map[string]any{
					"intervalHours": float64(1),
					"maxDownloadMB": float64(10),
				},
			}, nil
		},
		Nodes: func(ctx context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "n1", SubscriptionID: "s1", Enabled: true}}, nil
		},
		Measure: func(ctx context.Context, node string, maxBytes int64) MeasureResult {
			return MeasureResult{Mbps: 1}
		},
	}
	n, err := svc.EnqueueDue(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	svc.RunOnce(context.Background())
	svc2 := &Service{Store: st}
	svc2.LoadHistory()
	svc2.mu.Lock()
	m := svc2.measures["n1"]
	svc2.mu.Unlock()
	if m.Mbps != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestProbeMeasurerNotFound(t *testing.T) {
	m := NewProbeMeasurer(
		MaterialFunc(func(ctx context.Context, tags []string) (*probe.Material, error) {
			return probe.Subset(map[string]any{"outbounds": []any{}}, tags), nil
		}),
		RunnerFunc(func(ctx context.Context, m *probe.Material, jobs []probe.Job, opt probe.Options) ([]probe.Result, error) {
			return []probe.Result{{Error: "找不到这个节点", Reason: "not-found"}}, nil
		}),
		nil,
	)
	r := m(context.Background(), "missing", 1024)
	if !r.Failed || r.Reason != "not-found" {
		t.Fatalf("%+v", r)
	}
}
