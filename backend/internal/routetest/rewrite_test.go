package routetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
)

func profile(rw map[string]any) map[string]any {
	return map[string]any{"dns": map[string]any{"rewrite": rw}}
}

func TestRewriteRulesFromProfile(t *testing.T) {
	got := RewriteRulesFromProfile(profile(map[string]any{"rules": []any{
		map[string]any{"source": "Services.GoogleAPIs.cn.", "domain": "services.googleapis.com"},
		map[string]any{"source": "*.a.com", "addresses": []any{"1.2.3.4", " ::1 ", 5, ""}},
		map[string]any{"source": "off.com", "domain": "x.com", "enabled": false},
		map[string]any{"source": "both.com", "domain": "x.com", "addresses": []any{"9.9.9.9"}},
		map[string]any{"source": "empty.com"},
		map[string]any{"domain": "nosrc.com"},
		"garbage",
	}}))
	want := []RewriteRule{
		{Source: "services.googleapis.cn", Domain: "services.googleapis.com"},
		{Source: "*.a.com", Addresses: []string{"1.2.3.4", "::1"}},
		{Source: "both.com", Domain: "x.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if RewriteRulesFromProfile(profile(map[string]any{"enabled": false, "rules": []any{map[string]any{"source": "a.com", "domain": "b.com"}}})) != nil {
		t.Fatal("总开关关了不该有规则")
	}
	if RewriteRulesFromProfile(map[string]any{}) != nil {
		t.Fatal("没有 dns 段不该有规则")
	}
}

func TestMatchRewrite(t *testing.T) {
	rules := []RewriteRule{
		{Source: "*.a.com", Addresses: []string{"1.1.1.1"}},
		{Source: "*.b.a.com", Domain: "t.com"},
		{Source: "x.b.a.com", Addresses: []string{"2.2.2.2"}},
	}
	cases := map[string]struct {
		source string
		none   bool
	}{
		"x.b.a.com": {source: "x.b.a.com"}, // 精确优先
		"y.b.a.com": {source: "*.b.a.com"}, // 更具体的泛域名优先
		"z.a.com.":  {source: "*.a.com"},   // 末尾的点
		"Z.A.COM":   {source: "*.a.com"},   // 大小写
		"a.com":     {none: true},          // *.a.com 只管子域,和配置里的 .a.com 后缀一致
		"other.com": {none: true},
		"nota.com":  {none: true},
		"xa.com":    {none: true},
	}
	for d, c := range cases {
		h := matchRewrite(rules, d)
		if c.none != (h == nil) || (h != nil && h.Source != c.source) {
			t.Errorf("%s: %+v", d, h)
		}
	}
	if h := matchRewrite(rules, "y.b.a.com"); h.Addresses == nil || len(h.Addresses) != 0 || h.Domain != "t.com" {
		t.Errorf("域名型的 addresses 要是空数组: %+v", h)
	}
}

func TestAnnotateRewrite(t *testing.T) {
	rules := []RewriteRule{{Source: "a.com", Domain: "b.com"}}
	mk := func(tag string) *dnsdecide.Decision { return &dnsdecide.Decision{Server: &dnsdecide.Server{Tag: tag}} }
	p := &DNSPredictor{Rewrites: func(context.Context) ([]RewriteRule, error) { return rules, nil }}
	if d := mk(RewriteTag); func() bool {
		p.annotateRewrite(context.Background(), d, "a.com")
		return d.Rewrite == nil || d.Rewrite.Domain != "b.com"
	}() {
		t.Fatal("命中重写服务器应补规则")
	}
	if d := mk("other"); func() bool { p.annotateRewrite(context.Background(), d, "a.com"); return d.Rewrite != nil }() {
		t.Fatal("没命中重写服务器不该补")
	}
	if d := mk(RewriteTag); func() bool { p.annotateRewrite(context.Background(), d, "zzz.com"); return d.Rewrite != nil }() {
		t.Fatal("档案里没有对应规则时不补")
	}
	p.Rewrites = func(context.Context) ([]RewriteRule, error) { return nil, errors.New("x") }
	if d := mk(RewriteTag); func() bool { p.annotateRewrite(context.Background(), d, "a.com"); return d.Rewrite != nil }() {
		t.Fatal("读档案失败不补")
	}
	(&DNSPredictor{}).annotateRewrite(context.Background(), &dnsdecide.Decision{}, "a.com") // 空 Server、nil 回调不崩
}

func TestPredictReportsRewriteHit(t *testing.T) {
	// 配置形状和 core 生成的一致:重写规则排在 dns.rules 最前面,交给 dns-rewrite 服务器
	cfg := filepath.Join(t.TempDir(), "config.json")
	js := `{"dns":{"final":"dns-direct","servers":[
	  {"type":"udp","tag":"dns-direct","server":"223.5.5.5"},
	  {"type":"udp","tag":"dns-rewrite","server":"127.0.0.1","server_port":7854}],
	 "rules":[{"domain":["services.googleapis.cn"],"server":"dns-rewrite"},{"domain_suffix":[".lan.test"],"server":"dns-rewrite"}]},
	 "route":{"rules":[]},"outbounds":[{"tag":"direct","type":"direct"}]}`
	if err := os.WriteFile(cfg, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &DNSPredictor{ConfigFile: cfg, Rewrites: func(context.Context) ([]RewriteRule, error) {
		return []RewriteRule{
			{Source: "services.googleapis.cn", Domain: "services.googleapis.com"},
			{Source: "*.lan.test", Addresses: []string{"10.0.0.2"}},
		}, nil
	}}
	d, err := p.Predict(context.Background(), "services.googleapis.cn")
	if err != nil || d.Server == nil || d.Server.Tag != RewriteTag || d.Rewrite == nil ||
		d.Rewrite.Source != "services.googleapis.cn" || d.Rewrite.Domain != "services.googleapis.com" || len(d.Rewrite.Addresses) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
	d, _ = p.Predict(context.Background(), "nas.lan.test")
	if d.Rewrite == nil || d.Rewrite.Source != "*.lan.test" || !reflect.DeepEqual(d.Rewrite.Addresses, []string{"10.0.0.2"}) {
		t.Fatalf("%+v", d.Rewrite)
	}
	if d, _ = p.Predict(context.Background(), "baidu.com"); d.Rewrite != nil {
		t.Fatalf("没命中重写不该带 rewrite: %+v", d.Rewrite)
	}
}
