package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

type fakeProfiles struct {
	p   map[string]any
	err error
}

func (f fakeProfiles) Get(context.Context) (map[string]any, error) { return f.p, f.err }

type fakeGroups struct {
	g   []any
	err error
}

func (f fakeGroups) Groups(context.Context) ([]any, error) { return f.g, f.err }

type fakeSubs struct {
	recs  []subs.Record
	nodes map[string][]sub.Node
	err   error
}

func (f fakeSubs) Dump(context.Context) ([]subs.Record, map[string][]sub.Node, error) {
	return f.recs, f.nodes, f.err
}

type fakePanel struct {
	set    map[string]string
	img    string
	setErr error
	imgErr error
}

func (f fakePanel) Settings(context.Context) (map[string]string, error) { return f.set, f.setErr }
func (f fakePanel) BackgroundImage(context.Context) (string, error)     { return f.img, f.imgErr }

func newSvc() *Service {
	return &Service{
		Profiles: fakeProfiles{p: map[string]any{
			"region": "cn", "ipv6": false,
			"chainProxies": []any{map[string]any{"name": "链"}},
			"clientRoutes": []any{map[string]any{"id": "c1"}},
			"servers":      []any{map[string]any{"id": "s1"}},
		}},
		Groups: fakeGroups{g: []any{map[string]any{"id": "auto", "name": "自动选择"}}},
		Subs: fakeSubs{
			recs: []subs.Record{{ID: "b", Name: "B", URL: "https://b.test/sub?token=1", Enabled: true}, {ID: "a", Name: "A"}},
			nodes: map[string][]sub.Node{
				"a": {{Tag: "n1", Type: "trojan", Server: "a.test", ServerPort: 443, Fields: map[string]any{"password": "p"}}, {Tag: "n2", Type: "vless", Server: "b.test", ServerPort: 8443}},
				"b": {{Tag: "n3", Type: "ss", Server: "c.test", ServerPort: 1}},
			},
		},
		Panel:   fakePanel{set: map[string]string{"config/theme": "dark"}, img: "data:image/png;base64,AAAA"},
		Version: "0.9.9",
		Now:     func() time.Time { return time.Date(2026, 10, 31, 8, 30, 0, 0, time.FixedZone("x", 8*3600)) },
	}
}

func toMap(t *testing.T, b *Backup) map[string]any {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExportAllParts(t *testing.T) {
	b, err := newSvc().Export(context.Background(), Options{Subscriptions: true, ChainProxies: true, ClientRoutes: true, Servers: true})
	if err != nil {
		t.Fatal(err)
	}
	m := toMap(t, b)
	if m["format"] != "open-box-backup" || m["version"] != float64(1) || m["exportedAt"] != "2026-10-31T00:30:00Z" || m["openboxVersion"] != "0.9.9" {
		t.Fatalf("头不对:%v", m)
	}
	if inc := m["includes"].(map[string]any); inc["subscriptions"] != true || inc["chainProxies"] != true || inc["clientRoutes"] != true || inc["servers"] != true {
		t.Fatalf("%v", inc)
	}
	prof := m["profile"].(map[string]any)
	for _, k := range []string{"region", "chainProxies", "clientRoutes", "servers"} {
		if _, ok := prof[k]; !ok {
			t.Errorf("档案缺 %s", k)
		}
	}
	if len(m["groups"].([]any)) != 1 {
		t.Fatalf("%v", m["groups"])
	}
	subsList := m["subscriptions"].([]any)
	if len(subsList) != 2 || subsList[0].(map[string]any)["id"] != "b" {
		t.Fatalf("订阅要保持用户排序:%v", subsList)
	}
	nodes := m["nodes"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("%v", nodes)
	}
	// 节点按订阅顺序摊平:b 的在前;订阅 id 和节点字段在同一层
	n0 := nodes[0].(map[string]any)
	if n0["subscription"] != "b" || n0["tag"] != "n3" || n0["type"] != "ss" || n0["server"] != "c.test" || n0["server_port"] != float64(1) {
		t.Fatalf("%v", n0)
	}
	n1 := nodes[1].(map[string]any)
	if n1["subscription"] != "a" || n1["fields"].(map[string]any)["password"] != "p" {
		t.Fatalf("%v", n1)
	}
	if m["panelSettings"].(map[string]any)["config/theme"] != "dark" || m["backgroundImage"] != "data:image/png;base64,AAAA" {
		t.Fatalf("%v", m)
	}
}

func TestExportOptionalPartsAreOmittedNotEmptied(t *testing.T) {
	b, err := newSvc().Export(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := toMap(t, b)
	for _, k := range []string{"subscriptions", "nodes"} {
		if _, ok := m[k]; ok {
			t.Errorf("没勾订阅就不该有 %s 这个键", k)
		}
	}
	prof := m["profile"].(map[string]any)
	for _, k := range []string{"chainProxies", "clientRoutes", "servers"} {
		if _, ok := prof[k]; ok {
			t.Errorf("没勾的 %s 整个键不该在档案里(导入时表示保持现状)", k)
		}
	}
	if prof["region"] != "cn" {
		t.Fatal("别的档案内容照常带上")
	}
	if inc := m["includes"].(map[string]any); inc["subscriptions"] != false || inc["chainProxies"] != false {
		t.Fatalf("%v", inc)
	}
	// 一块一块单独勾
	for name, o := range map[string]Options{"chain": {ChainProxies: true}, "routes": {ClientRoutes: true}, "servers": {Servers: true}} {
		b, _ := newSvc().Export(context.Background(), o)
		p := toMap(t, b)["profile"].(map[string]any)
		got := []bool{p["chainProxies"] != nil, p["clientRoutes"] != nil, p["servers"] != nil}
		want := map[string][]bool{"chain": {true, false, false}, "routes": {false, true, false}, "servers": {false, false, true}}[name]
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s:%v,想要 %v", name, got, want)
			}
		}
	}
	// 不改动档案服务返回的原 map
	prof2, _ := newSvc().Profiles.Get(context.Background())
	if _, ok := prof2["chainProxies"]; !ok {
		t.Fatal("原 map 不该被改")
	}
}

