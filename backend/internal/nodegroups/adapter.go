package nodegroups

import (
	"context"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
)

// CoreAdapter 把 *core.Client 适配成 Core。
type CoreAdapter struct{ C *core.Client }

// NormalizeGroups 实现 Core。
func (a CoreAdapter) NormalizeGroups(ctx context.Context, groups []any) ([]any, error) {
	var out []any
	if err := a.C.CallInto(ctx, "normalizeGroups", []any{groups}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EmitGroups 实现 Core:只取 dropped(options 传空,测速地址等不影响哪些组会被丢弃)。
func (a CoreAdapter) EmitGroups(ctx context.Context, groups []any, nodes []any) ([]Dropped, error) {
	var res struct {
		Dropped []Dropped `json:"dropped"`
	}
	if err := a.C.CallInto(ctx, "emitGroups", []any{groups, nodes, map[string]any{}}, &res); err != nil {
		return nil, err
	}
	return res.Dropped, nil
}
