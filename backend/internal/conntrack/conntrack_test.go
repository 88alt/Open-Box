package conntrack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

const (
	procUDP     = "ipv4     2 udp      17 25 src=192.168.5.77 dst=192.168.5.1 sport=39000 dport=53 src=192.168.5.1 dst=192.168.5.77 sport=53 dport=39000 mark=0 zone=0 use=2"
	procTCP     = "ipv4     2 tcp      6 117 TIME_WAIT src=192.168.5.77 dst=93.184.216.34 sport=40123 dport=443 src=93.184.216.34 dst=192.168.5.77 sport=443 dport=40123 [ASSURED] mark=255 zone=0 use=2"
	cmdUDP      = "udp      17 20 src=192.168.5.77 dst=1.1.1.1 sport=39001 dport=53 [UNREPLIED] src=1.1.1.1 dst=192.168.5.77 sport=53 dport=39001 mark=0 use=1"
	redirected  = "ipv4     2 udp      17 28 src=192.168.5.77 dst=8.8.8.8 sport=39002 dport=53 src=192.168.5.1 dst=192.168.5.77 sport=7853 dport=39002 mark=0 use=1"
	procV6      = "ipv6     10 udp     17 20 src=fd00::77 dst=fd00::1 sport=1234 dport=53 src=fd00::1 dst=fd00::77 sport=53 dport=1234 mark=0 use=1"
	noiseHeader = "conntrack v1.4.6 (conntrack-tools): 3 flow entries have been shown."
)

func TestParseBothFormats(t *testing.T) {
	e, ok := Parse(procUDP)
	if !ok || e.Proto != "udp" || e.Src != "192.168.5.77" || e.Dst != "192.168.5.1" || e.Sport != 39000 || e.Dport != 53 ||
		e.RSrc != "192.168.5.1" || e.RDst != "192.168.5.77" || e.RSport != 53 || e.RDport != 39000 || e.Mark != 0 || e.Unreplied || e.State != "" {
		t.Fatalf("/proc 的 udp:%+v", e)
	}
	e, ok = Parse(procTCP)
	if !ok || e.Proto != "tcp" || e.State != "TIME_WAIT" || e.Mark != 255 || e.Dport != 443 || e.RSport != 443 {
		t.Fatalf("/proc 的 tcp:%+v", e)
	}
	e, ok = Parse(cmdUDP)
	if !ok || e.Proto != "udp" || !e.Unreplied || e.Dst != "1.1.1.1" || e.RSrc != "1.1.1.1" {
		t.Fatalf("conntrack -L 的写法:%+v", e)
	}
	e, ok = Parse(procV6)
	if !ok || e.Src != "fd00::77" || e.RSrc != "fd00::1" {
		t.Fatalf("ipv6:%+v", e)
	}
	if e.Line != procV6 {
		t.Errorf("Line 要保留原文")
	}
}

func TestParseRejectsNonEntries(t *testing.T) {
	for _, l := range []string{"", "   ", noiseHeader, "ipv4 2 udp 17 25 src=1.1.1.1 dst=2.2.2.2 sport=1 dport=2", "src=1.1.1.1 dst=2.2.2.2 src=2.2.2.2 dst=1.1.1.1", "garbage"} {
		if e, ok := Parse(l); ok {
			t.Errorf("%q 不该解析成记录:%+v", l, e)
		}
	}
}

func fileWith(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nf_conntrack")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindFromFileFilters(t *testing.T) {
	r := &Reader{File: fileWith(t, procUDP, procTCP, redirected, "garbage line", procV6)}
	got, err := r.Find(context.Background(), Query{Proto: "udp", Src: "192.168.5.77", Dport: 53})
	if err != nil || len(got) != 2 {
		t.Fatalf("err=%v got=%+v", err, got)
	}
	if got[0].Sport != 39000 || got[1].Sport != 39002 {
		t.Errorf("要保持表里顺序:%+v", got)
	}
	got, _ = r.Find(context.Background(), Query{Dst: "93.184.216.34", Sport: 40123})
	if len(got) != 1 || got[0].Proto != "tcp" {
		t.Fatalf("按目的和源端口:%+v", got)
	}
	got, _ = r.Find(context.Background(), Query{Src: "fd00:0:0:0:0:0:0:77"}) // 写法不同的同一个 v6 地址
	if len(got) != 1 {
		t.Fatalf("地址按 IP 比较:%+v", got)
	}
	got, _ = r.Find(context.Background(), Query{Src: "10.9.9.9"})
	if len(got) != 0 {
		t.Fatalf("没有就是空:%+v", got)
	}
}

