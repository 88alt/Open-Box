// Package failover 是「故障转移」节点组的后端部分。
//
// 内核里没有 fallback 这种出站,core 把一个故障转移组落成:一个 selector(父组,名字就是组名)+ 每个多节点页签一个私有
// 子组(urltest,或手动选择时是 selector),同时随配置给出每个组的「运行计划」(Built.Failover:页签顺序、每个页签落成了
// 哪个出站、检测参数)。主备切换本该由后台根据检测结果去改父 selector 的当前选择。
//
// 本包目前做第一部分(自己的设计,依据前端 OpenboxFailoverStatus 等类型和注释、core 的 FailoverState、sing-box 官方
// Clash API 文档,没有参照上游):
//   - 部署成功后把运行计划存成文件(SavePlan),状态接口按「已部署的那份」计划说话;
//   - 只读状态(Status):计划 + 内核此刻的选择(GET /proxies),推出每个组现在用的是哪个页签、是主用还是备用。
//
// 检测与切换的状态机在 manager.go(第二步,纯逻辑,对内核的动作和时钟都是注入的);还没接进 main.go、状态接口
// 也还没带上它的数据(第三步),所以 GET /failover/status 里 running 现在仍恒为 false、各页签 health 仍恒为 unknown。
package failover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// Lane 是计划里的一个主备页签(core 的 LaneState)。
type Lane struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Icon    string   `json:"icon,omitempty"`
	Index   int      `json:"index"`
	Members []string `json:"members"`
	Valid   []string `json:"valid"`
	Mode    string   `json:"mode"` // single / urltest / selector / empty
	Ref     *string  `json:"ref"`
	SubTag  *string  `json:"subTag"`
}

// Settings 是组的检测参数(core 的 FailoverRuntimeSettings)。
type Settings struct {
	Interval         string  `json:"interval,omitempty"`
	IntervalMs       float64 `json:"intervalMs,omitempty"`
	Tolerance        float64 `json:"tolerance,omitempty"`
	TestURL          string  `json:"testUrl,omitempty"`
	ExpectedStatus   string  `json:"expectedStatus,omitempty"`
	TimeoutMs        int64   `json:"timeoutMs"`
	FailureThreshold int64   `json:"failureThreshold"`
	RestorePrimary   bool    `json:"restorePrimary"`
	RecoveryHoldMs   int64   `json:"recoveryHoldMs"`
}

// Group 是一个故障转移组的运行计划(core 的 FailoverState)。
type Group struct {
	ID        string   `json:"id"`
	Tag       string   `json:"tag"` // 父 selector 的标签,也就是组名
	Lanes     []Lane   `json:"lanes"`
	RejectTag string   `json:"rejectTag"`
	Settings  Settings `json:"settings"`
}

// Plan 是已部署配置里所有故障转移组的计划。
type Plan []Group

// PlanFileName 是计划文件在配置目录里的文件名。
const PlanFileName = "failover.json"

// maxPlanBytes 是读计划文件的上限(正常只有几 KB)。
const maxPlanBytes = 4 << 20

// ParsePlan 把 core 给的 failover(任意 JSON 值)整理成 Plan;nil 当作没有故障转移组。
func ParsePlan(v any) (Plan, error) {
	if v == nil {
		return Plan{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("故障转移计划序列化失败: %w", err)
	}
	return decode(b)
}

func decode(b []byte) (Plan, error) {
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, errors.New("故障转移计划不是合法的 JSON 数组")
	}
	if p == nil {
		p = Plan{}
	}
	for i := range p {
		if p[i].ID == "" || p[i].Tag == "" {
			return nil, errors.New("故障转移计划里有组缺少 id 或 tag")
		}
		if p[i].Lanes == nil {
			p[i].Lanes = []Lane{}
		}
		for j := range p[i].Lanes {
			if p[i].Lanes[j].Members == nil {
				p[i].Lanes[j].Members = []string{}
			}
			if p[i].Lanes[j].Valid == nil {
				p[i].Lanes[j].Valid = []string{}
			}
		}
	}
	return p, nil
}

// SavePlan 原子写计划文件(0600)。没有故障转移组时写 []:把上一份旧计划盖掉,别让状态接口还认着已经删掉的组。
func SavePlan(path string, p Plan) error {
	if p == nil {
		p = Plan{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(path, b, 0o600)
}

// LoadPlan 读计划文件。文件不存在 = 还没部署过 / 没有计划,返回空计划、没有错误;文件坏了返回错误。
func LoadPlan(path string) (Plan, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Plan{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, maxPlanBytes+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > maxPlanBytes {
		return nil, errors.New("故障转移计划文件过大")
	}
	if len(bytes.TrimSpace(buf)) == 0 {
		return Plan{}, nil
	}
	return decode(buf)
}
