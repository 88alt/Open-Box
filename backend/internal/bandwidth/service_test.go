package bandwidth

import (
	"context"
	"testing"
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
