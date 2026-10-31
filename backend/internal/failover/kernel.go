package failover

import (
	"context"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// ClashKernel 是 Kernel 的真实实现:经已部署配置里的控制接口(Clash API)读选择、测延迟、改选择。
// 内核没在运行 / 没开控制接口时,三个动作都返回 error,管理器据此「不测不切」。
type ClashKernel struct {
	// ConfigFile 是已部署的正式配置(从里面取控制接口的地址和 secret)。
	ConfigFile string
}

var _ Kernel = ClashKernel{}

// Selected 读各代理当前选中的出站(只有节点组有「当前选中」,别的代理对应空串)。
func (c ClashKernel) Selected(ctx context.Context) (map[string]string, error) {
	ps, err := singbox.KernelProxies(ctx, c.ConfigFile)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ps))
	for _, p := range ps {
		out[p.Name] = p.Now
	}
	return out, nil
}

// Delay 让内核经出站 name 访问 testURL(https)量端到端延迟。
func (c ClashKernel) Delay(ctx context.Context, name, testURL string, timeout time.Duration) (time.Duration, error) {
	return singbox.KernelDelay(ctx, c.ConfigFile, name, testURL, timeout)
}

// Select 把 selector 组 group 的当前选择改成 choice。
func (c ClashKernel) Select(ctx context.Context, group, choice string) error {
	return singbox.KernelSelect(ctx, c.ConfigFile, group, choice)
}
