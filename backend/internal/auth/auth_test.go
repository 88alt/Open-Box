package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newMgr(t *testing.T) (*Manager, *fakeClock, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	m, err := NewManager(st, WithIterations(1000), WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	return m, clk, dir
}

func TestSetupThenLogin(t *testing.T) {
	m, _, _ := newMgr(t)
	if m.PasswordSet() {
		t.Fatal("初始不应已设置")
	}
	if _, err := m.Login("whatever", "c"); !errors.Is(err, ErrNotSetup) {
		t.Fatalf("未设置时登录应返回 ErrNotSetup: %v", err)
	}
	tok, err := m.Setup("secret-1")
	if err != nil {
		t.Fatal(err)
	}
	if !m.PasswordSet() || !m.Authenticated(tok) {
		t.Fatal("设置后应已登录")
	}
	if _, err := m.Setup("another"); !errors.Is(err, ErrAlreadySet) {
		t.Fatalf("重复设置应返回 ErrAlreadySet: %v", err)
	}
	if _, err := m.Login("wrong", "c"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("错误密码: %v", err)
	}
	tok2, err := m.Login("secret-1", "c")
	if err != nil || !m.Authenticated(tok2) {
		t.Fatalf("正确密码应能登录: %v", err)
	}
}

func TestPasswordLengthRules(t *testing.T) {
	m, _, _ := newMgr(t)
	cases := []struct {
		pw  string
		err error
	}{
		{"abc", ErrTooShort},
		{"", ErrTooShort},
		{"汉字汉", ErrTooShort}, // 3 个字符,按字符数而不是字节数
		{strings.Repeat("x", MaxPasswordBytes+1), ErrTooLong},
		{"汉字汉字", nil},
		{strings.Repeat("x", MaxPasswordBytes), nil},
	}
	for _, c := range cases {
		if err := validatePassword(c.pw); !errors.Is(err, c.err) {
			t.Errorf("%q: 得到 %v, 期望 %v", c.pw, err, c.err)
		}
	}
	if _, err := m.Setup("abc"); !errors.Is(err, ErrTooShort) {
		t.Fatal(err)
	}
	if m.PasswordSet() {
		t.Fatal("太短的密码不应被保存")
	}
}

func TestLockoutAndRecovery(t *testing.T) {
	m, clk, _ := newMgr(t)
	if _, err := m.Setup("secret-1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := m.Login("bad", "1.2.3.4"); !errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
	}
	var locked *LockedError
	if _, err := m.Login("secret-1", "1.2.3.4"); !errors.As(err, &locked) || locked.RetryAfter <= 0 {
		t.Fatalf("5 次失败后即使密码正确也应被锁定: %v", err)
	}
	if _, err := m.Login("secret-1", "5.6.7.8"); err != nil {
		t.Fatalf("其它客户端不受影响: %v", err)
	}
	clk.advance(31 * time.Second)
	if _, err := m.Login("secret-1", "1.2.3.4"); err != nil {
		t.Fatalf("锁定到期后应能登录: %v", err)
	}
	// 成功登录清零:再错 4 次不应锁定
	for i := 0; i < 4; i++ {
		_, _ = m.Login("bad", "1.2.3.4")
	}
	if _, err := m.Login("secret-1", "1.2.3.4"); err != nil {
		t.Fatalf("成功后计数应清零: %v", err)
	}
}

func TestLockoutBacksOff(t *testing.T) {
	m, clk, _ := newMgr(t)
	_, _ = m.Setup("secret-1")
	for i := 0; i < 5; i++ {
		_, _ = m.Login("bad", "c")
	}
	clk.advance(31 * time.Second)
	_, _ = m.Login("bad", "c") // 第 6 次失败 → 锁 60 秒
	var locked *LockedError
	_, err := m.Login("secret-1", "c")
	if !errors.As(err, &locked) || locked.RetryAfter < 50*time.Second {
		t.Fatalf("第二轮锁定应更久: %v", err)
	}
}

func TestSessionExpiryAndTamper(t *testing.T) {
	m, clk, _ := newMgr(t)
	tok, _ := m.Setup("secret-1")
	if !m.Authenticated(tok) {
		t.Fatal("新令牌应有效")
	}
	for name, bad := range map[string]string{
		"空":     "",
		"乱码":    "garbage",
		"少一段":   strings.Join(strings.Split(tok, ".")[:3], "."),
		"改签名":   tok[:len(tok)-2] + "AA",
		"改过期时间": strings.Replace(tok, strings.Split(tok, ".")[1], "9999999999", 1),
		"改版本":   "v2" + tok[2:],
	} {
		if m.Authenticated(bad) {
			t.Errorf("%s: 不应通过校验", name)
		}
	}
	clk.advance(SessionTTL + time.Second)
	if m.Authenticated(tok) {
		t.Fatal("过期令牌不应有效")
	}
}

func TestTokenFromAnotherInstallIsRejected(t *testing.T) {
	a, _, _ := newMgr(t)
	b, _, _ := newMgr(t)
	ta, _ := a.Setup("secret-1")
	_, _ = b.Setup("secret-1")
	if b.Authenticated(ta) {
		t.Fatal("别的实例签发的令牌不应被接受")
	}
}

func TestChangePasswordRotatesSessions(t *testing.T) {
	m, _, _ := newMgr(t)
	old, _ := m.Setup("secret-1")
	if _, err := m.ChangePassword("wrong", "newpass1", "c"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("当前密码错误: %v", err)
	}
	if _, err := m.ChangePassword("secret-1", "abc", "c"); !errors.Is(err, ErrTooShort) {
		t.Fatalf("新密码太短: %v", err)
	}
	if !m.Authenticated(old) {
		t.Fatal("失败的修改不应让旧会话失效")
	}
	fresh, err := m.ChangePassword("secret-1", "newpass1", "c")
	if err != nil {
		t.Fatal(err)
	}
	if m.Authenticated(old) {
		t.Fatal("改密后旧会话必须失效")
	}
	if !m.Authenticated(fresh) {
		t.Fatal("改密返回的新令牌应有效")
	}
	if _, err := m.Login("secret-1", "c"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatal("旧密码不应再能登录")
	}
	if _, err := m.Login("newpass1", "c"); err != nil {
		t.Fatal(err)
	}
	fresh2, _, _ := newMgr(t)
	if _, err := fresh2.ChangePassword("a", "bbbb", "c"); !errors.Is(err, ErrNotSetup) {
		t.Fatalf("未设置时改密: %v", err)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	m, clk, dir := newMgr(t)
	tok, _ := m.Setup("secret-1")

	st, _ := store.Open(dir)
	m2, err := NewManager(st, WithIterations(1000), WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	if !m2.PasswordSet() || !m2.Authenticated(tok) {
		t.Fatal("重启后密码和会话应保留")
	}
	if _, err := m2.Login("secret-1", "c"); err != nil {
		t.Fatal(err)
	}
}

func TestStoredFileHasNoPlaintext(t *testing.T) {
	m, _, dir := newMgr(t)
	_, _ = m.Setup("my-plain-pass")
	data, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "my-plain-pass") || !strings.Contains(string(data), "pbkdf2-sha256$1000$") {
		t.Fatalf("数据文件内容不对: %s", data)
	}
}

func TestCorruptDataRefusesToStart(t *testing.T) {
	for name, content := range map[string]string{
		"不是 JSON": "{oops",
		"缺字段":     `{"passwordHash":"x"}`,
		"密钥太短":    `{"passwordHash":"x","sessionSecret":"AAAA"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			_ = os.MkdirAll(dir, 0o700)
			_ = os.WriteFile(filepath.Join(dir, "auth.json"), []byte(content), 0o600)
			st, _ := store.Open(dir)
			if _, err := NewManager(st); err == nil {
				t.Fatal("损坏的数据文件必须拒绝启动,不能当成未设置")
			}
		})
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	good, _ := hashPassword("abcd", 10)
	if !verifyPassword(good, "abcd") || verifyPassword(good, "abce") {
		t.Fatal("基本校验失败")
	}
	for _, bad := range []string{"", "x", "pbkdf2-sha256$10$AA", "md5$10$AA$AA", "pbkdf2-sha256$0$AA$AA",
		"pbkdf2-sha256$99999999999$AA$AA", "pbkdf2-sha256$10$!!$AA", "pbkdf2-sha256$10$AA$"} {
		if verifyPassword(bad, "abcd") {
			t.Errorf("%q 不应通过", bad)
		}
	}
}

func TestConcurrentLoginsCannotBypassLimiter(t *testing.T) {
	m, _, _ := newMgr(t)
	_, _ = m.Setup("secret-1")
	var wg sync.WaitGroup
	invalid := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Login("bad", "same"); errors.Is(err, ErrInvalidPassword) {
				invalid <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(invalid)
	if n := len(invalid); n != 5 {
		t.Fatalf("并发 100 次错误尝试中,只应有 5 次真正校验,得到 %d", n)
	}
}
