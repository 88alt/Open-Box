package kernelgen

import (
	"context"
	"fmt"

	"github.com/timeflysoon/Open-Box/backend/internal/mihomo"
)

// MihomoBuilder 把 Generator 的 MihomoBuild 适配成 mihomo.Builder(mihomo.Deployer 要的形状)。
//
// 单独一个类型而不是给 Generator 加一个怪名字的方法,是为了让「部署流程」和「装配」两边的接口各说各的,
// 以后任一边改签名都不会牵动另一边。
type MihomoBuilder struct{ Gen *Generator }

// BuildMihomo 装配一份 mihomo 配置。
func (b MihomoBuilder) BuildMihomo(ctx context.Context) (*mihomo.BuildResult, error) {
	if b.Gen == nil {
		return nil, fmt.Errorf("没有 Mihomo 配置生成器")
	}
	return b.Gen.MihomoBuild(ctx)
}
