package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func TestResetReturnsToNotSetUp(t *testing.T) {
	m, clk, dir := newMgr(t)
	tok, err := m.Setup("secret-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.Login("secret-1", "c")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Authenticated(tok) || !m.Authenticated(other) {
		t.Fatal("重置前两个会话都应有效")
	}
	// 攒一个被锁定的来源:重置后限速计数也要清掉
	for i := 0; i < 20; i++ {
		m.lim.fail("locked-client")
	}
	if m.lim.blocked("locked-client") == 0 {
		t.Fatal("前提:该来源应已被锁定")
	}

	if err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if m.PasswordSet() {
		t.Error("重置后应是未设置")
	}
	if m.Authenticated(tok) || m.Authenticated(other) {
		t.Error("所有会话都应失效")
	}
	if _, err := m.Login("secret-1", "c"); !errors.Is(err, ErrNotSetup) {
		t.Errorf("重置后登录应是 ErrNotSetup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("auth.json 应已删除: %v", err)
	}
	if m.lim.blocked("locked-client") != 0 {
		t.Error("限速计数应已清空")
	}

	// 重启之后也是未设置;而且能像第一次那样设置新密码,旧会话仍然无效
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(st, WithIterations(1000), WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	if m2.PasswordSet() {
		t.Error("重启后应仍是未设置")
	}
	clk.advance(time.Second)
	nt, err := m.Setup("brand-new")
	if err != nil {
		t.Fatalf("应能重新设置密码: %v", err)
	}
	if !m.Authenticated(nt) || m.Authenticated(tok) {
		t.Error("新会话有效、旧会话仍无效")
	}
	if _, err := m.Login("secret-1", "c"); !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("旧密码不应再能登录: %v", err)
	}
}

func TestResetBeforeSetupIsNoop(t *testing.T) {
	m, _, _ := newMgr(t)
	if err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if m.PasswordSet() {
		t.Fatal("仍应是未设置")
	}
}

// 文件删不掉时必须报错,并且密码和会话原样有效:不能让面板在数据还在的时候变成没有密码。
func TestResetFailureKeepsPassword(t *testing.T) {
	m, _, dir := newMgr(t)
	tok, err := m.Setup("secret-1")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "auth.json")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	// 非空目录占住这个名字:os.Remove 删不掉(不依赖是不是 root)
	if err := os.MkdirAll(filepath.Join(p, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.Reset(); err == nil {
		t.Fatal("删不掉应返回错误")
	}
	if !m.PasswordSet() || !m.Authenticated(tok) {
		t.Error("失败后密码和会话应原样有效")
	}
	if _, err := m.Login("secret-1", "c"); err != nil {
		t.Errorf("失败后仍应能登录: %v", err)
	}
}
