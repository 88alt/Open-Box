// Package businesspref 挂 GET /api/openbox/business-preference/status:概览页「业务优选」卡要的分组统计。
//
// 上游这段数据由魔改内核在选路时按目标累计(对齐上游 v0.1.476 的 businessOverview*),
// 内核闭源、没有公开材料说明从哪里读;我们的内核用的是上游现成二进制,但后端目前没有读这些统计的入口。
// 所以这里如实给出两件事——**哪些分组开着业务优选**,以及**统计读不到**(reliability / performance 为 null、
// state 为 unavailable)——面板据此走「暂无法统计改选次数」那一支,不编造次数。
// 等内核侧能把统计读出来(见 STATUS「业务优选进选路」),再把 counts 与 last 填上即可,接口外形不用变。
package businesspref

import (
	"context"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// windowMinutes 是统计窗口:上游口径为最近 1 小时(businessOverviewHelpWindow 也是这么写的)。
const windowMinutes = 60

// businessTypes 是能开业务优选的分组类型(上游:自动优选 / 故障转移 / 负载均衡)。
var businessTypes = map[string]bool{"urltest": true, "failover": true, "loadbalance": true}

// Deps 是本分组的依赖。
type Deps struct {
	// Groups 读已保存的节点组(*nodegroups.Service 满足)。
	Groups interface {
		Groups(ctx context.Context) ([]any, error)
	}
}

// Register 挂路由。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/business-preference/status", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) { status(w, r, d) },
	}))
}

// group 是一个开着业务优选的分组。
type group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// state:running / waiting / no_backup / manual_lane / pending / unavailable,
	// 面板按 businessOverview_<state> 取文案。目前只可能是 unavailable(读不到内核统计)。
	State string `json:"state"`
	// 当前页签名;分组没有页签概念时不给
	Lane string `json:"lane,omitempty"`
	// 近 windowMinutes 分钟内按原因分开的改选条数;读不到统计时为 null
	Reliability *int `json:"reliability"`
	Performance *int `json:"performance"`
	// 最近一次改选;读不到统计时不给这个字段
	Last any `json:"last,omitempty"`
}

type response struct {
	WindowMinutes int     `json:"windowMinutes"`
	Groups        []group `json:"groups"`
}

func status(w http.ResponseWriter, r *http.Request, d Deps) {
	httpx.NoStore(w)
	groups := []group{}
	if d.Groups != nil {
		stored, err := d.Groups.Groups(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "读取分组失败: "+err.Error())
			return
		}
		for _, item := range stored {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if !businessTypes[str(m, "type")] || m["businessPreference"] != true {
				continue
			}
			groups = append(groups, group{
				ID:    str(m, "id"),
				Name:  str(m, "name"),
				State: "unavailable",
			})
		}
	}
	httpx.JSON(w, http.StatusOK, response{WindowMinutes: windowMinutes, Groups: groups})
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
