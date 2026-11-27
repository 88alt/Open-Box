package probe

import "testing"

// 上游 v0.1.493 #1009:测速站拒绝要能区分 403(封禁)与 429(限流),
// 前端用 latencyReasonForbidden / latencyReasonRateLimited 两条文案显示。
func TestReasonForStatus(t *testing.T) {
	cases := map[int]string{
		403: ReasonForbidden,
		429: ReasonRateLimited,
		500: "failed",
		404: "failed",
		200: "failed", // 2xx 不该走到这里,但函数本身按「不认识」处理
	}
	for code, want := range cases {
		if got := reasonForStatus(code); got != want {
			t.Errorf("%d → %q,想要 %q", code, got, want)
		}
	}
}

// 常量值必须与前端 LATENCY_REASON_KEYS 的键一致,否则界面上会显示成默认的「超时」。
func TestReasonConstantsMatchFrontendKeys(t *testing.T) {
	if ReasonForbidden != "forbidden" {
		t.Fatalf("ReasonForbidden = %q,前端认的是 \"forbidden\"", ReasonForbidden)
	}
	if ReasonRateLimited != "rate_limited" {
		t.Fatalf("ReasonRateLimited = %q,前端认的是 \"rate_limited\"", ReasonRateLimited)
	}
}
