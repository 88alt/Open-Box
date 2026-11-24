package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// 把装配结果落盘,并做语法校验(双内核 3e-5 的收尾部分)。
//
// 上游 di 只回内存里的对象,落盘在切换内核那段(`D=async({config,entry,failover,monitoring,configText,...})`)里做:
//   - `<etc>/mihomo.yaml` 写整份配置(0600);
//   - `<etc>/mihomo-entry-bypass.nft` 写入口 nft(内容后面补一个换行),没有就删掉;
//   - `<etc>/mihomo.inputs` 写一份快照 JSON(generatedAt / kernel / failover / monitoring /
//     可选 warning / nodeDirectResolution / dnsFilter);
//   - `<etc>/router-kernel` 写 `mihomo` 一个词;
//   - OpenWrt 的 dnsmasq 模式再写 `<etc>/mihomo-dnsmasq`(内容 `dnsmasq`),否则删掉。
//
// 这些文件名与内容都按上游口径;真正的「切换 + 失败回滚 + 拉起服务」在第 5 步做,这里只负责把文件写对。

// ArtifactPaths 是落盘用到的路径(对应上游 Tr() 里的 mihomoConfig / mihomoEntryBypass / mihomoInputs / routerKernel / mihomoDnsmasq)。
type ArtifactPaths struct {
	Config       string
	Inputs       string
	EntryBypass  string
	RouterKernel string
	DNSMasq      string
}

// ArtifactMeta 是 inputs 快照里除配置以外的字段。
type ArtifactMeta struct {
	Kernel               string
	Failover             json.RawMessage
	Monitoring           json.RawMessage
	Warning              string
	NodeDirectResolution any
	DNSFilterEnabled     bool
	DNSFilterKey         string
	DNSMasq              bool // OpenWrt 且档案 dns.mode 是 dnsmasq
	GeneratedAt          time.Time
}

// WriteArtifacts 把装配结果写成 mihomo.yaml 与几个伴生文件。空内容按上游的做法删除旧文件。
func WriteArtifacts(fs FS, paths ArtifactPaths, res *BuildResult, meta ArtifactMeta) error {
	if res == nil || res.Config == nil {
		return errors.New("mihomo: 没有装配结果可写")
	}
	text, err := MarshalYAML(res.Config)
	if err != nil {
		return err
	}
	if err := fs.WriteFile(paths.Config, text, 0o600); err != nil {
		return err
	}
	if err := writeOrRemove(fs, paths.EntryBypass, res.Entry, "\n"); err != nil {
		return err
	}
	if err := writeOrRemove(fs, paths.RouterKernel, "mihomo", "\n"); err != nil {
		return err
	}
	if paths.DNSMasq != "" {
		if err := writeOrRemove(fs, paths.DNSMasq, map[bool]string{true: "dnsmasq", false: ""}[meta.DNSMasq], "\n"); err != nil {
			return err
		}
	}
	if paths.Inputs == "" {
		return nil
	}
	snap := NewOrdered()
	at := meta.GeneratedAt
	if at.IsZero() {
		at = time.Now()
	}
	kernel := meta.Kernel
	if kernel == "" {
		kernel = "mihomo"
	}
	snap.Set("generatedAt", at.UTC().Format("2006-01-02T15:04:05.000Z"))
	snap.Set("kernel", kernel)
	snap.Set("failover", rawOrEmptyArray(meta.Failover))
	snap.Set("monitoring", rawOrEmptyArray(meta.Monitoring))
	if meta.Warning != "" {
		snap.Set("warning", meta.Warning)
	}
	if meta.NodeDirectResolution != nil {
		snap.Set("nodeDirectResolution", meta.NodeDirectResolution)
	}
	filter := NewOrdered()
	filter.Set("enabled", meta.DNSFilterEnabled)
	filter.Set("key", meta.DNSFilterKey)
	snap.Set("dnsFilter", filter)
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return fs.WriteFile(paths.Inputs, data, 0o600)
}

func writeOrRemove(fs FS, path, text, suffix string) error {
	if path == "" {
		return nil
	}
	if text == "" {
		if fs.Exists(path) {
			return fs.Remove(path)
		}
		return nil
	}
	return fs.WriteFile(path, []byte(text+suffix), 0o600)
}

func rawOrEmptyArray(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return []any{}
	}
	return raw
}

// CheckArgs 是 `mihomo -t` 的参数(-d 是工作目录,-f 是要检查的配置文件)。
// 单独拆出来是为了在没装 Mihomo 的机器上也能核对参数形状。
func CheckArgs(configPath, homeDir string) []string {
	args := []string{"-t"}
	if homeDir != "" {
		args = append(args, "-d", homeDir)
	}
	return append(args, "-f", configPath)
}

// Check 跑 `mihomo -t`,只回「通过 / 失败(带输出)」。超时 20 秒。
// 原版 Mihomo 不认 openbox-* 补丁键时会报出来——装配出来的配置要过这一关,得用发布包里那份补丁内核。
func Check(ctx context.Context, exe, configPath, homeDir string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, CheckArgs(configPath, homeDir)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mihomo 配置校验失败:%s", trimOutput(out.String(), err))
	}
	return nil
}

func trimOutput(out string, err error) string {
	out = string(bytes.TrimSpace([]byte(out)))
	if out == "" {
		return err.Error()
	}
	if len(out) > 2000 {
		out = out[:2000] + "…"
	}
	return out
}
