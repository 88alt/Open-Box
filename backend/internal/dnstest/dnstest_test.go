package dnstest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

type fakeQuery struct {
	got []probe.DNSJob
	res probe.DNSResult
	err error
}

func (f *fakeQuery) QueryDNS(_ context.Context, j probe.DNSJob) (probe.DNSResult, error) {
	f.got = append(f.got, j)
	return f.res, f.err
}

func cfgWith(final string, outbounds ...any) map[string]any {
	return map[string]any{"route": map[string]any{"final": final}, "outbounds": outbounds}
}

func ob(typ, tag string, kv ...any) map[string]any {
	m := map[string]any{"type": typ, "tag": tag}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func newTester(q Querier) *Tester {
	return &Tester{
		Query:  q,
		WAN:    func() []string { return []string{"192.168.1.1", "8.8.8.8"} },
		Region: func(context.Context) string { return "cn" },
		Profile: func(context.Context) (map[string]any, error) {
			return map[string]any{"testUrl": "https://www.gstatic.com/generate_204", "directTestUrl": "https://connectivitycheck.platform.hicloud.com/generate_204"}, nil
		},
		Config: func(context.Context) (map[string]any, error) {
			return cfgWith("其他",
				ob("direct", "直连"),
				ob("selector", "其他", "outbounds", []any{"自动", "直连"}, "default", "节点B"),
				ob("urltest", "自动", "outbounds", []any{"节点A"}),
				ob("http", "节点A", "server", "1.1.1.1"),
				ob("http", "节点B", "server", "2.2.2.2", "detour", "节点A")), nil
		},
	}
}

func TestTest_InputErrors(t *testing.T) {
	tt := newTester(&fakeQuery{})
	for name, req := range map[string]Request{
		"side 为空":  {Server: "1.1.1.1"},
		"side 不认识": {Side: "both", Server: "1.1.1.1"},
		"域名不收":     {Side: "direct", Server: "dns.google"},
		"协议不支持":    {Side: "direct", Server: "1.1.1.1", Protocol: "tls"},
		"端口太大":     {Side: "direct", Server: "1.1.1.1", Port: 70000},
		"端口为负":     {Side: "direct", Server: "1.1.1.1", Port: -1},
		"0.0.0.0":  {Side: "direct", Server: "0.0.0.0"},
		"空地址":      {Side: "direct"},
	} {
		_, err := tt.Test(context.Background(), req)
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%s: 想要 *InputError,得到 %v", name, err)
		}
	}
}

func TestTest_Direct(t *testing.T) {
	fq := &fakeQuery{res: probe.DNSResult{OK: true, Ms: 23}}
	res, err := newTester(fq).Test(context.Background(), Request{Side: "direct", Server: " [2001:4860:4860::8888] ", Protocol: "UDP", Port: 5353})
	if err != nil {
		t.Fatal(err)
	}
	want := Result{OK: true, Ms: 23, Server: "2001:4860:4860::8888", Protocol: "udp", Port: 5353}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("res = %+v", res)
	}
	j := fq.got[0]
	if j.Detour != "" || j.Material != nil || j.Name != "connectivitycheck.platform.hicloud.com" {
		t.Errorf("直连侧任务不对: %+v", j)
	}
	if j.Server["type"] != "udp" || j.Server["server"] != "2001:4860:4860::8888" || j.Server["server_port"] != 5353 {
		t.Errorf("上游对象不对: %v", j.Server)
	}

	// 默认端口不写 server_port;协议缺省:直连 udp
	fq.got = nil
	_, _ = newTester(fq).Test(context.Background(), Request{Side: "direct", Server: "223.5.5.5"})
	if _, has := fq.got[0].Server["server_port"]; has || fq.got[0].Server["type"] != "udp" {
		t.Errorf("默认值不对: %v", fq.got[0].Server)
	}
}

func TestTest_Wan(t *testing.T) {
	fq := &fakeQuery{res: probe.DNSResult{OK: true, Ms: 5}}
	// 代理侧写 wan 也是直连、固定 UDP 53,请求里的协议端口不看,也不生成配置
	tt := newTester(fq)
	tt.Config = func(context.Context) (map[string]any, error) { t.Fatal("wan 不该生成配置"); return nil, nil }
	res, err := tt.Test(context.Background(), Request{Side: "proxy", Server: "wan", Protocol: "tcp", Port: 853})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Wan || res.Server != "192.168.1.1" || res.Protocol != "udp" || res.Port != 53 || res.Via != "" || res.Note != "" {
		t.Errorf("res = %+v", res)
	}
	if fq.got[0].Detour != "" || fq.got[0].Server["type"] != "udp" {
		t.Errorf("任务不对: %+v", fq.got[0])
	}

	// 读不到系统上游:按地区退回,并在 note 里说明
	for region, want := range map[string]string{"cn": "223.5.5.5", "intl": "1.1.1.1", "": "223.5.5.5"} {
		tt := newTester(&fakeQuery{res: probe.DNSResult{OK: true, Ms: 1}})
		tt.WAN = func() []string { return nil }
		r := region
		tt.Region = func(context.Context) string { return r }
		res, _ := tt.Test(context.Background(), Request{Side: "direct", Server: "wan"})
		if res.Server != want || !strings.Contains(res.Note, want) {
			t.Errorf("地区 %q: %+v", region, res)
		}
	}
}

