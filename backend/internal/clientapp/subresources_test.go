package clientapp

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pairedToken 走一遍配对,返回路由器 ID 和设备 token(配对开关已打开)。
func pairedToken(t *testing.T, s *Service) (routerID, token string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	is, err := s.ClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key, err := pairDeriveKey(is.PairCode, is.RouterID, is.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("openbox-pair-v1:" + is.Nonce))
	sealed, err := s.PairPost(ctx, is.RouterID, is.Nonce, base64.RawURLEncoding.EncodeToString(m.Sum(nil)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	plain, err := g.Open(nil, raw[:12], raw[12:], nil)
	if err != nil {
		t.Fatal(err)
	}
	var cred struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(plain, &cred); err != nil || cred.Token == "" {
		t.Fatalf("%s %v", plain, err)
	}
	return is.RouterID, cred.Token
}

// 设备校验的顺序(对齐上游):先认设备,认出来了才看开关。
// 开关关着时,不认得的 token 仍是 unpaired,只有有效 token 才看得到 disabled。
func TestAuthDeviceOrder(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	rid, tok := pairedToken(t, s)
	if _, _, err := s.authDevice(ctx, rid, tok); err != nil {
		t.Fatalf("有效设备: %v", err)
	}
	if _, err := s.SetClientConfigEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("0", tokenHexLen)
	cases := []struct {
		name          string
		router, token string
		want          error
	}{
		{"有效 token、开关关着", rid, tok, ErrPairDisabled},
		{"陌生 token、开关关着", rid, other, ErrPairUnpaired},
		{"格式不对的 token", rid, "xyz", ErrPairUnpaired},
		{"路由器 ID 不对", strings.Repeat("a", 32), tok, ErrPairUnpaired},
	}
	for _, c := range cases {
		if _, _, err := s.authDevice(ctx, c.router, c.token); !errors.Is(err, c.want) {
			t.Errorf("%s: 得到 %v,想要 %v", c.name, err, c.want)
		}
	}
}

func TestRuleSetPath(t *testing.T) {
	s := fileSvc(t)
	geo, lists := t.TempDir(), t.TempDir()
	s.GeoDir, s.RulesetDir = geo, lists
	for dir, name := range map[string]string{geo: "geosite-cn.srs", lists: "list-0123abcd.srs"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SRS"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(lists, "list-0123abcd-ip.srs"), []byte("SRS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(geo, "geoip-dir.srs"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 规则库目录里的一个文件,用来确认 list- 名字不会去规则库目录找
	if err := os.WriteFile(filepath.Join(geo, "list-0123abcd-ip.srs"), []byte("WRONG"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rid, tok := pairedToken(t, s)
	ok := map[string]string{
		"geosite-cn":        filepath.Join(geo, "geosite-cn.srs"),
		"geosite-cn.srs":    filepath.Join(geo, "geosite-cn.srs"),
		"list-0123abcd":     filepath.Join(lists, "list-0123abcd.srs"),
		"list-0123abcd-ip":  filepath.Join(lists, "list-0123abcd-ip.srs"),
		"list-0123abcd.srs": filepath.Join(lists, "list-0123abcd.srs"),
	}
	for tag, want := range ok {
		got, err := s.RuleSetPath(ctx, rid, tok, tag)
		if err != nil || got != want {
			t.Errorf("%s: %q %v,想要 %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{
		"geosite-nope", "geoip-dir", "..%2Fetc", "../etc/passwd", "geosite-../x", "list-0123ABCD", "list-0123abc",
		"obnode-direct", "", ".srs", "config.json", "geosite-cn.srs.srs",
	} {
		if _, err := s.RuleSetPath(ctx, rid, tok, tag); !errors.Is(err, ErrTokenNotFound) {
			t.Errorf("%q 应当找不到: %v", tag, err)
		}
	}
	// 目录没配:对应的一类找不到
	s.GeoDir = ""
	if _, err := s.RuleSetPath(ctx, rid, tok, "geosite-cn"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("GeoDir 为空: %v", err)
	}
	// 设备不认得、开关关着:不看 tag
	if _, err := s.RuleSetPath(ctx, rid, strings.Repeat("1", tokenHexLen), "list-0123abcd"); !errors.Is(err, ErrPairUnpaired) {
		t.Errorf("陌生设备: %v", err)
	}
	if _, err := s.SetClientConfigEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RuleSetPath(ctx, rid, tok, "list-0123abcd"); !errors.Is(err, ErrPairDisabled) {
		t.Errorf("开关关着: %v", err)
	}
}

func TestBackground(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	rid, tok := pairedToken(t, s)
	if _, err := s.Background(ctx, rid, tok); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("没设置背景图: %v", err)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	set := func(img string) {
		t.Helper()
		if err := s.Store.Save(backgroundFile, map[string]string{"image": img}); err != nil {
			t.Fatal(err)
		}
	}
	set("data:image/PNG;base64," + base64.StdEncoding.EncodeToString(png))
	img, err := s.Background(ctx, rid, tok)
	if err != nil || img.Type != "image/png" || !bytes.Equal(img.Bytes, png) || len(img.Version) != 16 {
		t.Fatalf("%+v %v", img, err)
	}
	sum := sha256.Sum256(png)
	if want := base64.StdEncoding.EncodeToString(sum[:8]); want == "" || img.Version == "" {
		t.Fatal("version")
	}
	// 同一张图版本不变,换图版本变
	again, _ := s.Background(ctx, rid, tok)
	set("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("other")))
	other, _ := s.Background(ctx, rid, tok)
	if again.Version != img.Version || other.Version == img.Version || other.Type != "image/jpeg" {
		t.Fatalf("版本: %s %s %s", img.Version, again.Version, other.Version)
	}
	for _, bad := range []string{
		"", "http://x/y.png", "data:text/html;base64,PGI+", "data:image/png,abc", "data:image/png;base64,@@@",
		"data:image/svg xml;base64,AAAA", "data:image/;base64,AAAA", "data:image/png;base64,",
	} {
		set(bad)
		if _, err := s.Background(ctx, rid, tok); !errors.Is(err, ErrTokenNotFound) {
			t.Errorf("%q 应当找不到: %v", bad, err)
		}
	}
	if _, err := s.Background(ctx, rid, strings.Repeat("2", tokenHexLen)); !errors.Is(err, ErrPairUnpaired) {
		t.Errorf("陌生设备: %v", err)
	}
}