func TestExportEmptyThingsAreArraysNotNull(t *testing.T) {
	s := newSvc()
	s.Groups = fakeGroups{g: nil}
	s.Subs = fakeSubs{recs: nil, nodes: nil}
	s.Panel = fakePanel{set: map[string]string{}, img: ""}
	b, err := s.Export(context.Background(), Options{Subscriptions: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	text := string(raw)
	for _, want := range []string{`"groups":[]`, `"subscriptions":[]`, `"nodes":[]`, `"panelSettings":{}`, `"backgroundImage":""`} {
		if !strings.Contains(text, want) {
			t.Errorf("缺 %s:%s", want, text)
		}
	}
}

func TestExportFailsWhenAnyPartCannotBeRead(t *testing.T) {
	boom := errors.New("磁盘坏了")
	cases := map[string]func(*Service){
		"档案":   func(s *Service) { s.Profiles = fakeProfiles{err: boom} },
		"节点组":  func(s *Service) { s.Groups = fakeGroups{err: boom} },
		"面板设置": func(s *Service) { s.Panel = fakePanel{setErr: boom} },
		"背景图":  func(s *Service) { s.Panel = fakePanel{imgErr: boom} },
		"订阅":   func(s *Service) { s.Subs = fakeSubs{err: boom} },
	}
	for what, mut := range cases {
		s := newSvc()
		mut(s)
		b, err := s.Export(context.Background(), Options{Subscriptions: true})
		if err == nil || b != nil || !strings.Contains(err.Error(), "读取"+what+"失败") || !errors.Is(err, boom) {
			t.Errorf("%s:%v %v", what, b, err)
		}
	}
	// 没勾订阅就不去读订阅:读订阅坏了也不影响
	s := newSvc()
	s.Subs = fakeSubs{err: boom}
	if _, err := s.Export(context.Background(), Options{}); err != nil {
		t.Fatalf("没勾订阅不该读订阅:%v", err)
	}
}

func TestVersionAndTimeOptional(t *testing.T) {
	s := newSvc()
	s.Version, s.Now = "", nil
	b, err := s.Export(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := toMap(t, b)["openboxVersion"]; ok {
		t.Fatal("没有版本号就不写这个键")
	}
	if _, err := time.Parse(time.RFC3339, b.ExportedAt); err != nil {
		t.Fatalf("导出时间要是 RFC3339:%q", b.ExportedAt)
	}
}
