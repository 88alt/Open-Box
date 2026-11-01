package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// 导入(POST /api/openbox/backup/import)。规则:
//
//  1. 先把整份文件全部校验一遍(格式和版本、档案、订阅和节点、节点组、面板设置、背景图),任何一项不过就
//     一个字都不写,返回 400 和原因;
//  2. 都通过了才按 档案 → 订阅和节点 → 节点组 → 面板设置 → 背景图 的顺序写(节点组引用节点,
//     节点组的重名校验又要看订阅和链式代理,所以顺序不能变);
//  3. 写的中途失败(磁盘问题、并发改动)不自动撤销,错误里写清楚已经写到哪一步;
//  4. 只改存着的数据,不部署、不重启内核(前端导入之后自己提示)。
//
// 文件里没有的部分:订阅和节点(没勾订阅)、档案的 chainProxies / clientRoutes / servers(没勾)保持现状;
// 面板设置(没有 panelSettings 键)和背景图(没有键或空串)也保持现状;档案的其它键整体替换。

// ImportFile 是要导入的备份文件。指针 / 切片 / map 为 nil 表示文件里没有这个键(或是 null)。
type ImportFile struct {
	Format          string         `json:"format"`
	Version         *float64       `json:"version"`
	Profile         map[string]any `json:"profile"`
	Groups          []any          `json:"groups"`
	Subscriptions   *[]subs.Record `json:"subscriptions"`
	Nodes           *[]Node        `json:"nodes"`
	PanelSettings   map[string]any `json:"panelSettings"`
	BackgroundImage *string        `json:"backgroundImage"`
}

// Imported 对应前端 OpenboxBackupImportResult.imported;renamedNodes 是多出来的(前端不读)。
type Imported struct {
	Profile           bool    `json:"profile"`
	Groups            int     `json:"groups"`
	Subscriptions     int     `json:"subscriptions"`
	Nodes             int     `json:"nodes"`
	SubscriptionsMode *string `json:"subscriptionsMode"`
	PanelSettings     *int    `json:"panelSettings,omitempty"`
	BackgroundImage   *bool   `json:"backgroundImage,omitempty"`
	RenamedNodes      int     `json:"renamedNodes,omitempty"`
}

// ImportResult 是导入的结果。warnings 是多出来的:导入成功但有需要用户留意的地方(悬空的节点组成员等)。
type ImportResult struct {
	OK       bool     `json:"ok"`
	Imported Imported `json:"imported"`
	Warnings []string `json:"warnings,omitempty"`
}

// InputError 是文件有问题(HTTP 400),什么都没写。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &InputError{Msg: fmt.Sprintf(format, a...)} }

// ErrBusy:已经有一次导入在进行(HTTP 409)。
var ErrBusy = errors.New("已有一次导入在进行,请等它结束再试")

// PartialError:写入中途失败(HTTP 500)。已经写的不会自动撤销。
type PartialError struct {
	Step string   // 失败的那一步
	Done []string // 在它之前已经写完的步骤
	Err  error
}

func (e *PartialError) Error() string {
	if len(e.Done) == 0 {
		return fmt.Sprintf("导入失败(「%s」这一步出错,还没有写入任何内容):%v", e.Step, e.Err)
	}
	return fmt.Sprintf("导入没有完成:「%s」这一步出错:%v。在此之前已经写入:%s;这些不会自动撤销,可以修好问题后再导入一次", e.Step, e.Err, strings.Join(e.Done, "、"))
}
func (e *PartialError) Unwrap() error { return e.Err }

// invalid 把各服务的「输入有问题」翻成 InputError(带上是哪一部分),其它错误原样返回。
func invalid(part string, err error) error {
	var pv *profile.ValidationError
	var si *subs.InputError
	var gi *nodegroups.InputError
	switch {
	case errors.As(err, &pv):
		return bad("档案:%s", pv.Msg)
	case errors.As(err, &si):
		return bad("订阅:%s", si.Msg)
	case errors.As(err, &gi):
		return bad("节点组:%s", gi.Msg)
	}
	return fmt.Errorf("校验%s失败: %w", part, err)
}

// checkFile 只看文件本身的形状:格式、版本、必须有的部分。
func checkFile(f *ImportFile) error {
	if f == nil || f.Format != Format {
		return bad("这不是 Open-Box 的备份文件")
	}
	if f.Version == nil || *f.Version != float64(int(*f.Version)) || *f.Version < 1 {
		return bad("备份文件缺少有效的版本号")
	}
	if *f.Version > Version {
		return bad("备份文件的版本是 %d,比这个后端能读的版本(%d)新,请先升级后端", int(*f.Version), Version)
	}
	if f.Profile == nil {
		return bad("备份文件里没有档案")
	}
	if f.Groups == nil {
		return bad("备份文件里没有节点组")
	}
	if f.Subscriptions == nil && f.Nodes != nil && len(*f.Nodes) > 0 {
		return bad("备份文件里有节点却没有订阅,不知道它们属于谁")
	}
	if f.Subscriptions != nil && f.Nodes == nil {
		return bad("备份文件里有订阅却没有节点列表(没有节点要写成空数组)")
	}
	return nil
}

// chainNames 取档案里启用且有名字的链式代理的名字(它们和节点一样占用出站名)。
func chainNames(p map[string]any) []string {
	list, _ := p["chainProxies"].([]any)
	var out []string
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if en, has := m["enabled"].(bool); has && !en {
			continue
		}
		if name, _ := m["name"].(string); strings.TrimSpace(name) != "" {
			out = append(out, name)
		}
	}
	return out
}