func TestFindFallsBackToCommandWithFilters(t *testing.T) {
	var gotName string
	var gotArgs []string
	r := &Reader{
		File:     filepath.Join(t.TempDir(), "missing"),
		LookPath: func(string) (string, error) { return "/usr/sbin/conntrack", nil },
		Run: func(_ context.Context, name string, args ...string) (system.RunResult, error) {
			gotName, gotArgs = name, args
			return system.RunResult{Stdout: cmdUDP + "\n" + procUDP + "\n", Stderr: noiseHeader}, nil
		},
	}
	got, err := r.Find(context.Background(), Query{Proto: "udp", Src: "192.168.5.77", Dst: "1.1.1.1", Dport: 53, Sport: 39001})
	if err != nil || len(got) != 1 || got[0].Sport != 39001 {
		t.Fatalf("err=%v got=%+v(命令的输出还要再按条件过滤一遍)", err, got)
	}
	want := []string{"-L", "-p", "udp", "-s", "192.168.5.77", "-d", "1.1.1.1", "--sport", "39001", "--dport", "53"}
	if gotName != "conntrack" || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("命令:%s %v", gotName, gotArgs)
	}
}

func TestFindErrorsAreErrorsNotEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	noCmd := func(string) (string, error) { return "", errors.New("not found") }
	if got, err := (&Reader{File: missing, LookPath: noCmd}).Find(context.Background(), Query{}); err == nil || got != nil {
		t.Fatalf("表和命令都没有必须报错,不能当成「没有记录」:%v %v", got, err)
	}
	have := func(string) (string, error) { return "/x/conntrack", nil }
	failing := func(context.Context, string, ...string) (system.RunResult, error) {
		return system.RunResult{Code: 1, Stderr: "Operation not permitted"}, nil
	}
	if _, err := (&Reader{File: missing, LookPath: have, Run: failing}).Find(context.Background(), Query{}); err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("命令失败要带原因:%v", err)
	}
	cantStart := func(context.Context, string, ...string) (system.RunResult, error) {
		return system.RunResult{}, errors.New("exec failed")
	}
	if _, err := (&Reader{File: missing, LookPath: have, Run: cantStart}).Find(context.Background(), Query{}); err == nil {
		t.Fatal("起不来命令要报错")
	}
}

func TestFindCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Reader{File: fileWith(t, procUDP)}).Find(ctx, Query{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消要返回 context 错误:%v", err)
	}
}

func parse(t *testing.T, lines ...string) []Entry {
	t.Helper()
	var out []Entry
	for _, l := range lines {
		e, ok := Parse(l)
		if !ok {
			t.Fatalf("解析不了:%s", l)
		}
		out = append(out, e)
	}
	return out
}

func TestAnalyzeDNSNormalAnswer(t *testing.T) {
	ev := AnalyzeDNS(parse(t, procUDP), "192.168.5.1", 53, []int{39000})
	if ev.Flows != 1 || ev.Hijacked || ev.AnsweredBy != "192.168.5.1" || ev.Line != procUDP {
		t.Fatalf("%+v", ev)
	}
}

func TestAnalyzeDNSRewrittenReplyIsHijacked(t *testing.T) {
	ev := AnalyzeDNS(parse(t, redirected), "8.8.8.8", 53, []int{39002})
	if ev.Flows != 1 || !ev.Hijacked || ev.AnsweredBy != "192.168.5.1:7853" {
		t.Fatalf("回复方的地址和端口都变了:%+v", ev)
	}
	// 只有地址变:不带端口
	e := parse(t, "ipv4 2 udp 17 20 src=192.168.5.77 dst=8.8.8.8 sport=5000 dport=53 src=192.168.5.1 dst=192.168.5.77 sport=53 dport=5000 mark=0")
	if ev := AnalyzeDNS(e, "8.8.8.8", 53, []int{5000}); !ev.Hijacked || ev.AnsweredBy != "192.168.5.1" {
		t.Fatalf("%+v", ev)
	}
}

func TestAnalyzeDNSOnlyCountsThisQuerysPorts(t *testing.T) {
	entries := parse(t, procUDP, cmdUDP, redirected)
	// 终端这次用的源端口是 39000;表里还留着之前几次测试的记录,不能算进来
	ev := AnalyzeDNS(entries, "192.168.5.1", 53, []int{39000})
	if ev.Flows != 1 || ev.Hijacked {
		t.Fatalf("旧记录不该参与:%+v", ev)
	}
	// 一次解析可能换端口重试:两个端口都算
	ev = AnalyzeDNS(parse(t, procUDP, procUDP), "192.168.5.1", 53, []int{39000, 39999})
	if ev.Flows != 2 {
		t.Fatalf("%+v", ev)
	}
	// 没有对上的记录:flows 为 0,不是 error
	ev = AnalyzeDNS(entries, "192.168.5.1", 53, []int{1})
	if ev.Flows != 0 || ev.Hijacked || ev.Line != "" || ev.AnsweredBy != "192.168.5.1" {
		t.Fatalf("%+v", ev)
	}
	// 没给端口:什么都对不上
	if ev := AnalyzeDNS(entries, "192.168.5.1", 53, nil); ev.Flows != 0 {
		t.Fatalf("%+v", ev)
	}
}

func TestAnalyzeDNSUnrepliedIsNotCalledHijacked(t *testing.T) {
	ev := AnalyzeDNS(parse(t, cmdUDP), "1.1.1.1", 53, []int{39001})
	if ev.Flows != 1 || ev.Hijacked || ev.AnsweredBy != "1.1.1.1" {
		t.Fatalf("没回复就看不出谁答的,不能判劫持:%+v", ev)
	}
}
