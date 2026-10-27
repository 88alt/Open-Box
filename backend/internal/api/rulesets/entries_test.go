package rulesets

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

// fakeGeo 按分类名返回预设结果或错误,并记录被问过的分类名。
type fakeGeo struct {
	byTag map[string]*geodata.Result
	errs  map[string]error
	asked []string
}

func (f *fakeGeo) Entries(_ context.Context, tag string) (*geodata.Result, error) {
	f.asked = append(f.asked, tag)
	if err := f.errs[tag]; err != nil {
		return nil, err
	}
	if r := f.byTag[tag]; r != nil {
		return r, nil
	}
	return nil, geodata.ErrNotFound
}

func geoHandler(g GeoEntries) http.Handler {
	mux := http.NewServeMux()
	Register(mux, Deps{Lists: rulelist.NewService(nil), Geo: g})
	return mux
}

func domains(n int) *geodata.Result {
	r := &geodata.Result{Entries: []rulelist.Entry{}}
	for i := 0; i < n; i++ {
		r.Entries = append(r.Entries, rulelist.Entry{Type: "domain_suffix", Value: fmt.Sprintf("site%03d.example.com", i)})
	}
	return r
}

func TestEntriesPagingFilterAndShape(t *testing.T) {
	g := &fakeGeo{byTag: map[string]*geodata.Result{"geosite-cn": domains(120), "geoip-empty": {Entries: []rulelist.Entry{}}}}
	h := geoHandler(g)

	code, m := get(h, "GET", "/api/openbox/rulesets/entries?tag=geosite-cn")
	if code != 200 || m["tag"] != "geosite-cn" || m["total"] != float64(120) || m["matched"] != float64(120) ||
		m["offset"] != float64(0) || m["limit"] != float64(50) || len(m["entries"].([]any)) != 50 {
		t.Fatalf("%d %v", code, m)
	}
	if _, has := m["truncated"]; has {
		t.Fatal("没有被截断时不该带 truncated")
	}
	first := m["entries"].([]any)[0].(map[string]any)
	if first["type"] != "domain_suffix" || first["value"] != "site000.example.com" {
		t.Fatalf("条目字段名应与 preview 一致: %v", first)
	}

	// 最后一页
	_, m = get(h, "GET", "/api/openbox/rulesets/entries?tag=geosite-cn&offset=100&limit=50")
	if len(m["entries"].([]any)) != 20 || m["offset"] != float64(100) {
		t.Fatalf("%v", m)
	}
	// 搜索:total 不变,matched 是命中数(不区分大小写)
	_, m = get(h, "GET", "/api/openbox/rulesets/entries?tag=geosite-cn&q=SITE07")
	if m["total"] != float64(120) || m["matched"] != float64(10) || len(m["entries"].([]any)) != 10 {
		t.Fatalf("%v", m)
	}
	// limit 夹到 [1, 500],offset 夹到命中数以内
	_, m = get(h, "GET", "/api/openbox/rulesets/entries?tag=geosite-cn&limit=99999&offset=9999")
	if m["limit"] != float64(maxLimit) || m["offset"] != float64(120) || len(m["entries"].([]any)) != 0 {
		t.Fatalf("%v", m)
	}
	// 前端用 limit=1 取总数
	_, m = get(h, "GET", "/api/openbox/rulesets/entries?tag=geosite-cn&limit=1")
	if m["total"] != float64(120) || len(m["entries"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
	// 空分类:entries 是 [] 而不是 null
	code, m = get(h, "GET", "/api/openbox/rulesets/entries?tag=geoip-empty")
	if code != 200 || m["total"] != float64(0) {
		t.Fatalf("%d %v", code, m)
	}
	if list, ok := m["entries"].([]any); !ok || len(list) != 0 {
		t.Fatalf("空分类的 entries 应是空数组: %#v", m["entries"])
	}
	// 搜不到:同样是空数组
	_, m = get(h, "GET", "/api/openbox/rulesets/entries?tag=geosite-cn&q=nothing-like-this")
	if list, ok := m["entries"].([]any); !ok || len(list) != 0 || m["matched"] != float64(0) {
		t.Fatalf("%v", m)
	}
}

func TestEntriesTruncatedFlag(t *testing.T) {
	r := domains(3)
	r.Truncated = true
	_, m := get(geoHandler(&fakeGeo{byTag: map[string]*geodata.Result{"geosite-big": r}}), "GET", "/api/openbox/rulesets/entries?tag=geosite-big")
	if m["truncated"] != true || m["total"] != float64(3) {
		t.Fatalf("%v", m)
	}
}

func TestEntriesErrors(t *testing.T) {
	g := &fakeGeo{
		byTag: map[string]*geodata.Result{"geosite-cn": domains(1)},
		errs: map[string]error{
			"geosite-bad":    geodata.ErrInvalidTag,
			"geosite-nolib":  geodata.ErrNoLibrary,
			"geosite-nosb":   geodata.ErrNoSingBox,
			"geosite-broken": &geodata.DecodeError{Msg: "sing-box 解码 geosite-broken.srs 失败: bad magic"},
			"geosite-cancel": context.Canceled,
			"geosite-other":  errors.New("磁盘坏了"),
		},
	}
	h := geoHandler(g)
	cases := []struct {
		query string
		code  int
		want  string // 错误文案里应有的片段
	}{
		{"", 400, "tag"},
		{"?tag=", 400, "tag"},
		{"?tag=%20%20", 400, "tag"},
		{"?tag=geosite-bad", 400, "分类名不合法"},
		{"?tag=geosite-missing", 404, "geosite-missing"},
		{"?tag=geosite-nolib", 503, "规则库目录"},
		{"?tag=geosite-nosb", 503, "sing-box"},
		{"?tag=geosite-broken", 500, "bad magic"},
		{"?tag=geosite-cancel", 503, "取消"},
		{"?tag=geosite-other", 500, "磁盘坏了"},
	}
	for _, c := range cases {
		code, m := get(h, "GET", "/api/openbox/rulesets/entries"+c.query)
		msg, _ := m["error"].(string)
		if code != c.code || !strings.Contains(msg, c.want) {
			t.Errorf("%q: %d %v,想要 %d 且含 %q", c.query, code, m, c.code, c.want)
		}
	}
	if len(g.asked) > 0 && g.asked[0] == "" {
		t.Fatal("没带 tag 不该去问规则库")
	}
	for _, a := range g.asked {
		if strings.TrimSpace(a) == "" {
			t.Fatalf("空 tag 不该走到规则库: %q", g.asked)
		}
	}
	if code, _ := get(h, "POST", "/api/openbox/rulesets/entries?tag=geosite-cn"); code != http.StatusMethodNotAllowed {
		t.Fatalf("entries 只收 GET: %d", code)
	}
}

func TestEntriesTrimsTag(t *testing.T) {
	g := &fakeGeo{byTag: map[string]*geodata.Result{"geosite-cn": domains(2)}}
	code, m := get(geoHandler(g), "GET", "/api/openbox/rulesets/entries?tag=%20geosite-cn%20")
	if code != 200 || m["tag"] != "geosite-cn" || len(g.asked) != 1 || g.asked[0] != "geosite-cn" {
		t.Fatalf("%d %v %v", code, m, g.asked)
	}
}
