package routetest

import (
	"context"
	"testing"
	"time"
)

// 设备现象(sing-box ipv6:false):A 查询正常、**AAAA 查询完全不被应答**(内核直接丢弃)。
// 这时 resolve 不能因为 AAAA 超时就把整次解析判成失败 —— A 已经拿到地址了。
// 这组测试盯住「A 成功 + AAAA 无应答」这个组合。
func TestResolveAsucceedsWhenAAAAIsDropped(t *testing.T) {
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return nil // 丢弃:内核 ipv6:false 时的真实行为
		}
		return reply(req, 0, false, a4("1.1.1.1", 300))
	})
	s := &Service{DNSAddr: f.addr, Timeout: 1 * time.Second}
	r := s.resolve(context.Background(), "one.one.one.one", true)

	if !r.OK || len(r.Answers) != 1 || r.Answers[0] != "1.1.1.1" {
		t.Fatalf("A 记录拿到了就该 OK:%+v", r)
	}
	if r.Error != "" {
		t.Fatalf("A 成功时 Error 应为空:%q", r.Error)
	}
	// AAAA 侧可以报「超时」,但不能影响 A 的结论
	if r.OK6 != nil && *r.OK6 {
		t.Fatalf("AAAA 没应答不该标成功:%+v", r)
	}
	if len(r.Answers6) != 0 {
		t.Fatalf("AAAA 不该有地址:%+v", r)
	}
}

// ResolveName 是 penetration 用的入口:它的 errText 只反映 A 的错误。
// AAAA 被丢弃时,ResolveName 必须照常回 A 的地址、errText 为空。
func TestResolveNameIgnoresDroppedAAAA(t *testing.T) {
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return nil
		}
		return reply(req, 0, false, a4("1.1.1.1", 300))
	})
	s := &Service{DNSAddr: f.addr, Timeout: 1 * time.Second, IPv6: func(context.Context) bool { return true }}

	fakeIP, addrs, errText := s.ResolveName(context.Background(), "one.one.one.one")
	if errText != "" {
		t.Fatalf("A 成功时不该有错误文案:%q", errText)
	}
	if len(addrs) != 1 || addrs[0] != "1.1.1.1" {
		t.Fatalf("应回 A 的地址:%v", addrs)
	}
	if fakeIP {
		t.Fatal("1.1.1.1 不在 fake-ip 池里")
	}
}
