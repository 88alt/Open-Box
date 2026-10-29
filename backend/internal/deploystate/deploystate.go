// Package deploystate 包住一次部署(singbox.Deployer.Deploy),把「最近一次部署的结果」存下来,
// 给 GET /api/openbox/deploy/state 用:刷新页面、换浏览器后面板仍能看到上一次部署成没成功、为什么没成功。
// 自己的设计,依据前端 api/openbox.ts 的 OpenboxDeployResult / OpenboxDeployState 的类型和注释
// (「GET /deploy/state 跨刷新保留,'idle' 表示从没部署过」),没有参照上游。
//
// 不管是谁触发的部署(面板上的「部署」按钮、DNS 过滤的应用),都从 Run 走,所以状态是同一份。
package deploystate

import (
	"context"
	"log/slog"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	fileName = "deploy-state"
	// StageIdle 是从没部署过。
	StageIdle = "idle"
	maxMsgLen = 2000
)

// State 对应前端的 OpenboxDeployState。At 是毫秒时间戳,idle 时为 0。
type State struct {
	Stage   string   `json:"stage"`
	Message string   `json:"message"`
	At      int64    `json:"at"`
	BadTags []string `json:"badTags"`
}

// Service 跑部署并记结果。
type Service struct {
	Store *store.Store
	// Deploy 做一次完整部署(singbox.Deployer.Deploy 满足)。
	Deploy func(ctx context.Context) singbox.DeployResult
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func idle() State { return State{Stage: StageIdle, BadTags: []string{}} }

// State 读最近一次部署的结果;没部署过(或文件不存在)是 idle。文件坏了报错,不覆盖。
func (s *Service) State() (State, error) {
	var st State
	ok, err := s.Store.Load(fileName, &st)
	if err != nil {
		return State{}, err
	}
	if !ok || st.Stage == "" {
		return idle(), nil
	}
	if st.BadTags == nil {
		st.BadTags = []string{}
	}
	return st, nil
}

// Run 做一次部署并记下结果。「已有部署在进行」那种什么都没做的结果不记(正在进行的那次结束时会记)。
// 记录失败只写日志,不影响返回的部署结果:系统已经变了,调用方必须拿到真实结果。
func (s *Service) Run(ctx context.Context) singbox.DeployResult {
	res := s.Deploy(ctx)
	if res.Busy {
		return res
	}
	msg := res.Message
	if r := []rune(msg); len(r) > maxMsgLen {
		msg = string(r[:maxMsgLen]) + "…"
	}
	bad := res.BadTags
	if bad == nil {
		bad = []string{}
	}
	st := State{Stage: res.Stage, Message: msg, At: s.now().UnixMilli(), BadTags: bad}
	if res.OK {
		st.Stage = singbox.StageRunning
	}
	if err := s.Store.Save(fileName, st); err != nil {
		slog.Warn("记录部署结果失败", "error", err)
	}
	return res
}
