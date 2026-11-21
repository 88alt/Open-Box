package scoreweights

import "testing"

func TestNormalize(t *testing.T) {
	if w, err := Normalize(nil); err != nil || w != Default() {
		t.Fatalf("nil → default, got %v %v", w, err)
	}
	w, err := Normalize(map[string]any{"latency": 50.0, "download": 20.0, "jitter": 30.0})
	if err != nil || w.Latency != 50 || w.Download != 20 || w.Jitter != 30 {
		t.Fatalf("ok case: %v %v", w, err)
	}
	// 滑块算出的浮点小数
	if _, err := Normalize(map[string]any{"latency": 33.3333333333, "download": 33.3333333333, "jitter": 33.3333333334}); err != nil {
		t.Fatalf("float sum: %v", err)
	}
	bad := []any{
		"x",
		map[string]any{"latency": 50.0, "download": 50.0},
		map[string]any{"latency": 50.0, "download": 30.0, "jitter": 30.0},
		map[string]any{"latency": -10.0, "download": 60.0, "jitter": 50.0},
		map[string]any{"latency": 101.0, "download": 0.0, "jitter": -1.0},
		map[string]any{"latency": "40", "download": 30.0, "jitter": 30.0},
	}
	for i, b := range bad {
		if _, err := Normalize(b); err == nil {
			t.Fatalf("bad[%d] should fail", i)
		}
	}
}
