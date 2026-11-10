package dnsforward

import (
	"context"
	"errors"
	"testing"
)

func svc(mode string, modeErr error, applied bool) *Service {
	return &Service{
		Mode:       func(context.Context) (string, error) { return mode, modeErr },
		Applied:    func(context.Context) bool { return applied },
		KernelAddr: "127.0.0.1:7853",
	}
}

func TestDescribeTakeoverAppliedIsKernelAll(t *testing.T) {
	got := svc("dnsmasq", nil, true).Describe(context.Background())
	if got == nil || got.Forward != "kernel" || got.Plan != "all" || got.To != "127.0.0.1:7853" {
		t.Fatalf("接管生效应回 kernel / all / 内核地址:%+v", got)
	}
}

func TestDescribeUnsureCasesReturnNil(t *testing.T) {
	cases := map[string]*Service{
		"模式 off":      svc("off", nil, true),
		"模式 hijack":   svc("hijack", nil, true),
		"未知模式":        svc("whatever", nil, true),
		"模式为空":        svc("", nil, true),
		"接管没生效":       svc("dnsmasq", nil, false),
		"档案读不到":       svc("", errors.New("boom"), true),
		"没接 Mode":     {Applied: func(context.Context) bool { return true }},
		"没接 Applied":  {Mode: func(context.Context) (string, error) { return "dnsmasq", nil }},
		"nil Service": nil,
	}
	for name, s := range cases {
		if got := s.Describe(context.Background()); got != nil {
			t.Errorf("%s:拿不准要返回 nil,得到 %+v", name, got)
		}
	}
}

func TestDescribeDoesNotAskTakeoverWhenModeIsNotDnsmasq(t *testing.T) {
	asked := false
	s := &Service{
		Mode:    func(context.Context) (string, error) { return "off", nil },
		Applied: func(context.Context) bool { asked = true; return true },
	}
	if s.Describe(context.Background()) != nil || asked {
		t.Fatal("模式不是 dnsmasq 就不用去查接管现状")
	}
}
