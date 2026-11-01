package backup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// script 记录每一次调用的顺序,并按名字注入错误。
type script struct {
	mu   sync.Mutex
	log  []string
	errs map[string]error
	// 记录下来的入参
	groupTags map[string]bool
	subIn     subs.RestoreInput
	// 阻塞用:profile.replace 开始时通知并等待放行
	started chan struct{}
	release chan struct{}
}

func (s *script) hit(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, name)
	return s.errs[name]
}
func (s *script) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.log...)
}
func (s *script) writes() []string {
	out := []string{}
	for _, c := range s.calls() {
		if strings.HasSuffix(c, ".replace") || strings.HasSuffix(c, ".restore") || strings.HasSuffix(c, ".settings") || strings.HasSuffix(c, ".image") {
			out = append(out, c)
		}
	}
	return out
}

type sProfiles struct {
	s   *script
	cur map[string]any
}

func (p sProfiles) Get(context.Context) (map[string]any, error) { return p.cur, nil }
func (p sProfiles) CheckReplace(context.Context, map[string]any) error {
	return p.s.hit("profile.check")
}
func (p sProfiles) Replace(context.Context, map[string]any) (map[string]any, error) {
	if p.s.started != nil {
		p.s.started <- struct{}{}
		<-p.s.release
	}
	return nil, p.s.hit("profile.replace")
}

type sGroups struct {
	s   *script
	res *nodegroups.SaveResult
}

func (g sGroups) Groups(context.Context) ([]any, error) { return nil, nil }
func (g sGroups) CheckRestore(_ context.Context, in []any, tags map[string]bool) ([]any, error) {
	g.s.groupTags = tags
	return in, g.s.hit("groups.check")
}
func (g sGroups) Restore(context.Context, []any) (*nodegroups.SaveResult, error) {
	if err := g.s.hit("groups.restore"); err != nil {
		return nil, err
	}
	if g.res != nil {
		return g.res, nil
	}
	return &nodegroups.SaveResult{OK: true, Groups: []any{1, 2, 3}}, nil
}

type sSubs struct {
	s       *script
	enabled map[string]bool
	renamed int
	pool    []sub.Node
}

func (u sSubs) Dump(context.Context) ([]subs.Record, map[string][]sub.Node, error) {
	return nil, nil, nil
}
func (u sSubs) PoolNodes(context.Context) ([]sub.Node, error) { return u.pool, nil }
func (u sSubs) CheckRestore(_ context.Context, in subs.RestoreInput) (*subs.RestoreResult, error) {
	u.s.subIn = in
	if err := u.s.hit("subs.check"); err != nil {
		return nil, err
	}
	return &subs.RestoreResult{EnabledTags: u.enabled}, nil
}
func (u sSubs) Restore(_ context.Context, in subs.RestoreInput) (*subs.RestoreResult, error) {
	if err := u.s.hit("subs.restore"); err != nil {
		return nil, err
	}
	return &subs.RestoreResult{Subscriptions: len(in.Records), Nodes: len(in.Nodes), Renamed: u.renamed}, nil
}

type sPanel struct{ s *script }

func (p sPanel) Settings(context.Context) (map[string]string, error) { return nil, nil }
func (p sPanel) BackgroundImage(context.Context) (string, error)     { return "", nil }
func (p sPanel) CleanSettings(in map[string]any) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in {
		out[k], _ = v.(string)
	}
	return out, p.s.hit("panel.clean")
}
func (p sPanel) CheckImage(string) error { return p.s.hit("panel.checkimage") }
func (p sPanel) ReplaceSettings(context.Context, map[string]string) error {
	return p.s.hit("panel.settings")
}
func (p sPanel) SetBackgroundImage(context.Context, string) error { return p.s.hit("panel.image") }

func newImporter(s *script) *Service {
	return &Service{
		Profiles: sProfiles{s: s, cur: map[string]any{"chainProxies": []any{map[string]any{"name": "现有链", "enabled": true}}}},
		Groups:   sGroups{s: s},
		Subs:     sSubs{s: s, enabled: map[string]bool{"n1": true}, pool: []sub.Node{{Tag: "pool1"}}},
		Panel:    sPanel{s: s},
	}
}

func newScript() *script { return &script{errs: map[string]error{}} }

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

func fullFile() *ImportFile {
	return &ImportFile{
		Format: Format, Version: f64(1),
		Profile:         map[string]any{"region": "cn", "chainProxies": []any{map[string]any{"name": "文件链"}, map[string]any{"name": "关着的链", "enabled": false}}},
		Groups:          []any{map[string]any{"id": "g"}},
		Subscriptions:   &[]subs.Record{{ID: "a"}},
		Nodes:           &[]Node{{Subscription: "a", Node: sub.Node{Tag: "n1"}}},
		PanelSettings:   map[string]any{"config/theme": "dark"},
		BackgroundImage: str("data:image/png;base64,AAAA"),
	}
}

