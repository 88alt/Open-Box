package profile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const storeName = "profile"

// ErrCoreUnavailable 表示需要 openbox-core 的操作没有可用的核心。
var ErrCoreUnavailable = errors.New("openbox-core 不可用,无法完成该操作")

// ValidationError 是用户输入不合法(HTTP 400)。
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Core 是档案服务用到的核心能力;测试里用假实现。
type Core interface {
	// MigrateRouting 对应核心的 migrateStoredRouting:把改版前的旧路由翻译成站点集。
	MigrateRouting(ctx context.Context, routing map[string]any) (out map[string]any, changed bool, err error)
	// NormalizeGroups 对应核心的 normalizeGroups:补全节点组字段并带上内置直连 / 拒绝。
	NormalizeGroups(ctx context.Context, groups []any) ([]any, error)
}

// Service 读写档案。
type Service struct {
	st   *store.Store
	core Core // 可为 nil
	log  *slog.Logger
}

// New 创建服务。c 可以为 nil(核心不可用时,旧路由迁移被跳过,默认节点组接口返回 ErrCoreUnavailable)。
func New(st *store.Store, c Core, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{st: st, core: c, log: log}
}

// legacyRoutingKeys 是改版前路由里的老字段,出现任何一个就需要走迁移。
var legacyRoutingKeys = []string{"regions", "regionId", "regionMode", "categories", "directRulesets", "fallback"}

func hasLegacyRouting(r map[string]any) bool {
	for _, k := range legacyRoutingKeys {
		if _, ok := r[k]; ok {
			return true
		}
	}
	return false
}

// effective 返回「默认值 + 已保存内容」合成的完整档案。
// 已保存过 routing 时不再叠默认 routing:老档案的旧字段和默认站点集混在一起会得到错误的迁移结果。
func effective(stored map[string]any) map[string]any {
	d := Default()
	if _, has := stored["routing"]; has {
		delete(d, "routing")
	}
	return Merge(d, stored)
}

// migrate 在需要时调用核心迁移旧路由,返回是否改动了 eff。核心不可用或失败时保持原样(只记日志)。
func (s *Service) migrate(ctx context.Context, eff map[string]any) bool {
	r, ok := eff["routing"].(map[string]any)
	if !ok || !hasLegacyRouting(r) || s.core == nil {
		return false
	}
	out, changed, err := s.core.MigrateRouting(ctx, r)
	if err != nil {
		s.log.Warn("迁移旧版分流失败,保持原样", "err", err)
		return false
	}
	if !changed {
		return false
	}
	eff["routing"] = out
	return true
}

// Get 返回完整档案。读到旧版分流时迁移并写回。
func (s *Service) Get(ctx context.Context) (map[string]any, error) {
	stored := map[string]any{}
	if _, err := s.st.Load(storeName, &stored); err != nil {
		return nil, fmt.Errorf("读取档案失败: %w", err)
	}
	eff := effective(stored)
	if s.migrate(ctx, eff) {
		return s.Patch(ctx, map[string]any{}) // 在锁里重新迁移并落盘
	}
	return eff, nil
}

// Patch 把 patch 深度合并进档案并保存,返回合并后的完整档案。
// 校验失败返回 *ValidationError,不改动已保存的内容。
func (s *Service) Patch(ctx context.Context, patch map[string]any) (map[string]any, error) {
	var result map[string]any
	stored := map[string]any{}
	err := s.st.Update(storeName, &stored, func() error {
		cur := effective(stored)
		s.migrate(ctx, cur)
		next := Merge(cur, prepare(cur, patch))
		if _, patched := patch["routing"]; patched {
			s.migrate(ctx, next) // 新写入的 routing 同样可能带旧字段(例如从旧备份导入)
		}
		if err := Check(next); err != nil {
			return &ValidationError{Msg: err.Error()}
		}
		stored, result = next, next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// prepare 清理 patch 里不该由客户端写的内容,返回新的 patch(不改入参)。
//   - rulesetDir 由服务端决定,客户端传的忽略;
//   - chainProxies[].node 是服务端按 link 解析出的只读摘要:link 没变就沿用旧摘要,否则丢弃(等服务端重新解析)。
func prepare(cur, patch map[string]any) map[string]any {
	out := make(map[string]any, len(patch))
	for k, v := range patch {
		if k == "rulesetDir" {
			continue
		}
		out[k] = v
	}
	if list, ok := out["chainProxies"].([]any); ok {
		out["chainProxies"] = keepChainSummaries(cur, list)
	}
	return out
}

func keepChainSummaries(cur map[string]any, list []any) []any {
	old := map[string]map[string]any{}
	if prev, ok := cur["chainProxies"].([]any); ok {
		for _, e := range prev {
			if m, ok := e.(map[string]any); ok {
				if id, _ := m["id"].(string); id != "" {
					old[id] = m
				}
			}
		}
	}
	out := make([]any, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e) // 交给 Check / 后续校验处理
			continue
		}
		c := Clone(m).(map[string]any)
		delete(c, "node")
		id, _ := c["id"].(string)
		if prev, found := old[id]; found && id != "" {
			if prev["link"] == c["link"] && prev["node"] != nil {
				c["node"] = prev["node"]
			}
		}
		out = append(out, c)
	}
	return out
}

// DefaultGroups 返回默认节点组(含内置直连 / 拒绝),由核心补全字段。
func (s *Service) DefaultGroups(ctx context.Context) ([]any, error) {
	if s.core == nil {
		return nil, ErrCoreUnavailable
	}
	return s.core.NormalizeGroups(ctx, DefaultGroupsInput())
}

// CoreAdapter 把 *core.Client 适配成 Core。
type CoreAdapter struct{ C *core.Client }

// MigrateRouting 实现 Core。
func (a CoreAdapter) MigrateRouting(ctx context.Context, routing map[string]any) (map[string]any, bool, error) {
	var res struct {
		Routing map[string]any `json:"routing"`
		Changed bool           `json:"changed"`
	}
	if err := a.C.CallInto(ctx, "migrateStoredRouting", []any{routing}, &res); err != nil {
		return nil, false, err
	}
	return res.Routing, res.Changed, nil
}

// NormalizeGroups 实现 Core。
func (a CoreAdapter) NormalizeGroups(ctx context.Context, groups []any) ([]any, error) {
	var out []any
	if err := a.C.CallInto(ctx, "normalizeGroups", []any{groups}, &out); err != nil {
		return nil, err
	}
	return out, nil
}
