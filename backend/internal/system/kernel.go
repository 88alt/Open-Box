package system

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 路由器内核:sing-box(默认)或 Mihomo。选择存在 <ConfigDir>/router-kernel 里,内容就是内核名;
// 文件不存在 = sing-box;内容既不是 sing-box 也不是 mihomo 就是无效(服务脚本会拒绝启动,见 STATUS.md 双内核第 6 步)。
// 对齐上游 v0.1.462 的 routerKernel 路径和读取逻辑(上游放在 <root>/etc,这里放在 ConfigDir,两边都是「配置目录」)。
const (
	KernelSingBox = "sing-box"
	KernelMihomo  = "mihomo"
)

// ErrInvalidRouterKernel 表示 router-kernel 文件的内容不是 sing-box / mihomo。
var ErrInvalidRouterKernel = errors.New("路由器内核选择文件无效")

// ValidKernel 报告 k 是不是支持的内核名。
func ValidKernel(k string) bool { return k == KernelSingBox || k == KernelMihomo }

// RouterKernelFile 是内核选择文件。
func (p Paths) RouterKernelFile() string { return filepath.Join(p.ConfigDir, "router-kernel") }

// MihomoConfigFile 是给 Mihomo 生成的配置(sing-box 用的是 config.json)。
func (p Paths) MihomoConfigFile() string { return filepath.Join(p.ConfigDir, "mihomo.yaml") }

// MihomoEntryBypassFile 是 Mihomo 的入口旁路 nft 规则;存在且非空时服务脚本导出 OPENBOX_MIHOMO_ENTRY_BYPASS=1。
func (p Paths) MihomoEntryBypassFile() string {
	return filepath.Join(p.ConfigDir, "mihomo-entry-bypass.nft")
}

// MihomoDnsmasqFile 是 Mihomo 下的 dnsmasq 模式标记(sing-box 下看 config.json 里的 DNS 入站)。
func (p Paths) MihomoDnsmasqFile() string { return filepath.Join(p.ConfigDir, "mihomo-dnsmasq") }

// MihomoInputsFile 记录上次部署 Mihomo 配置时的输入指纹(判断「配置改了、内核还没用上」)。
func (p Paths) MihomoInputsFile() string { return filepath.Join(p.ConfigDir, "mihomo.inputs") }

// MihomoHome 是 Mihomo 的工作目录(-d)。
func (p Paths) MihomoHome() string { return filepath.Join(p.DataDir, "mihomo") }

// ReadRouterKernel 读 path 里选中的内核。文件不存在返回 sing-box;内容无效返回 ErrInvalidRouterKernel;
// 其它读文件错误原样返回。path 为空按「没有选择文件」处理,也是 sing-box。
func ReadRouterKernel(path string) (string, error) {
	if path == "" {
		return KernelSingBox, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return KernelSingBox, nil
	}
	if err != nil {
		return "", fmt.Errorf("读取内核选择文件失败: %w", err)
	}
	k := strings.TrimSpace(string(b))
	if !ValidKernel(k) {
		return "", ErrInvalidRouterKernel
	}
	return k, nil
}