func TestImportChecksEverythingBeforeWritingAnythingThenWritesInOrder(t *testing.T) {
	sc := newScript()
	res, err := newImporter(sc).Import(context.Background(), fullFile(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"profile.check", "subs.check", "groups.check", "panel.clean", "panel.checkimage",
		"profile.replace", "subs.restore", "groups.restore", "panel.settings", "panel.image"}
	if !reflect.DeepEqual(sc.calls(), want) {
		t.Fatalf("调用顺序不对\n想要 %v\n得到 %v", want, sc.calls())
	}
	im := res.Imported
	if !res.OK || !im.Profile || im.Groups != 3 || im.Subscriptions != 1 || im.Nodes != 1 ||
		im.SubscriptionsMode == nil || *im.SubscriptionsMode != "replace" ||
		im.PanelSettings == nil || *im.PanelSettings != 1 || im.BackgroundImage == nil || !*im.BackgroundImage {
		t.Fatalf("结果不对: %+v", im)
	}
	if sc.subIn.Mode != subs.RestoreReplace || len(sc.subIn.Nodes) != 1 || sc.subIn.Nodes[0].Subscription != "a" {
		t.Fatalf("交给订阅服务的入参不对: %+v", sc.subIn)
	}
	// 节点组重名校验用的名字 = 启用订阅的节点 + 文件里启用的链式代理(关着的不算,现有档案的不算)
	if !reflect.DeepEqual(sc.groupTags, map[string]bool{"n1": true, "文件链": true}) {
		t.Fatalf("节点组校验的名字集合不对: %v", sc.groupTags)
	}
}

func TestImportAnyCheckFailureWritesNothing(t *testing.T) {
	for _, failing := range []string{"profile.check", "subs.check", "groups.check", "panel.clean", "panel.checkimage"} {
		sc := newScript()
		sc.errs[failing] = &subs.InputError{Msg: "坏了"}
		if failing == "profile.check" {
			sc.errs[failing] = &profile.ValidationError{Msg: "坏了"}
		}
		if failing == "groups.check" {
			sc.errs[failing] = &nodegroups.InputError{Msg: "坏了"}
		}
		if failing == "panel.clean" || failing == "panel.checkimage" {
			sc.errs[failing] = errors.New("坏了")
		}
		_, err := newImporter(sc).Import(context.Background(), fullFile(), subs.RestoreAppend)
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%s: 应是 InputError(400),得到 %T %v", failing, err, err)
			continue
		}
		if w := sc.writes(); len(w) != 0 {
			t.Errorf("%s: 校验没过却写了东西: %v", failing, w)
		}
	}
	// 服务自己的错误(读不出现有数据之类)不当成 400,也不写
	sc := newScript()
	sc.errs["groups.check"] = nodegroups.ErrCoreUnavailable
	_, err := newImporter(sc).Import(context.Background(), fullFile(), "")
	var ie *InputError
	if errors.As(err, &ie) || !errors.Is(err, nodegroups.ErrCoreUnavailable) || len(sc.writes()) != 0 {
		t.Fatalf("核心不可用应原样传出且不写: %v %v", err, sc.writes())
	}
}

func TestImportRejectsBadFiles(t *testing.T) {
	mut := func(f func(x *ImportFile)) *ImportFile { x := fullFile(); f(x); return x }
	cases := map[string]*ImportFile{
		"nil":       nil,
		"格式不对":      mut(func(x *ImportFile) { x.Format = "other" }),
		"没有版本":      mut(func(x *ImportFile) { x.Version = nil }),
		"版本 0":      mut(func(x *ImportFile) { x.Version = f64(0) }),
		"版本不是整数":    mut(func(x *ImportFile) { x.Version = f64(1.5) }),
		"版本比后端新":    mut(func(x *ImportFile) { x.Version = f64(2) }),
		"没有档案":      mut(func(x *ImportFile) { x.Profile = nil }),
		"没有节点组":     mut(func(x *ImportFile) { x.Groups = nil }),
		"有订阅没有节点列表": mut(func(x *ImportFile) { x.Nodes = nil }),
		"有节点没有订阅":   mut(func(x *ImportFile) { x.Subscriptions = nil }),
	}
	for name, f := range cases {
		sc := newScript()
		_, err := newImporter(sc).Import(context.Background(), f, "")
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%s: 应被拒绝为 InputError,得到 %v", name, err)
		}
		if len(sc.calls()) != 0 {
			t.Errorf("%s: 文件本身有问题时连服务都不该碰: %v", name, sc.calls())
		}
	}
	sc := newScript()
	if _, err := newImporter(sc).Import(context.Background(), fullFile(), "merge"); err == nil || len(sc.calls()) != 0 {
		t.Fatalf("未知的订阅导入方式应被拒绝: %v", err)
	}
	// 版本比后端新的提示里要写清楚版本号
	_, err := newImporter(newScript()).Import(context.Background(), cases["版本比后端新"], "")
	if err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("提示应让用户升级后端: %v", err)
	}
}

