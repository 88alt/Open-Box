package system

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 查询日志相关的接管测试:日志路径要写成 / 开头的绝对路径,Windows 上跳过(后端只跑在 OpenWrt / Debian 上)。

func logEnv(t *testing.T) (*tkEnv, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("查询日志路径要写成 / 开头的绝对路径;后端只跑在 OpenWrt / Debian 上")
	}
	e := newTkEnv(t)
	run := filepath.Join(e.dir, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	lp := filepath.Join(run, "openbox-dnsmasq.log")
	e.tk.QueryLog = lp
	return e, lp
}

func (e *tkEnv) text(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.file)
	if err != nil {
		t.Fatalf("读接管文件: %v", err)
	}
	return string(b)
}

func (e *tkEnv) plain() string {
	return takeoverMarker + "\nno-resolv\nserver=127.0.0.1#" + port(e.kernel.addr) + "\n"
}

func (e *tkEnv) mainConf() string { return filepath.Join(e.dir, "dnsmasq.conf.cfg01") }

func (e *tkEnv) setMain(t *testing.T, extra string) {
	t.Helper()
	text := "# 生成的主配置\nconf-dir=" + e.conf + "\nresolv-file=/tmp/resolv.conf.auto\n" + extra
	if err := os.WriteFile(e.mainConf(), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestQueryLogAddedAndIdempotent(t *testing.T) {
	e, lp := logEnv(t)
	changed, err := e.tk.Apply(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if want := e.plain() + "log-queries\nlog-facility=" + lp + "\n"; e.text(t) != want {
		t.Fatalf("内容不对:\n%s\n期望:\n%s", e.text(t), want)
	}
	if path, skip := e.tk.QueryLogState(); path != lp || skip != "" {
		t.Fatalf("QueryLogState = %q %q", path, skip)
	}
	if !e.tk.LogOwned(lp) || e.tk.LogOwned("/other.log") || e.tk.LogOwned("") {
		t.Fatalf("LogOwned 判断不对")
	}
	if st := e.tk.Status(context.Background()); !st.Applied || !st.Supported {
		t.Fatalf("带着日志行的接管也该算已应用: %+v", st)
	}
	// 再来一次:接管文件里自己的 log-queries 不能被当成「用户自己设的」
	changed, err = e.tk.Apply(context.Background())
	if err != nil || changed || e.svc.count() != 1 {
		t.Fatalf("幂等失败: changed=%v err=%v restarts=%d", changed, err, e.svc.count())
	}
	if _, skip := e.tk.QueryLogState(); skip != "" {
		t.Fatalf("skip = %q", skip)
	}
}

func TestQueryLogNotConfiguredAddsNothing(t *testing.T) {
	e := newTkEnv(t)
	if _, err := e.tk.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.text(t) != e.plain() {
		t.Fatalf("不该有日志行:\n%s", e.text(t))
	}
	if path, skip := e.tk.QueryLogState(); path != "" || skip != "" {
		t.Fatalf("QueryLogState = %q %q", path, skip)
	}
	if e.tk.LogOwned("/var/run/openbox-dnsmasq.log") {
		t.Fatal("没加日志就不该认领任何日志文件")
	}
}

func TestQueryLogUserSettingsLeftAlone(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e *tkEnv)
	}{
		{"主配置里有 log-facility", func(t *testing.T, e *tkEnv) { e.setMain(t, "log-facility=/var/log/dnsmasq.log\n") }},
		{"主配置里有 log-queries", func(t *testing.T, e *tkEnv) { e.setMain(t, "log-queries\n") }},
		{"主配置里有带值的 log-queries", func(t *testing.T, e *tkEnv) { e.setMain(t, "  log-queries=extra  \n") }},
		{"配置目录里别的文件有", func(t *testing.T, e *tkEnv) {
			if err := os.WriteFile(filepath.Join(e.conf, "99-mine.conf"), []byte("log-queries=extra\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"conf-file 引进来的文件里有", func(t *testing.T, e *tkEnv) {
			inc := filepath.Join(e.dir, "inc.conf")
			if err := os.WriteFile(inc, []byte("log-facility=LOCAL0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			e.setMain(t, "conf-file="+inc+"\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, lp := logEnv(t)
			c.setup(t, e)
			changed, err := e.tk.Apply(context.Background())
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if e.text(t) != e.plain() {
				t.Fatalf("用户已经设了日志,不该再加:\n%s", e.text(t))
			}
			if path, skip := e.tk.QueryLogState(); path != "" || skip != LogSkipUser {
				t.Fatalf("QueryLogState = %q %q", path, skip)
			}
			if e.tk.LogOwned(lp) {
				t.Fatal("没加日志就不该认领")
			}
			if e.svc.count() != 1 {
				t.Fatalf("restarts=%d", e.svc.count())
			}
		})
	}
}

func TestQueryLogIgnoresCommentsAndFilteredFiles(t *testing.T) {
	e, lp := logEnv(t)
	e.setMain(t, "# log-queries\n#log-facility=/x\n")
	// 配置目录的过滤规则会让 dnsmasq 跳过 .bak:里面的设置不算
	if err := os.WriteFile(e.mainConf(), []byte("conf-dir="+e.conf+",.bak\n# log-queries\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.conf, "old.bak"), []byte("log-queries\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tk.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.text(t), "log-facility="+lp) {
		t.Fatalf("注释和被过滤掉的文件不算用户设置,应该加日志:\n%s", e.text(t))
	}
}

func TestQueryLogUserSettingAppearsAndGoesAway(t *testing.T) {
	e, lp := logEnv(t)
	ctx := context.Background()
	if _, err := e.tk.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	e.setMain(t, "log-facility=/var/log/mine.log\n")
	changed, err := e.tk.Apply(ctx)
	if err != nil || !changed || e.svc.count() != 2 {
		t.Fatalf("用户后来加了日志设置:changed=%v err=%v restarts=%d", changed, err, e.svc.count())
	}
	if e.text(t) != e.plain() {
		t.Fatalf("应去掉我们的日志行:\n%s", e.text(t))
	}
	if path, skip := e.tk.QueryLogState(); path != "" || skip != LogSkipUser {
		t.Fatalf("QueryLogState = %q %q", path, skip)
	}
	e.setMain(t, "")
	changed, err = e.tk.Apply(ctx)
	if err != nil || !changed || e.svc.count() != 3 {
		t.Fatalf("用户去掉了设置:changed=%v err=%v restarts=%d", changed, err, e.svc.count())
	}
	if !strings.Contains(e.text(t), "log-facility="+lp) {
		t.Fatalf("应重新加上日志:\n%s", e.text(t))
	}
	if path, skip := e.tk.QueryLogState(); path != lp || skip != "" {
		t.Fatalf("QueryLogState = %q %q", path, skip)
	}
}

func TestQueryLogRestartFailureRetriesWithoutLog(t *testing.T) {
	e, lp := logEnv(t)
	ctx := context.Background()
	e.svc.failAt[1] = true // 带着日志的第一次重启失败
	changed, err := e.tk.Apply(ctx)
	if err != nil || !changed {
		t.Fatalf("接管本身应该成功: changed=%v err=%v", changed, err)
	}
	if e.text(t) != e.plain() {
		t.Fatalf("应去掉日志行:\n%s", e.text(t))
	}
	if path, skip := e.tk.QueryLogState(); path != "" || skip != LogSkipFailed {
		t.Fatalf("QueryLogState = %q %q", path, skip)
	}
	if e.svc.count() != 2 {
		t.Fatalf("restarts=%d", e.svc.count())
	}
	// failed 记着:再部署不再试,不重启
	changed, err = e.tk.Apply(ctx)
	if err != nil || changed || e.svc.count() != 2 {
		t.Fatalf("不该重试: changed=%v err=%v restarts=%d", changed, err, e.svc.count())
	}
	// 撤销之后才会重新试
	if err := e.tk.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if _, err := e.tk.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.text(t), "log-facility="+lp) {
		t.Fatalf("撤销后应重新试着加日志:\n%s", e.text(t))
	}
	if e.svc.count() != 4 {
		t.Fatalf("restarts=%d", e.svc.count())
	}
}

func TestQueryLogHealthFailureRetriesWithoutLog(t *testing.T) {
	e, _ := logEnv(t)
	// 第 1 次重启(带日志)后 dnsmasq 不答;第 2 次(去掉日志)恢复
	e.svc.onRestart = func(n int) { e.dnsmasq.setUp(n != 1) }
	changed, err := e.tk.Apply(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if e.text(t) != e.plain() || e.svc.count() != 2 {
		t.Fatalf("内容 %q restarts=%d", e.text(t), e.svc.count())
	}
	if _, skip := e.tk.QueryLogState(); skip != LogSkipFailed {
		t.Fatalf("skip=%q", skip)
	}
}

func TestQueryLogBothAttemptsFailRollsBack(t *testing.T) {
	e, _ := logEnv(t)
	e.svc.failAt[1], e.svc.failAt[2] = true, true
	changed, err := e.tk.Apply(context.Background())
	if err == nil || changed || !strings.Contains(err.Error(), "已自动撤销") || !strings.Contains(err.Error(), "带查询日志的那一次") {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	e.wantNoFile(t)
	e.wantNoState(t)
	if e.svc.count() != 3 {
		t.Fatalf("应重启 3 次(带日志、不带日志、撤销),实际 %d", e.svc.count())
	}
}

func TestQueryLogUnusablePathSkipsWithoutRetry(t *testing.T) {
	for _, p := range []string{
		"",                                 // 不配 = 没有原因
		"relative.log",                     // 不是绝对路径
		"/var/run/has space.log",           // 空白
		"/definitely/not/here/dnsmasq.log", // 目录不存在
		"/var/run/",                        // 目录
		"/var/run/a#b.log",                 // 注释符
	} {
		t.Run(p, func(t *testing.T) {
			e, _ := logEnv(t)
			e.tk.QueryLog = p
			changed, err := e.tk.Apply(context.Background())
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if e.text(t) != e.plain() || e.svc.count() != 1 {
				t.Fatalf("内容 %q restarts=%d", e.text(t), e.svc.count())
			}
			want := LogSkipFailed
			if p == "" {
				want = ""
			}
			if path, skip := e.tk.QueryLogState(); path != "" || skip != want {
				t.Fatalf("QueryLogState = %q %q,想要 skip=%q", path, skip, want)
			}
		})
	}
}

func TestValidLogPath(t *testing.T) {
	for p, ok := range map[string]bool{
		"/var/run/openbox-dnsmasq.log": true,
		"/tmp/a-b_c.d.log":             true,
		"":                             false,
		"log":                          false,
		"/":                            false,
		"/tmp/":                        false,
		"/tmp/a b":                     false,
		"/tmp/a\nb":                    false,
		"/tmp/a,b":                     false,
		"/tmp/a#b":                     false,
		`/tmp/a"b`:                     false,
		`/tmp/a\b`:                     false,
		"/" + strings.Repeat("a", 250): false,
	} {
		if err := validLogPath(p); (err == nil) != ok {
			t.Errorf("validLogPath(%q) = %v,想要 ok=%v", p, err, ok)
		}
	}
}

func TestHasLogOption(t *testing.T) {
	for text, want := range map[string]bool{
		"log-queries":                      true,
		"log-queries=extra":                true,
		" log-facility = /x.log ":          true,
		"# log-queries":                    false,
		"":                                 false,
		"log-dhcp":                         false,
		"server=1.1.1.1#53\nlog-async=5":   false,
		"no-resolv\nlog-queries\nserver=x": true,
		"some-log-queries=1":               false,
	} {
		if got := hasLogOption(text); got != want {
			t.Errorf("hasLogOption(%q) = %v,想要 %v", text, got, want)
		}
	}
}

func TestRevertRemovesOurLogFileOnly(t *testing.T) {
	ctx := context.Background()
	t.Run("带 .log 的普通文件", func(t *testing.T) {
		e, lp := logEnv(t)
		if _, err := e.tk.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lp, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.tk.Revert(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(lp); !os.IsNotExist(err) {
			t.Fatalf("日志文件该被删掉: %v", err)
		}
		e.wantNoFile(t)
		e.wantNoState(t)
	})
	t.Run("名字不是 .log 的不碰", func(t *testing.T) {
		e, lp := logEnv(t)
		lp = strings.TrimSuffix(lp, ".log") + ".txt"
		e.tk.QueryLog = lp
		if _, err := e.tk.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lp, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.tk.Revert(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(lp); err != nil {
			t.Fatalf("不该删: %v", err)
		}
	})
	t.Run("符号链接不碰", func(t *testing.T) {
		e, lp := logEnv(t)
		if _, err := e.tk.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(e.dir, "precious.log")
		if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, lp); err != nil {
			t.Skipf("建不了符号链接: %v", err)
		}
		if err := e.tk.Revert(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("链接指向的文件不该被删: %v", err)
		}
	})
	t.Run("用户自己的日志设置:没加过就不删", func(t *testing.T) {
		e, lp := logEnv(t)
		e.setMain(t, "log-queries\n")
		if _, err := e.tk.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lp, []byte("theirs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.tk.Revert(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(lp); err != nil {
			t.Fatalf("不是我们让写的,不该删: %v", err)
		}
	})
}
