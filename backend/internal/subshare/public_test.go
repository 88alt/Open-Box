package subshare

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

func TestPublicServeBase64AndSingBox(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st, NewToken: func() string { return "abcdefghijklmnopqrstuvwxyz012345" }}
	name, ids := "s", []string{"sub1"}
	sh, err := svc.Create(context.Background(), Input{Name: &name, SubscriptionIDs: &ids})
	if err != nil {
		t.Fatal(err)
	}
	p := &Public{
		Shares: svc,
		Nodes: func(ctx context.Context, ids []string) ([]sub.Node, *sub.Usage, error) {
			return []sub.Node{{
				Tag: "n1", Type: "shadowsocks", Server: "1.2.3.4", ServerPort: 8388,
				Fields: map[string]any{"method": "aes-128-gcm", "password": "pw"},
			}}, nil, nil
		},
	}
	res, err := p.Serve(context.Background(), sh.Token, "base64", "10.0.0.1")
	if err != nil || len(res.Body) == 0 {
		t.Fatalf("%v %v", res, err)
	}
	res2, err := p.Serve(context.Background(), sh.Token, "singbox", "10.0.0.1")
	if err != nil || res2.ContentType == "" {
		t.Fatal(err)
	}
	if _, err := p.Serve(context.Background(), "bad-token-bad-token-bad-token-bad", "base64", "10.0.0.1"); err != ErrPublicNotFound {
		t.Fatalf("%v", err)
	}
}
