package clientapp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/contentfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func TestTokenContentFilterMetaAndDoc(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cf := &contentfilter.Service{Store: st, Dir: filepath.Join(root, "cf")}
	txt := "[URL Rewrite]\n^https://a\\.com/ reject\n"
	if _, err := cf.Save(context.Background(), contentfilter.SaveInput{Name: "甲", Dialect: "surge", Text: &txt}); err != nil {
		t.Fatal(err)
	}
	s := fileSvc(t)
	s.ContentFilter = func(ctx context.Context) (contentfilter.Snapshot, error) { return cf.Snapshot(ctx, 0) }
	s.ContentFilterBuilt = func(ctx context.Context) (*contentfilter.Built, error) { return cf.BuiltSnapshot(ctx, 0) }
	ctx := context.Background()
	if _, err := s.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(ctx)
	key, _ := pairDeriveKey(is.PairCode, is.RouterID, is.Nonce)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("openbox-pair-v1:" + is.Nonce))
	proof := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	sealed, err := s.PairPost(ctx, is.RouterID, is.Nonce, proof)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(sealed)
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	plain, err := g.Open(nil, raw[:12], raw[12:], nil)
	if err != nil {
		t.Fatal(err)
	}
	var cred struct{ Token, Key string }
	json.Unmarshal(plain, &cred)

	meta, err := s.TokenContentFilterMeta(ctx, is.RouterID, cred.Token)
	if err != nil || meta.V != 1 {
		t.Fatalf("%+v %v", meta, err)
	}
	built, _ := cf.BuiltSnapshot(ctx, 0)
	doc, err := s.TokenContentFilterDoc(ctx, is.RouterID, cred.Token, built.Metadata.SHA256)
	if err != nil || doc.Data == "" {
		t.Fatalf("%+v %v", doc, err)
	}
	if _, err := s.TokenContentFilterDoc(ctx, is.RouterID, cred.Token, "deadbeef"); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("hash mismatch: %v", err)
	}
}
