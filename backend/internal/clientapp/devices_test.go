package clientapp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func TestPairAndConfigByTokenRoundTrip(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	if _, err := s.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	is, err := s.ClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := s.PairGet(ctx, is.RouterID)
	if err != nil || nonce != is.Nonce {
		t.Fatalf("PairGet: %q %v", nonce, err)
	}
	if _, err := s.PairGet(ctx, "nope"); !errors.Is(err, ErrPairUnpaired) {
		t.Fatalf("wrong router: %v", err)
	}
	key, err := pairDeriveKey(is.PairCode, is.RouterID, is.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("openbox-pair-v1:" + is.Nonce))
	proof := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	sealed, err := s.PairPost(ctx, is.RouterID, is.Nonce, proof)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < 28 {
		t.Fatalf("sealed: %v len=%d", err, len(raw))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := g.Open(nil, raw[:12], raw[12:], nil)
	if err != nil {
		t.Fatal(err)
	}
	var cred struct {
		Token string `json:"token"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(plain, &cred); err != nil || cred.Token == "" || cred.Key == "" {
		t.Fatalf("%s %v", plain, err)
	}
	bundle, err := s.ConfigByToken(ctx, is.RouterID, cred.Token, "1.2.3.4")
	if err != nil || bundle.V != 1 || bundle.IV == "" || bundle.Data == "" {
		t.Fatalf("%+v %v", bundle, err)
	}
	badTok := cred.Token
	if badTok[0] == '0' {
		badTok = "1" + badTok[1:]
	} else {
		badTok = "0" + badTok[1:]
	}
	if _, err := s.ConfigByToken(ctx, is.RouterID, badTok, "1.2.3.4"); !errors.Is(err, ErrPairUnpaired) {
		t.Fatalf("bad token: %v", err)
	}
}

func TestPairBadProof(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	s.SetClientConfigEnabled(ctx, true)
	is, _ := s.ClientConfig(ctx)
	_, err := s.PairPost(ctx, is.RouterID, is.Nonce, "not-a-proof")
	var bad *PairBadCodeError
	if !errors.As(err, &bad) {
		t.Fatalf("%v", err)
	}
}

func TestPairDeriveMatchesUpstreamParams(t *testing.T) {
	key, err := pairDeriveKey("ABCD", "routeridrouteridrouteridrouterid", "00112233445566778899aabb")
	if err != nil {
		t.Fatal(err)
	}
	want, err := pbkdf2.Key(sha256.New, "ABCD", []byte("openbox-pair:routeridrouteridrouteridrouterid:00112233445566778899aabb"), 200000, 32)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != string(want) {
		t.Fatal("derive mismatch")
	}
}
