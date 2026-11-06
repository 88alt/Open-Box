package clientapp

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// App 分享配对路径(对齐上游 v0.1.402):
// GET/POST /client/v1/:routerId/share/:serverId/pair
// 与公开取回 GET /client/v1/share?sid=&g= 并存。

func shareDeriveKey(pairCode, routerID, serverID string) ([]byte, error) {
	salt := []byte(fmt.Sprintf("openbox-share:%s:%s", routerID, serverID))
	return pbkdf2.Key(sha256.New, pairCode, salt, DefaultSealIter, sealKeyLen)
}

func shareProof(key []byte, nonce string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("openbox-pair-v1:" + nonce))
	return []byte(base64.RawURLEncoding.EncodeToString(m.Sum(nil)))
}

// SharePairGet:GET pair → {nonce}(nonce 就是当前代数 gen)。
func (s *Service) SharePairGet(ctx context.Context, routerID, serverID string) (nonce string, err error) {
	is, err := s.ClientConfig(ctx)
	if err != nil {
		return "", err
	}
	if routerID != is.RouterID {
		return "", ErrPairUnpaired
	}
	if !validShareID(serverID) {
		return "", ErrPairUnpaired
	}
	have, err := s.profileServerIDs(ctx)
	if err != nil {
		return "", err
	}
	if !have[serverID] {
		return "", ErrPairUnpaired
	}
	var st state
	if _, err := s.Store.Load(stateFile, &st); err != nil {
		return "", err
	}
	sh := st.Shares[serverID]
	if sh == nil || !validPairCode(sh.PairCode) || !validGen(sh.Gen) {
		return "", ErrPairUnpaired
	}
	return sh.Gen, nil
}

// SharePairPost:POST pair {nonce,proof} → {sealed}(内含 link+gen)。
func (s *Service) SharePairPost(ctx context.Context, routerID, serverID, nonce, proof string) (sealed string, err error) {
	now := s.now()
	if s.sharePairLimit == nil {
		s.sharePairLimit = &pairLimiter{}
	}
	if d := s.sharePairLimit.locked(now); d > 0 {
		return "", &PairLockedError{After: d}
	}
	is, err := s.ClientConfig(ctx)
	if err != nil {
		return "", err
	}
	if routerID != is.RouterID {
		return "", ErrPairUnpaired
	}
	if !validShareID(serverID) {
		return "", ErrPairUnpaired
	}
	have, err := s.profileServerIDs(ctx)
	if err != nil {
		return "", err
	}
	if !have[serverID] {
		return "", ErrPairUnpaired
	}
	var st state
	if _, err := s.Store.Load(stateFile, &st); err != nil {
		return "", err
	}
	sh := st.Shares[serverID]
	if sh == nil || !validPairCode(sh.PairCode) || !validGen(sh.Gen) || sh.Seal == nil {
		return "", ErrPairUnpaired
	}
	if nonce != sh.Gen || !validGen(nonce) {
		return "", ErrPairExpired
	}
	key, err := shareDeriveKey(sh.PairCode, is.RouterID, serverID)
	if err != nil {
		return "", err
	}
	want := shareProof(key, sh.Gen)
	got := []byte(proof)
	if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		rem := s.sharePairLimit.fail(now)
		return "", &PairBadCodeError{Remaining: rem}
	}
	s.sharePairLimit.reset()

	// 解开已存封包取 link
	plain, err := OpenShare(*sh.Seal, sh.PairCode, is.RouterID, serverID, sh.Gen, s.kdfIter(), s.kdfIter())
	if err != nil {
		return "", ErrPairUnpaired
	}
	var sp sharePlain
	if json.Unmarshal(plain, &sp) != nil || sp.Link == "" {
		return "", ErrPairUnpaired
	}
	out, err := json.Marshal(map[string]string{"link": sp.Link, "gen": sh.Gen})
	if err != nil {
		return "", err
	}
	return sealBytes(key, out, s.Rand)
}