// Import 导入备份。mode 只对订阅有意义:replace(默认)或 append。
func (s *Service) Import(ctx context.Context, f *ImportFile, mode subs.RestoreMode) (*ImportResult, error) {
	if !s.importing.TryLock() {
		return nil, ErrBusy
	}
	defer s.importing.Unlock()

	if err := checkFile(f); err != nil {
		return nil, err
	}
	if mode == "" {
		mode = subs.RestoreReplace
	}
	if mode != subs.RestoreReplace && mode != subs.RestoreAppend {
		return nil, bad("订阅导入方式只能是 replace 或 append")
	}

	// ---- 一、整份校验(不写任何东西)----
	if err := s.Profiles.CheckReplace(ctx, f.Profile); err != nil {
		return nil, invalid("档案", err)
	}
	var subIn *subs.RestoreInput
	tags := map[string]bool{}
	if f.Subscriptions != nil {
		in := subs.RestoreInput{Records: *f.Subscriptions, Mode: mode}
		for _, n := range *f.Nodes {
			in.Nodes = append(in.Nodes, subs.RestoreNode{Subscription: n.Subscription, Node: n.Node})
		}
		subIn = &in
		chk, err := s.Subs.CheckRestore(ctx, in)
		if err != nil {
			return nil, invalid("订阅", err)
		}
		for t := range chk.EnabledTags {
			tags[t] = true
		}
	} else {
		pool, err := s.Subs.PoolNodes(ctx)
		if err != nil {
			return nil, fmt.Errorf("读取现有节点失败: %w", err)
		}
		for _, n := range pool {
			tags[n.Tag] = true
		}
	}
	chainSrc := f.Profile
	if _, has := f.Profile["chainProxies"]; !has || f.Profile["chainProxies"] == nil {
		cur, err := s.Profiles.Get(ctx)
		if err != nil {
			return nil, fmt.Errorf("读取现有档案失败: %w", err)
		}
		chainSrc = cur
	}
	for _, name := range chainNames(chainSrc) {
		tags[name] = true
	}
	if _, err := s.Groups.CheckRestore(ctx, f.Groups, tags); err != nil {
		return nil, invalid("节点组", err)
	}
	var settings map[string]string
	if f.PanelSettings != nil {
		var err error
		if settings, err = s.Panel.CleanSettings(f.PanelSettings); err != nil {
			return nil, bad("面板设置:%v", err)
		}
	}
	image := ""
	if f.BackgroundImage != nil && *f.BackgroundImage != "" {
		image = *f.BackgroundImage
		if err := s.Panel.CheckImage(image); err != nil {
			return nil, bad("背景图:%v", err)
		}
	}

	// ---- 二、按顺序写 ----
	res := &ImportResult{OK: true}
	var done []string
	fail := func(step string, err error) error { return &PartialError{Step: step, Done: done, Err: err} }

	if _, err := s.Profiles.Replace(ctx, f.Profile); err != nil {
		return nil, fail("档案", err)
	}
	done = append(done, "档案")
	res.Imported.Profile = true

	if subIn != nil {
		r, err := s.Subs.Restore(ctx, *subIn)
		if err != nil {
			return nil, fail("订阅和节点", err)
		}
		done = append(done, "订阅和节点")
		m := string(mode)
		res.Imported.SubscriptionsMode = &m
		res.Imported.Subscriptions, res.Imported.Nodes, res.Imported.RenamedNodes = r.Subscriptions, r.Nodes, r.Renamed
		if r.Renamed > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("追加订阅时有 %d 个节点与现有节点重名,已在名字后面加了序号;节点组里引用旧名字的成员需要自己重新选", r.Renamed))
		}
	}

	g, err := s.Groups.Restore(ctx, f.Groups)
	if err != nil {
		return nil, fail("节点组", err)
	}
	done = append(done, "节点组")
	res.Imported.Groups = len(g.Groups)
	res.Warnings = append(res.Warnings, groupWarnings(g)...)

	if settings != nil {
		if err := s.Panel.ReplaceSettings(ctx, settings); err != nil {
			return nil, fail("面板设置", err)
		}
		done = append(done, "面板设置")
		n := len(settings)
		res.Imported.PanelSettings = &n
	}
	if image != "" {
		if err := s.Panel.SetBackgroundImage(ctx, image); err != nil {
			return nil, fail("背景图", err)
		}
		t := true
		res.Imported.BackgroundImage = &t
	}
	return res, nil
}

// groupWarnings 把节点组保存结果里「导入成功但需要留意」的部分写成人话。
func groupWarnings(g *nodegroups.SaveResult) []string {
	var out []string
	for _, d := range g.Dangling {
		members := d.Members
		more := ""
		if len(members) > 5 {
			members, more = members[:5], fmt.Sprintf(" 等 %d 个", len(d.Members))
		}
		out = append(out, fmt.Sprintf("节点组「%s」里有成员现在找不到(节点不存在或已改名):%s%s", d.Name, strings.Join(members, "、"), more))
	}
	for _, d := range g.Dropped {
		out = append(out, fmt.Sprintf("节点组「%s」不会生效:%s", d.Name, droppedReason(d.Reason)))
	}
	return out
}

func droppedReason(r string) string {
	if r == "cycle" {
		return "组与组之间的引用成了环"
	}
	return r
}
