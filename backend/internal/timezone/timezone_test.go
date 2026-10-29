package timezone

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func TestTableSanity(t *testing.T) {
	if len(zoneList) < 300 || tbl.Version == "" {
		t.Fatalf("内置名单太少: %d 版本 %q", len(zoneList), tbl.Version)
	}
	if z := tbl.Zones["Asia/Shanghai"]; z.TZ != "CST-8" || z.CC != "CN" {
		t.Errorf("Asia/Shanghai: %+v", z)
	}
	if z := tbl.Zones["UTC"]; z.TZ != "UTC0" || z.CC != "" {
		t.Errorf("UTC: %+v", z)
	}
	for name, z := range tbl.Zones {
		if !safePosix.MatchString(z.TZ) || !safePosix.MatchString(name) {
			t.Errorf("%s 的名字或 POSIX 串不安全: %q", name, z.TZ)
		}
		if z.CC != "" && len(z.CC) != 2 {
			t.Errorf("%s 国家代码不对: %q", name, z.CC)
		}
	}
	for a, target := range tbl.Aliases {
		if _, ok := tbl.Zones[target]; !ok {
			t.Errorf("别名 %s 指向名单外的 %s", a, target)
		}
		if _, isZone := tbl.Zones[a]; isZone {
			t.Errorf("别名 %s 本身就在名单里", a)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"Asia/Shanghai", "Asia/Shanghai"}, {" Asia/Chongqing ", "Asia/Shanghai"}, {"Asia/Calcutta", "Asia/Kolkata"},
		{"US/Eastern", "America/New_York"}, {"Etc/UTC", "UTC"}, {"GMT", "UTC"}, {"Zulu", "UTC"}, {"UTC", "UTC"},
	} {
		if got, ok := Normalize(c.in); !ok || got != c.want {
			t.Errorf("Normalize(%q) = %q %v,期望 %q", c.in, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "Mars/Base", "Asia/Shanghai; reboot", "../etc/passwd", "asia/shanghai"} {
		if _, ok := Normalize(bad); ok {
			t.Errorf("%q 不该被认", bad)
		}
	}
	if !sortedStrings(zoneList) {
		t.Error("名单应排好序")
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// fakeOS 模拟一台 OpenWrt:uci 配置 + date。
type fakeOS struct {
	uci      map[string]string // 已提交
	pending  map[string]string // 已 set 未 commit
	cmds     []string
	failOn   string // 命令行包含这段时失败
	failOnce bool
	reloaded int
	others   []string // 别人暂存的修改
	reverted int
}

func newOpenWrt(zone, tz string) *fakeOS {
	f := &fakeOS{uci: map[string]string{}, pending: map[string]string{}}
	if zone != "" {
		f.uci["zonename"] = zone
	}
	if tz != "" {
		f.uci["timezone"] = tz
	}
	return f
}

func (f *fakeOS) run(_ context.Context, name string, args ...string) (system.RunResult, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.cmds = append(f.cmds, line)
	if f.failOn != "" && strings.Contains(line, f.failOn) {
		if f.failOnce {
			f.failOn = ""
		}
		return system.RunResult{Code: 1, Stderr: "boom"}, nil
	}
	switch {
	case name == "uci" && args[0] == "-q" && args[1] == "get":
		k := strings.TrimPrefix(args[2], "system.@system[0].")
		if v, ok := f.uci[k]; ok {
			return system.RunResult{Stdout: v + "\n"}, nil
		}
		return system.RunResult{Code: 1}, nil
	case name == "uci" && args[0] == "set":
		kv := strings.SplitN(strings.TrimPrefix(args[1], "system.@system[0]."), "=", 2)
		f.pending[kv[0]] = kv[1]
	case name == "uci" && args[0] == "-q" && args[1] == "delete":
		k := strings.TrimPrefix(args[2], "system.@system[0].")
		_, committed := f.uci[k]
		_, staged := f.pending[k]
		if !committed && !staged {
			return system.RunResult{Code: 1}, nil
		}
		f.pending[k] = ""
	case name == "uci" && args[0] == "changes":
		var b strings.Builder
		for k, v := range f.pending {
			fmt.Fprintf(&b, "system.@system[0].%s='%s'\n", k, v)
		}
		for _, o := range f.others {
			b.WriteString(o + "\n")
		}
		return system.RunResult{Stdout: b.String()}, nil
	case name == "uci" && args[0] == "revert":
		f.pending = map[string]string{}
		f.reverted++
	case name == "uci" && args[0] == "commit":
		for k, v := range f.pending {
			if v == "" {
				delete(f.uci, k)
			} else {
				f.uci[k] = v
			}
		}
		f.pending = map[string]string{}
	case name == "/etc/init.d/system":
		f.reloaded++
	case name == "env": // env TZ=<posix> date +...
		tz := strings.TrimPrefix(args[0], "TZ=")
		off := map[string]string{"CST-8": "+0800", "EST5EDT,M3.2.0,M11.1.0": "-0500", "UTC0": "+0000", "IST-5:30": "+0530"}[tz]
		return system.RunResult{Stdout: off + "|2026-10-08 10:03:52\n"}, nil
	}
	return system.RunResult{}, nil
}

func TestOpenWrtGet(t *testing.T) {
	f := newOpenWrt("Asia/Chongqing", "CST-8")
	s := &Service{Init: system.InitOpenWrt, Run: f.run}
	st, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Zone != "Asia/Shanghai" || st.Offset != "+08:00" || st.Local != "2026-10-08 10:03:52" || st.Platform != "openwrt" {
		t.Errorf("状态: %+v", st)
	}
	if len(st.Zones) < 300 || st.Countries["Asia/Shanghai"] != "CN" || st.Countries["UTC"] != "" {
		t.Errorf("名单和国家: %d %v", len(st.Zones), st.Countries["Asia/Shanghai"])
	}
	// 显式带 TZ 去问 date,不依赖本进程环境
	last := f.cmds[len(f.cmds)-1]
	if !strings.HasPrefix(last, "env TZ=CST-8 date") {
		t.Errorf("date 应显式带 TZ: %q", last)
	}
	// 只有 POSIX 串、没有时区名:zone 为空串,但偏移和时间照给
	f2 := newOpenWrt("", "IST-5:30")
	st, _ = (&Service{Init: system.InitOpenWrt, Run: f2.run}).Get(context.Background())
	if st.Zone != "" || st.Offset != "+05:30" {
		t.Errorf("没有时区名: %+v", st)
	}
}

func TestOpenWrtSetWritesBothAndReloads(t *testing.T) {
	f := newOpenWrt("UTC", "UTC0")
	s := &Service{Init: system.InitOpenWrt, Run: f.run}
	st, err := s.Set(context.Background(), "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if f.uci["zonename"] != "America/New_York" || f.uci["timezone"] != "EST5EDT,M3.2.0,M11.1.0" || f.reloaded != 1 {
		t.Errorf("uci: %v reload=%d", f.uci, f.reloaded)
	}
	if st.Zone != "America/New_York" || st.Offset != "-05:00" {
		t.Errorf("读回的状态: %+v", st)
	}
	var order []string
	for _, c := range f.cmds {
		switch {
		case strings.HasPrefix(c, "uci set"):
			order = append(order, "set")
		case strings.HasPrefix(c, "uci commit"):
			order = append(order, "commit")
		case strings.HasPrefix(c, "/etc/init.d/system"):
			order = append(order, "reload")
		}
	}
	if !reflect.DeepEqual(order, []string{"set", "set", "commit", "reload"}) {
		t.Errorf("顺序: %v", order)
	}
	// 别名也能存,存下来的是名单里的名字
	if st, err := s.Set(context.Background(), "Asia/Chongqing"); err != nil || st.Zone != "Asia/Shanghai" || f.uci["timezone"] != "CST-8" {
		t.Errorf("别名: %+v %v %v", st, err, f.uci)
	}
}

func TestOpenWrtSetFailureRestoresOld(t *testing.T) {
	f := newOpenWrt("Asia/Shanghai", "CST-8")
	f.failOn, f.failOnce = "init.d/system reload", true // 第一次 reload 失败,恢复时的 reload 成功
	s := &Service{Init: system.InitOpenWrt, Run: f.run}
	_, err := s.Set(context.Background(), "America/New_York")
	if err == nil || !strings.Contains(err.Error(), "已恢复原来的时区") {
		t.Fatalf("应报失败并说明已恢复: %v", err)
	}
	if f.uci["zonename"] != "Asia/Shanghai" || f.uci["timezone"] != "CST-8" {
		t.Errorf("原来的值应写回: %v", f.uci)
	}
	// 恢复也失败:要明说
	g := newOpenWrt("Asia/Shanghai", "CST-8")
	g.failOn = "init.d/system reload" // 一直失败
	_, err = (&Service{Init: system.InitOpenWrt, Run: g.run}).Set(context.Background(), "UTC")
	if err == nil || !strings.Contains(err.Error(), "恢复原来的时区也失败") {
		t.Errorf("恢复失败应明说: %v", err)
	}
	// 原来什么都没配:提交失败后要先 revert 丢掉暂存,再恢复成没配,而不是把新值留着
	h := newOpenWrt("", "")
	h.failOn, h.failOnce = "uci commit", true
	_, err = (&Service{Init: system.InitOpenWrt, Run: h.run}).Set(context.Background(), "UTC")
	if err == nil || len(h.uci) != 0 || len(h.pending) != 0 || h.reverted == 0 {
		t.Errorf("原来没配的应恢复成没配,暂存不能留: err=%v uci=%v pending=%v reverted=%d", err, h.uci, h.pending, h.reverted)
	}
}

func TestRefusesWhenOthersHaveStagedChanges(t *testing.T) {
	f := newOpenWrt("Asia/Shanghai", "CST-8")
	f.others = []string{"system.@system[0].hostname='newname'"}
	_, err := (&Service{Init: system.InitOpenWrt, Run: f.run}).Set(context.Background(), "UTC")
	if err == nil || !strings.Contains(err.Error(), "还没提交的修改") {
		t.Fatalf("有别人暂存的修改应拒绝: %v", err)
	}
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "uci set") || strings.HasPrefix(c, "uci commit") || strings.Contains(c, "reload") || strings.HasPrefix(c, "uci revert") {
			t.Errorf("拒绝时不能写、提交、重载,更不能 revert 别人的暂存: %q", c)
		}
	}
	if f.uci["zonename"] != "Asia/Shanghai" {
		t.Errorf("不该动: %v", f.uci)
	}
}

func TestUnknownZoneRunsNothing(t *testing.T) {
	f := newOpenWrt("UTC", "UTC0")
	s := &Service{Init: system.InitOpenWrt, Run: f.run}
	for _, z := range []string{"", "Mars/Base", "UTC; reboot", "$(reboot)"} {
		if _, err := s.Set(context.Background(), z); !errors.Is(err, ErrUnknownZone) {
			t.Errorf("%q 应 ErrUnknownZone: %v", z, err)
		}
	}
	if len(f.cmds) != 0 {
		t.Errorf("不认识的时区一条命令都不该跑: %v", f.cmds)
	}
}

func TestSystemd(t *testing.T) {
	cur := "Europe/London"
	var cmds []string
	run := func(_ context.Context, name string, args ...string) (system.RunResult, error) {
		cmds = append(cmds, strings.Join(append([]string{name}, args...), " "))
		switch {
		case name == "timedatectl" && args[0] == "show":
			return system.RunResult{Stdout: cur + "\n"}, nil
		case name == "timedatectl" && args[0] == "set-timezone":
			cur = args[1]
		case name == "date":
			return system.RunResult{Stdout: "+0100|2026-10-08 10:03:52\n"}, nil
		}
		return system.RunResult{}, nil
	}
	s := &Service{Init: system.InitSystemd, Run: run}
	st, err := s.Get(context.Background())
	if err != nil || st.Zone != "Europe/London" || st.Offset != "+01:00" || st.Platform != "systemd" {
		t.Fatalf("%+v %v", st, err)
	}
	st, err = s.Set(context.Background(), "Asia/Tokyo")
	if err != nil || st.Zone != "Asia/Tokyo" {
		t.Fatalf("%+v %v", st, err)
	}
	if !contains(cmds, "timedatectl set-timezone Asia/Tokyo") {
		t.Errorf("命令: %v", cmds)
	}
	// 失败带退出码
	bad := func(context.Context, string, ...string) (system.RunResult, error) {
		return system.RunResult{Code: 1, Stderr: "Access denied"}, nil
	}
	if _, err := (&Service{Init: system.InitSystemd, Run: bad}).Set(context.Background(), "UTC"); err == nil || !strings.Contains(err.Error(), "Access denied") {
		t.Errorf("应带原因: %v", err)
	}
}

func TestReadBackMismatchAndUnsupported(t *testing.T) {
	// 命令都成功,但读回来的不是想要的:不能报成功
	run := func(_ context.Context, name string, args ...string) (system.RunResult, error) {
		if name == "timedatectl" && args[0] == "show" {
			return system.RunResult{Stdout: "Europe/London\n"}, nil
		}
		return system.RunResult{Stdout: "+0000|2026-10-08 10:03:52\n"}, nil
	}
	if _, err := (&Service{Init: system.InitSystemd, Run: run}).Set(context.Background(), "Asia/Tokyo"); err == nil || !strings.Contains(err.Error(), "读回来") {
		t.Errorf("读回不一致应报错: %v", err)
	}
	none := &Service{Init: system.InitNone, Run: func(context.Context, string, ...string) (system.RunResult, error) {
		t.Fatal("没有服务管理器不该跑命令")
		return system.RunResult{}, nil
	}}
	if _, err := none.Set(context.Background(), "UTC"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("应 ErrUnsupported: %v", err)
	}
}

func TestGetToleratesBrokenSystem(t *testing.T) {
	run := func(context.Context, string, ...string) (system.RunResult, error) {
		return system.RunResult{}, fmt.Errorf("没有这个命令")
	}
	st, err := (&Service{Init: system.InitOpenWrt, Run: run}).Get(context.Background())
	if err != nil || st.Zone != "" || st.Offset != "" || st.Local != "" || len(st.Zones) < 300 {
		t.Errorf("读不到系统状态不是错误,名单照给: %+v %v", st, err)
	}
	// date 输出不合法:偏移和时间留空
	weird := func(_ context.Context, name string, _ ...string) (system.RunResult, error) {
		return system.RunResult{Stdout: "garbage"}, nil
	}
	if st, _ := (&Service{Init: system.InitSystemd, Run: weird}).Get(context.Background()); st.Offset != "" || st.Local != "" {
		t.Errorf("坏输出应留空: %+v", st)
	}
}

func TestUnsafePosixNotPassedToEnv(t *testing.T) {
	f := newOpenWrt("Asia/Shanghai", "CST-8; reboot")
	s := &Service{Init: system.InitOpenWrt, Run: f.run}
	_, _ = s.Get(context.Background())
	for _, c := range f.cmds {
		if strings.Contains(c, "reboot") && strings.HasPrefix(c, "env") {
			t.Errorf("来自配置的 POSIX 串不安全时不该拼进 env: %q", c)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
