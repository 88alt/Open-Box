package singbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
)

type fakeFilter struct {
	art     map[string]any
	key     string
	enabled bool
	ready   bool
}

func (f fakeFilter) CoreArtifact(context.Context, map[string]any) (map[string]any, string, bool, bool, error) {
	return f.art, f.key, f.enabled, f.ready, nil
}

func filterEnv(t *testing.T) (*env, dnsfilter.Settings, *dnsfilter.Artifact) {
	t.Helper()
	e := newEnv(t, System{})
	e.addSub(t, "A", twoNodes)
	s := dnsfilter.Default()
	s.Enabled = true
	s.Lists = []dnsfilter.List{{ID: "a", Name: "A", URL: "https://x/a", Enabled: true}}
	if _, err := e.prof.Patch(context.Background(), map[string]any{"dns": map[string]any{"filter": s.ToMap()}}); err != nil {
		t.Fatal(err)
	}
	p, err := dnsfilter.ParseList([]byte("||ads.test^\n@@||ok.ads.test^\n"))
	if err != nil {
		t.Fatal(err)
	}
	return e, s, dnsfilter.Compile(s, map[string]*dnsfilter.Parsed{"a": p})
}

func TestBuildPassesFilterArtifactToCore(t *testing.T) {
	e, s, art := filterEnv(t)
	e.gen.WithFilter(fakeFilter{art: art.ForCore("/data/rules"), key: dnsfilter.SettingsKey(s), enabled: true, ready: true})
	b, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b.Config)
	for _, g := range art.Groups {
		if !strings.Contains(string(raw), g.Tag) {
			t.Errorf("配置里没有过滤规则集 %s", g.Tag)
		}
	}
	if !strings.Contains(string(raw), "/data/rules/"+art.Groups[0].Tag+".srs") {
		t.Error("规则集路径不对")
	}
}

func TestBuildFilterNotReadyDoesNotBreakBuild(t *testing.T) {
	e, s, _ := filterEnv(t)
	for name, src := range map[string]FilterSource{
		"没接过滤来源":  nil,
		"还没按设置应用": fakeFilter{key: dnsfilter.SettingsKey(s), enabled: true, ready: false},
	} {
		if src != nil {
			e.gen.WithFilter(src)
		}
		b, err := e.gen.Build(context.Background())
		if err != nil {
			t.Fatalf("%s: 不该报错: %v", name, err)
		}
		if raw, _ := json.Marshal(b.Config); strings.Contains(string(raw), dnsfilter.TagPrefix) {
			t.Errorf("%s: 过滤没生效时配置里不该有过滤规则集", name)
		}
	}
	// 档案本身不能被改动(只是生成时当作关)
	prof, _ := e.prof.Get(context.Background())
	if !dnsFilterEnabled(prof) {
		t.Error("不该改动档案里的过滤开关")
	}
}
