package singbox

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// KernelVersion 对应前端的 OpenboxKernelVersion。
type KernelVersion struct {
	Version string `json:"version"` // 如 1.14.4;没读到是空字符串
	Raw     string `json:"raw"`     // `sing-box version` 的原始输出(截断)
	OK      bool   `json:"ok"`      // false = sing-box 缺失或无法执行
}

const versionTimeout = 10 * time.Second

var versionRe = regexp.MustCompile(`(?m)^sing-box version (\S+)`)

// ReadKernelVersion 运行 `<bin> version` 并解析版本号。读不到不是 error,返回 OK=false。
func ReadKernelVersion(ctx context.Context, bin string) KernelVersion {
	if bin == "" {
		bin = "sing-box"
	}
	exe, err := exec.LookPath(bin)
	if err != nil {
		return KernelVersion{}
	}
	cctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out := &limitedWriter{max: 2048}
	cmd := exec.CommandContext(cctx, exe, "version")
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return KernelVersion{Raw: strings.TrimSpace(out.buf.String())}
	}
	raw := strings.TrimSpace(out.buf.String())
	m := versionRe.FindStringSubmatch(raw)
	if m == nil {
		return KernelVersion{Raw: raw}
	}
	return KernelVersion{Version: m[1], Raw: raw, OK: true}
}

// ReadMihomoVersion 运行 `<bin> -v` 并取标准输出的第一行当版本(如「Mihomo Meta v1.19.0 linux amd64 …」),
// 对齐上游 v0.1.462 的读法:退出码 0 且第一行非空才算 OK;Raw 是标准输出加标准错误(截断)。
// 面板自己把这行整理成 mihomo-1.19.0 的样子(KernelServiceCard.vue),所以这里不解析。
// 读不到不是 error,返回 OK=false。
func ReadMihomoVersion(ctx context.Context, bin string) KernelVersion {
	if bin == "" {
		bin = "mihomo"
	}
	exe, err := exec.LookPath(bin)
	if err != nil {
		return KernelVersion{}
	}
	cctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	stdout, stderr := &limitedWriter{max: 2048}, &limitedWriter{max: 2048}
	cmd := exec.CommandContext(cctx, exe, "-v")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	raw := strings.TrimSpace(stdout.buf.String() + stderr.buf.String())
	if runErr != nil {
		return KernelVersion{Raw: raw}
	}
	first, _, _ := strings.Cut(stdout.buf.String(), "\n")
	first = strings.TrimSpace(first)
	return KernelVersion{Version: first, Raw: raw, OK: first != ""}
}
