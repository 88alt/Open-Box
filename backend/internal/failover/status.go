package failover

import (
	"context"
	"encoding/json"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// 下面几个类型的字段名和取值对应前端 OpenboxFailoverStatus / OpenboxFailoverGroupStatus / OpenboxFailoverLaneStatus。

// LaneStatus 是一个页签的运行状态。
type LaneStatus struct {
	ID         string                     `json:"id"`
	Name       string                     `json:"name"`
	Index      int                        `json:"index"`
	Role       string                     `json:"role"` // primary / backup
	Mode       string                     `json:"mode"`
	Manual     bool                       `json:"manual,omitempty"`
	Ref        *string                    `json:"ref"`
	SubTag     *string                    `json:"subTag"`
	Members    []string                   `json:"members"`
	Valid      []string                   `json:"valid"`
	Health     string                     `json:"health"` // up / down / unknown
	FailStreak int                        `json:"failStreak"`
	UpSince    *int64                     `json:"upSince"`
	KernelNow  *string                    `json:"kernelNow"`
	Confirmed  *bool                      `json:"confirmed"`
	Nodes      map[string]json.RawMessage `json:"nodes"`
}

// GroupStatus 是一个故障转移组的运行状态。
type GroupStatus struct {
	ID            string       `json:"id"`
	Tag           string       `json:"tag"`
	Status        string       `json:"status"` // pending / ok / backup / failing / reject / unknown
	Paused        string       `json:"paused"`
	LastError     string       `json:"lastError"`
	RejectTag     string       `json:"rejectTag"`
	CurrentLaneID *string      `json:"currentLaneId"`
	CurrentSince  *int64       `json:"currentSince"`
	KernelNow     *string      `json:"kernelNow"`
	LastSwitch    any          `json:"lastSwitch"`
	LastRoundAt   *int64       `json:"lastRoundAt"`
	NextRoundAt   *int64       `json:"nextRoundAt"`
	LaneOrder     []string     `json:"laneOrder"`
	Reorder       any          `json:"reorder"`
	InFlight      bool         `json:"inFlight"`
	Round         any          `json:"round"`
	Settings      Settings     `json:"settings"`
	Lanes         []LaneStatus `json:"lanes"`
}

// Status 是 GET /api/openbox/failover/status 的响应。
type Status struct {
	Version *string       `json:"version"`
	Paused  string        `json:"paused"`
	Running bool          `json:"running"`
	Probes  any           `json:"probes"`
	Groups  []GroupStatus `json:"groups"`
}

// KernelFunc 读内核当前的代理列表(*singbox.KernelProxy 的名字、类型、当前选择)。读不到返回 error。
type KernelFunc func(ctx context.Context) ([]singbox.KernelProxy, error)

// BuildStatus 用计划加内核此刻的选择算出状态。kernel 为空或读不到时,各组 status 是 unknown、kernelNow 为 null,
// 其余照计划如实给。没有检测管理器,所以 running 恒为 false、页签 health 恒为 unknown、没有节点检测结果。
func BuildStatus(ctx context.Context, plan Plan, kernel KernelFunc) Status {
	now := map[string]string{} // 代理名 → 它当前选中的
	haveKernel := false
	if kernel != nil {
		if ps, err := kernel(ctx); err == nil {
			haveKernel = true
			for _, p := range ps {
				now[p.Name] = p.Now
			}
		}
	}
	out := Status{Groups: make([]GroupStatus, 0, len(plan)), Paused: ""}
	for _, g := range plan {
		gs := GroupStatus{
			ID: g.ID, Tag: g.Tag, Status: "unknown", RejectTag: g.RejectTag, Settings: g.Settings,
			LaneOrder: make([]string, 0, len(g.Lanes)), Lanes: make([]LaneStatus, 0, len(g.Lanes)),
		}
		cur := ""
		if haveKernel {
			if n, ok := now[g.Tag]; ok && n != "" {
				cur = n
				gs.KernelNow = &cur
			}
		}
		for _, l := range g.Lanes {
			gs.LaneOrder = append(gs.LaneOrder, l.ID)
			ls := LaneStatus{
				ID: l.ID, Name: l.Name, Index: l.Index, Role: "backup", Mode: l.Mode, Manual: l.Mode == "selector",
				Ref: l.Ref, SubTag: l.SubTag, Members: l.Members, Valid: l.Valid, Health: "unknown",
				Nodes: map[string]json.RawMessage{},
			}
			if l.Index == 0 {
				ls.Role = "primary"
			}
			for _, m := range l.Valid {
				ls.Nodes[m] = json.RawMessage("null")
			}
			if l.SubTag != nil && haveKernel {
				if n, ok := now[*l.SubTag]; ok && n != "" {
					v := n
					ls.KernelNow = &v
				}
			}
			if cur != "" && l.Ref != nil && *l.Ref == cur {
				id := l.ID
				gs.CurrentLaneID = &id
				if l.Index == 0 {
					gs.Status = "ok"
				} else {
					gs.Status = "backup"
				}
			}
			gs.Lanes = append(gs.Lanes, ls)
		}
		if cur != "" && cur == g.RejectTag {
			gs.Status = "reject"
		}
		out.Groups = append(out.Groups, gs)
	}
	return out
}
