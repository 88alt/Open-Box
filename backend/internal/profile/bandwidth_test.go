package profile

import "testing"

func TestBandwidthDefaultAndCheck(t *testing.T) {
	bw := Default()["bandwidth"].(map[string]any)
	if bw["intervalHours"] != float64(2) || bw["maxDownloadMB"] != float64(64) {
		t.Errorf("默认值应是 2 小时 / 64 MB: %v", bw)
	}
	mk := func(h, mb any) map[string]any {
		p := Default()
		p["bandwidth"] = map[string]any{"intervalHours": h, "maxDownloadMB": mb}
		return p
	}
	for _, c := range [][2]any{{float64(1), float64(1)}, {float64(24), float64(1024)}, {float64(2), float64(64)}} {
		if err := Check(mk(c[0], c[1])); err != nil {
			t.Errorf("%v 应通过: %v", c, err)
		}
	}
	for _, c := range [][2]any{{float64(0), float64(64)}, {float64(25), float64(64)}, {float64(2), float64(0)}, {float64(2), float64(1025)}, {1.5, float64(64)}, {"2", float64(64)}, {float64(2), nil}} {
		if err := Check(mk(c[0], c[1])); err == nil {
			t.Errorf("%v 应被拒绝", c)
		}
	}
	// 缺的键不报错;不是对象才报错
	p := Default()
	p["bandwidth"] = map[string]any{"intervalHours": float64(6)}
	if err := Check(p); err != nil {
		t.Error(err)
	}
	p["bandwidth"] = "x"
	if err := Check(p); err == nil {
		t.Error("bandwidth 不是对象应被拒绝")
	}
}
