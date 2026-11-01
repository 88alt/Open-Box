// Package backup 是「导出 / 导入」:导出见本文件,导入见 import.go。
// 文件格式自己定,依据前端 api/openbox.ts 的 OpenboxBackup 类型和 BackupCard.vue 的用法,没有参照上游:
//
//	{ "format": "open-box-backup", "version": 1, "exportedAt": "<RFC3339>", "openboxVersion": "<后端版本>",
//	  "includes": { "subscriptions", "chainProxies", "clientRoutes", "servers" },   // 导出时勾了哪些可选部分
//	  "profile": { ... },          // 档案;没勾的可选部分(chainProxies / clientRoutes / servers)整个键不在里面
//	  "groups": [ ... ],           // 节点组,原样(含内置的直连 / 拒绝)
//	  "subscriptions": [ ... ],    // 勾了订阅才有:订阅元数据,原样
//	  "nodes": [ { "subscription": "<订阅 id>", "tag": ..., ... } ],   // 勾了订阅才有:各订阅的节点,摊平成一个数组
//	  "panelSettings": { "config/...": "..." },   // 面板设置,不含名字带 password 的键
//	  "backgroundImage": "<data URL,没有是空串>" }
//
// 备份里有订阅链接、节点密码这类凭据,和面板里导出的任何别的东西一样,要当敏感文件保管。
package backup

import (
	"context"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// 文件格式标记和版本。
const (
	Format  = "open-box-backup"
	Version = 1
)

// Options 是导出时可勾的四块。
type Options struct {
	Subscriptions bool // 订阅和它们的节点
	ChainProxies  bool // 档案里的链式代理
	ClientRoutes  bool // 档案里的终端分流
	Servers       bool // 档案里的共享网络
}

// Includes 是文件里记的「勾了哪些」。
type Includes struct {
	Subscriptions bool `json:"subscriptions"`
	ChainProxies  bool `json:"chainProxies"`
	ClientRoutes  bool `json:"clientRoutes"`
	Servers       bool `json:"servers"`
}

// Node 是备份里的一个订阅节点:订阅 id 加上节点本身(字段摊平在同一层)。
type Node struct {
	Subscription string `json:"subscription"`
	sub.Node
}

// Backup 是整个备份文件。
type Backup struct {
	Format         string            `json:"format"`
	Version        int               `json:"version"`
	ExportedAt     string            `json:"exportedAt"`
	OpenboxVersion string            `json:"openboxVersion,omitempty"`
	Includes       Includes          `json:"includes"`
	Profile        map[string]any    `json:"profile"`
	Groups         []any             `json:"groups"`
	Subscriptions  any               `json:"subscriptions,omitempty"` // 没勾是 nil(整个键不出现);勾了但没有订阅是 []
	Nodes          any               `json:"nodes,omitempty"`
	PanelSettings  map[string]string `json:"panelSettings"`
	BackgroundImg  string            `json:"backgroundImage"`
}

// 依赖(测试里换假的)。
type (
	// Profiles 读写档案(导出读,导入整体替换)。
	Profiles interface {
		Get(ctx context.Context) (map[string]any, error)
		CheckReplace(ctx context.Context, in map[string]any) error
		Replace(ctx context.Context, in map[string]any) (map[string]any, error)
	}
	// Groups 读写节点组。
	Groups interface {
		Groups(ctx context.Context) ([]any, error)
		CheckRestore(ctx context.Context, in []any, tags map[string]bool) ([]any, error)
		Restore(ctx context.Context, in []any) (*nodegroups.SaveResult, error)
	}
	// Subs 读写订阅和节点。
	Subs interface {
		Dump(ctx context.Context) ([]subs.Record, map[string][]sub.Node, error)
		PoolNodes(ctx context.Context) ([]sub.Node, error)
		CheckRestore(ctx context.Context, in subs.RestoreInput) (*subs.RestoreResult, error)
		Restore(ctx context.Context, in subs.RestoreInput) (*subs.RestoreResult, error)
	}
	// Panel 读写面板设置和背景图。
	Panel interface {
		Settings(ctx context.Context) (map[string]string, error)
		BackgroundImage(ctx context.Context) (string, error)
		CleanSettings(in map[string]any) (map[string]string, error)
		CheckImage(img string) error
		ReplaceSettings(ctx context.Context, clean map[string]string) error
		SetBackgroundImage(ctx context.Context, img string) error
	}
)

// Service 做导出和导入。
type Service struct {
	Profiles Profiles
	Groups   Groups
	Subs     Subs
	Panel    Panel
	// Version 写进文件的 openboxVersion,空就不写。
	Version string
	Now     func() time.Time

	importing sync.Mutex // 同一时刻只做一次导入
}

// Export 按 opts 导出一份备份。任何一块读不出来整个导出失败(不给一份缺东西的「备份」)。
func (s *Service) Export(ctx context.Context, opts Options) (*Backup, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	prof, err := s.Profiles.Get(ctx)
	if err != nil {
		return nil, wrap("档案", err)
	}
	groups, err := s.Groups.Groups(ctx)
	if err != nil {
		return nil, wrap("节点组", err)
	}
	settings, err := s.Panel.Settings(ctx)
	if err != nil {
		return nil, wrap("面板设置", err)
	}
	img, err := s.Panel.BackgroundImage(ctx)
	if err != nil {
		return nil, wrap("背景图", err)
	}

	out := &Backup{
		Format: Format, Version: Version, ExportedAt: now().UTC().Format(time.RFC3339), OpenboxVersion: s.Version,
		Includes: Includes{Subscriptions: opts.Subscriptions, ChainProxies: opts.ChainProxies, ClientRoutes: opts.ClientRoutes, Servers: opts.Servers},
		Profile:  pick(prof, opts), Groups: nonNil(groups), PanelSettings: settings, BackgroundImg: img,
	}
	if opts.Subscriptions {
		recs, byID, err := s.Subs.Dump(ctx)
		if err != nil {
			return nil, wrap("订阅", err)
		}
		nodes := []Node{}
		for _, r := range recs {
			for _, n := range byID[r.ID] {
				nodes = append(nodes, Node{Subscription: r.ID, Node: n})
			}
		}
		if recs == nil {
			recs = []subs.Record{}
		}
		out.Subscriptions, out.Nodes = recs, nodes
	}
	return out, nil
}

// pick 复制档案,没勾的可选部分整个键去掉(导入时「文件里没有」就表示「保持现状」)。
func pick(p map[string]any, o Options) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		switch {
		case k == "chainProxies" && !o.ChainProxies,
			k == "clientRoutes" && !o.ClientRoutes,
			k == "servers" && !o.Servers:
			continue
		}
		out[k] = v
	}
	return out
}

func nonNil(g []any) []any {
	if g == nil {
		return []any{}
	}
	return g
}

type exportError struct {
	what string
	err  error
}

func (e *exportError) Error() string { return "读取" + e.what + "失败: " + e.err.Error() }
func (e *exportError) Unwrap() error { return e.err }

func wrap(what string, err error) error { return &exportError{what: what, err: err} }
