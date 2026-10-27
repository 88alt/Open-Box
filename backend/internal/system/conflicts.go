package system

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 与其它代理服务冲突的检测(/api/openbox/service/status 的 conflicts)。
// 自己的设计,依据前端 OpenboxConflictService({id, label, running})和各项目公开的包名 / 服务名,没有参照上游。
//
// 只读:只问服务管理器「这个服务在不在跑」,不停、不改任何东西。名单里的服务名是按这些项目公开的 OpenWrt 包 /
// systemd 单元名整理的,名字对不对要真机确认(见 STATUS);没有这个服务、查不到都当作「没在跑」,绝不因为检测失败让状态接口报错。

// ConflictCandidate 是一个可能与 sing-box 抢流量的代理服务。Names 是它可能使用的服务名(任一在跑即算在跑)。
type ConflictCandidate struct {
	ID    string
	Label string
	Names []string
}

// Conflict 对应前端的 OpenboxConflictService。
type Conflict struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Running bool   `json:"running"`
}

// KnownConflicts 是默认的检测名单(顺序即返回顺序)。
var KnownConflicts = []ConflictCandidate{
	{ID: "openclash", Label: "OpenClash", Names: []string{"openclash"}},
	{ID: "passwall", Label: "PassWall", Names: []string{"passwall"}},
	{ID: "passwall2", Label: "PassWall 2", Names: []string{"passwall2"}},
	{ID: "homeproxy", Label: "HomeProxy", Names: []string{"homeproxy"}},
	{ID: "shadowsocksr", Label: "ShadowSocksR Plus+", Names: []string{"shadowsocksr"}},
	{ID: "nikki", Label: "Nikki", Names: []string{"nikki"}},
	{ID: "mihomo", Label: "Mihomo", Names: []string{"mihomo"}},
	{ID: "clash", Label: "Clash", Names: []string{"clash", "clash-meta"}},
	{ID: "v2raya", Label: "v2rayA", Names: []string{"v2raya"}},
	{ID: "v2ray", Label: "V2Ray", Names: []string{"v2ray"}},
	{ID: "xray", Label: "Xray", Names: []string{"xray"}},
}

// ConflictDetector 找出正在运行的冲突服务。
type ConflictDetector struct {
	Svc        *Services
	Candidates []ConflictCandidate // nil = KnownConflicts
	Exclude    []string            // 不算冲突的服务名(自己的 sing-box 服务、后端自己)
	Exists     func(string) bool   // 判断路径是否存在;OpenWrt 上只查 /etc/init.d 里真有的脚本
}

// Find 返回正在运行的冲突服务(都在跑才会出现在结果里,Running 恒为 true,与前端的约定一致)。
// 没有服务管理器时返回空;查询出错返回已查到的部分和 error,调用方应当把 error 当作「不确定」而不是「没有冲突」之外的事。
func (d *ConflictDetector) Find(ctx context.Context) ([]Conflict, error) {
	out := []Conflict{}
	if d == nil || d.Svc == nil {
		return out, nil
	}
	cands := d.Candidates
	if cands == nil {
		cands = KnownConflicts
	}
	skip := map[string]bool{}
	for _, n := range d.Exclude {
		skip[n] = true
	}
	// 每个候选保留可用的服务名
	type cand struct {
		c     ConflictCandidate
		names []string
	}
	var list []cand
	for _, c := range cands {
		var names []string
		for _, n := range c.Names {
			if skip[n] || ValidServiceName(n) != nil {
				continue
			}
			names = append(names, n)
		}
		if len(names) > 0 {
			list = append(list, cand{c, names})
		}
	}

	running := map[string]bool{}
	var firstErr error
	switch d.Svc.Kind {
	case InitOpenWrt:
		for _, c := range list {
			for _, n := range c.names {
				if d.Exists == nil || !d.Exists("/etc/init.d/"+n) {
					continue // 没装这个服务
				}
				res, err := d.Svc.run(ctx, d.Svc.statusTimeout(), "/etc/init.d/"+n, "running")
				if err != nil {
					if ctx.Err() != nil {
						return out, ctx.Err()
					}
					if firstErr == nil {
						firstErr = fmt.Errorf("查询 %s 失败: %w", n, err)
					}
					continue
				}
				running[n] = res.Code == 0
			}
		}
	case InitSystemd:
		// 一次命令查全部:systemctl is-active a b c 按顺序每个单元输出一行
		var names []string
		for _, c := range list {
			names = append(names, c.names...)
		}
		if len(names) == 0 {
			return out, nil
		}
		res, err := d.Svc.run(ctx, d.Svc.statusTimeout(), "systemctl", append([]string{"is-active"}, names...)...)
		if err != nil {
			return out, fmt.Errorf("查询服务状态失败: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
		if len(lines) != len(names) {
			return out, errors.New("systemctl is-active 的输出行数与查询的服务数不一致")
		}
		for i, n := range names {
			running[n] = strings.TrimSpace(lines[i]) == "active"
		}
	default:
		return out, nil
	}
	for _, c := range list {
		for _, n := range c.names {
			if running[n] {
				out = append(out, Conflict{ID: c.c.ID, Label: c.c.Label, Running: true})
				break
			}
		}
	}
	return out, firstErr
}