func TestImportWriteFailureReportsHowFarItGot(t *testing.T) {
	sc := newScript()
	sc.errs["groups.restore"] = errors.New("磁盘满了")
	_, err := newImporter(sc).Import(context.Background(), fullFile(), "")
	var pe *PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("应是 PartialError: %v", err)
	}
	if pe.Step != "节点组" || !reflect.DeepEqual(pe.Done, []string{"档案", "订阅和节点"}) {
		t.Fatalf("步骤记录不对: %+v", pe)
	}
	for _, want := range []string{"节点组", "档案、订阅和节点", "磁盘满了", "不会自动撤销"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息里应有 %q: %s", want, err)
		}
	}
	// 第一步就失败:明说还没写任何东西
	sc = newScript()
	sc.errs["profile.replace"] = errors.New("坏盘")
	_, err = newImporter(sc).Import(context.Background(), fullFile(), "")
	if err == nil || !strings.Contains(err.Error(), "还没有写入任何内容") {
		t.Fatalf("%v", err)
	}
	if len(sc.writes()) != 1 {
		t.Fatalf("第一步失败后不该继续写: %v", sc.writes())
	}
}

func TestImportOmittedPartsKeepCurrentState(t *testing.T) {
	sc := newScript()
	f := fullFile()
	f.Subscriptions, f.Nodes, f.PanelSettings, f.BackgroundImage = nil, nil, nil, str("")
	f.Profile = map[string]any{"region": "cn"} // 没有 chainProxies:链式代理名字取现有档案的
	res, err := newImporter(sc).Import(context.Background(), f, subs.RestoreAppend)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"profile.check", "groups.check", "profile.replace", "groups.restore"}
	if !reflect.DeepEqual(sc.calls(), want) {
		t.Fatalf("文件里没有的部分不该碰:\n想要 %v\n得到 %v", want, sc.calls())
	}
	if res.Imported.SubscriptionsMode != nil || res.Imported.PanelSettings != nil || res.Imported.BackgroundImage != nil {
		t.Fatalf("没带的部分不该出现在结果里: %+v", res.Imported)
	}
	// 没带订阅时,现有节点池和现有档案里的链式代理都要算进节点组的重名校验
	if !reflect.DeepEqual(sc.groupTags, map[string]bool{"pool1": true, "现有链": true}) {
		t.Fatalf("%v", sc.groupTags)
	}
	// 空的面板设置也是「有」:替换成空
	sc = newScript()
	f = fullFile()
	f.PanelSettings = map[string]any{}
	res, err = newImporter(sc).Import(context.Background(), f, "")
	if err != nil || res.Imported.PanelSettings == nil || *res.Imported.PanelSettings != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestImportWarnings(t *testing.T) {
	sc := newScript()
	imp := newImporter(sc)
	imp.Subs = sSubs{s: sc, enabled: map[string]bool{}, renamed: 3}
	imp.Groups = sGroups{s: sc, res: &nodegroups.SaveResult{OK: true, Groups: []any{1},
		Dangling: []nodegroups.Dangling{{Name: "自动", Members: []string{"a", "b", "c", "d", "e", "f", "g"}}},
		Dropped:  []nodegroups.Dropped{{Name: "环组", Reason: "cycle"}}}}
	res, err := imp.Import(context.Background(), fullFile(), subs.RestoreAppend)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported.RenamedNodes != 3 || len(res.Warnings) != 3 {
		t.Fatalf("%+v %v", res.Imported, res.Warnings)
	}
	all := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"3 个节点与现有节点重名", "「自动」", "a、b、c、d、e 等 7 个", "「环组」不会生效", "成了环"} {
		if !strings.Contains(all, want) {
			t.Errorf("警告里应有 %q:\n%s", want, all)
		}
	}
}

func TestImportRefusesSecondConcurrentImport(t *testing.T) {
	sc := newScript()
	sc.started, sc.release = make(chan struct{}), make(chan struct{})
	imp := newImporter(sc)
	done := make(chan error, 1)
	go func() { _, err := imp.Import(context.Background(), fullFile(), ""); done <- err }()
	<-sc.started // 第一次已经在写档案了
	if _, err := imp.Import(context.Background(), fullFile(), ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("第二次应得到 ErrBusy: %v", err)
	}
	close(sc.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func asInputError(err error, target **InputError) bool { return errors.As(err, target) }
