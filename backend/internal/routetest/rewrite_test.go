package routetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/dnsrewrite"
)

func TestAnnotateRewrite(t *testing.T) {
	rules := []dnsrewrite.Rule{{Source: "a.com", Domain: "b.com"}}
	mk := func(tag string) *dnsdecide.Decision { return &dnsdecide.Decision{Server: &dnsdecide.Server{Tag: tag}} }
	p := &DNSPredictor{Rewrites: func(context.Context) ([]dnsrewrite.Rule, error) { return rules, nil }}
	if d := mk(dnsrewrite.Tag); func() bool {
		p.annotateRewrite(context.Background(), d, "a.com")
		return d.Rewrite == nil || d.Rewrite.Domain != "b.com"
	}() {
		t.Fatal("命中重写服务器应补规则")
	}
	if d := mk("other"); func() bool { p.annotateRewrite(context.Background(), d, "a.com"); return d.Rewrite != nil }() {
		t.Fatal("没命中重写服务器不该补")
	}
	if d := mk(dnsrewrite.Tag); func() bool { p.annotateRewrite(context.Background(), d, "zzz.com"); return d.Rewrite != nil }() {
		t.Fatal("档案里没有对应规则时不补")
	}
	p.Rewrites = func(context.Context) ([]dnsrewrite.Rule, error) { return nil, errors.New("x") }
	if d := mk(dnsrewrite.Tag); func() bool { p.annotateRewrite(context.Background(), d, "a.com"); return d.Rewrite != nil }() {
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
	p := &DNSPredictor{ConfigFile: cfg, Rewrites: func(context.Context) ([]dnsrewrite.Rule, error) {
		return []dnsrewrite.Rule{
			{Source: "services.googleapis.cn", Domain: "services.googleapis.com"},
			{Source: "*.lan.test", Addresses: []string{"10.0.0.2"}},
		}, nil
	}}
	d, err := p.Predict(context.Background(), "services.googleapis.cn")
	if err != nil || d.Server == nil || d.Server.Tag != dnsrewrite.Tag || d.Rewrite == nil ||
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