func TestTest_Proxy(t *testing.T) {
	fq := &fakeQuery{res: probe.DNSResult{OK: true, Ms: 88}}
	res, err := newTester(fq).Test(context.Background(), Request{Side: "proxy", Server: "1.1.1.1", Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	// 兜底「其他」是 selector,默认「节点B」,「节点B」的 detour 是「节点A」
	if res.Policy != "其他" || res.Via != "节点B" || !reflect.DeepEqual(res.Chain, []string{"节点A", "节点B"}) || res.Note != "" {
		t.Errorf("线路不对: %+v", res)
	}
	j := fq.got[0]
	if j.Detour != "其他" || j.Material == nil || !j.Material.Has("节点B") || j.Name != "www.gstatic.com" {
		t.Errorf("任务不对: %+v", j)
	}
	if j.Server["type"] != "tcp" {
		t.Errorf("上游对象不对: %v", j.Server)
	}
}

func TestTest_ProxyNoExit(t *testing.T) {
	fq := &fakeQuery{}
	tt := newTester(fq)
	tt.Config = func(context.Context) (map[string]any, error) { return cfgWith("", ob("direct", "直连")), nil }
	res, err := tt.Test(context.Background(), Request{Side: "proxy", Server: "1.1.1.1"})
	if err != nil || res.OK || !strings.Contains(res.Error, "找不到代理出口") || res.Server != "1.1.1.1" || len(fq.got) != 0 {
		t.Errorf("res=%+v err=%v 查询=%d", res, err, len(fq.got))
	}
}

func TestTest_Errors(t *testing.T) {
	ctx := context.Background()
	// 没有 sing-box → 503
	tt := newTester(&fakeQuery{err: probe.ErrNoSingBox})
	_, err := tt.Test(ctx, Request{Side: "direct", Server: "1.1.1.1"})
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Errorf("想要 *UnavailableError: %v", err)
	}
	// 核心不可用 → 503;别的生成错误是普通错误
	tt = newTester(&fakeQuery{})
	tt.Config = func(context.Context) (map[string]any, error) { return nil, singbox.ErrCoreUnavailable }
	if _, err = tt.Test(ctx, Request{Side: "proxy", Server: "1.1.1.1"}); !errors.As(err, &ue) {
		t.Errorf("核心不可用想要 *UnavailableError: %v", err)
	}
	tt.Config = func(context.Context) (map[string]any, error) { return nil, errors.New("坏了") }
	if _, err = tt.Test(ctx, Request{Side: "proxy", Server: "1.1.1.1"}); err == nil || errors.As(err, &ue) {
		t.Errorf("普通错误不该是 503: %v", err)
	}
	// 测了没通:正常结果,错误原因原样带回
	tt = newTester(&fakeQuery{res: probe.DNSResult{Error: "超时:8 秒内没有答复", Reason: "timeout"}})
	res, err := tt.Test(ctx, Request{Side: "direct", Server: "10.255.255.1"})
	if err != nil || res.OK || res.Error != "超时:8 秒内没有答复" || res.Server != "10.255.255.1" {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestTestName(t *testing.T) {
	ctx := context.Background()
	tt := newTester(&fakeQuery{})
	set := func(p map[string]any, err error) {
		tt.Profile = func(context.Context) (map[string]any, error) { return p, err }
	}
	set(map[string]any{"testUrl": "http://example.org:8080/x", "directTestUrl": " https://www.baidu.com/ "}, nil)
	if g := tt.testName(ctx, "proxy"); g != "example.org" {
		t.Errorf("proxy = %q", g)
	}
	if g := tt.testName(ctx, "direct"); g != "www.baidu.com" {
		t.Errorf("direct = %q", g)
	}
	for _, p := range []map[string]any{{}, {"testUrl": "http://1.2.3.4/x", "directTestUrl": "not a url"}, {"testUrl": 5}} {
		set(p, nil)
		if tt.testName(ctx, "proxy") != defaultProxyNS || tt.testName(ctx, "direct") != defaultDirNS {
			t.Errorf("取不到该用默认值: %v", p)
		}
	}
	set(nil, errors.New("读不到"))
	if tt.testName(ctx, "proxy") != defaultProxyNS {
		t.Error("档案读不到该用默认值")
	}
	tt.Profile = nil
	if tt.testName(ctx, "direct") != defaultDirNS {
		t.Error("没接档案该用默认值")
	}
}

func TestDescribePath(t *testing.T) {
	cfg := cfgWith("组",
		ob("direct", "直连"), ob("block", "拒绝"),
		ob("selector", "组", "outbounds", []any{"直连", "子组"}),
		ob("selector", "子组", "outbounds", []any{"节点"}, "default", "节点"),
		ob("http", "节点"),
		ob("selector", "空组"),
		ob("selector", "自指", "outbounds", []any{"自指"}),
		ob("urltest", "自动", "outbounds", []any{"节点"}),
		ob("http", "尾", "detour", "头"), ob("http", "头"),
		ob("http", "悬空", "detour", "不存在"),
	)
	cases := []struct {
		tag, via string
		chain    []string
		note     string // 非空表示 note 里要含这段
	}{
		{"组", "", nil, "直连"}, // 没写 default 取第一个成员,是 direct
		{"子组", "节点", nil, ""},
		{"节点", "节点", nil, ""},
		{"尾", "尾", []string{"头", "尾"}, ""},
		{"悬空", "悬空", nil, ""},
		{"自动", "自动", nil, "自动选择组"},
		{"空组", "空组", nil, ""},
		{"自指", "自指", nil, ""},
		{"不存在", "不存在", nil, ""},
	}
	for _, c := range cases {
		via, chain, note := describePath(cfg, c.tag)
		if via != c.via || !reflect.DeepEqual(chain, c.chain) || (c.note == "") != (note == "") || !strings.Contains(note, c.note) {
			t.Errorf("%s: via=%q chain=%v note=%q", c.tag, via, chain, note)
		}
	}
}
