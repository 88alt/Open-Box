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
	"testing"
)

func TestSharePairRoundTrip(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	out, err := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	if err != nil {
		t.Fatal(err)
	}
	seal := out["a"]
	if seal.PairCode == "" {
		t.Fatal("no seal")
	}
	is, _ := e.s.ClientConfig(ctx)
	nonce, err := e.s.SharePairGet(ctx, is.RouterID, "a")
	if err != nil || nonce != seal.Gen {
		t.Fatalf("%q %v", nonce, err)
	}
	if _, err := e.s.SharePairGet(ctx, "nope", "a"); !errors.Is(err, ErrPairUnpaired) {
		t.Fatal(err)
	}
	key, err := shareDeriveKey(seal.PairCode, is.RouterID, "a")
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("openbox-pair-v1:" + seal.Gen))
	proof := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	sealed, err := e.s.SharePairPost(ctx, is.RouterID, "a", seal.Gen, proof)
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
	var got struct {
		Link string `json:"link"`
		Gen  string `json:"gen"`
	}
	if err := json.Unmarshal(plain, &got); err != nil || got.Link != linkA || got.Gen != seal.Gen {
		t.Fatalf("%s %v", plain, err)
	}
	if _, err := e.s.SharePairPost(ctx, is.RouterID, "a", seal.Gen, "bad"); !errors.As(err, new(*PairBadCodeError)) {
		t.Fatalf("%v", err)
	}
}
