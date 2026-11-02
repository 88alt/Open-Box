package nodegroups

import "testing"

func TestBandwidthFlagMustBeBool(t *testing.T) {
	g := func(v any) []any {
		return []any{map[string]any{"id": "g1", "name": "G", "type": "urltest", "bandwidth": v}}
	}
	for _, ok := range []any{true, false} {
		if err := checkBandwidthFlags(g(ok)); err != nil {
			t.Errorf("%v 应通过: %v", ok, err)
		}
	}
	for _, bad := range []any{"true", 1, 0, nil, []any{}} {
		wantBad(t, checkBandwidthFlags(g(bad)), "bandwidth 必须是 true 或 false")
	}
	// 没写这个键不受影响
	if err := checkBandwidthFlags([]any{map[string]any{"id": "x", "name": "X", "type": "selector"}}); err != nil {
		t.Error(err)
	}
}
