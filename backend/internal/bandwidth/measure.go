package bandwidth

import (
	"context"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/probe"
)

const defaultBWURL = "https://speed.cloudflare.com/__down?bytes=104857600" // 100 MiB 源,读到上限即停

// ConfigMaterial 提供「完整配置 + 摘出被测节点」的能力(由 singbox.Generator 装配)。
type ConfigMaterial interface {
	// MaterialFor 返回含 tags 出站的 probe.Material;配置不可用时返回错误。
	MaterialFor(ctx context.Context, tags []string) (*probe.Material, error)
}

// ProbeRunner 抽象 probe.Runner.Run。
type ProbeRunner interface {
	Run(ctx context.Context, m *probe.Material, jobs []probe.Job, opt probe.Options) ([]probe.Result, error)
}

// TestURL 返回带宽测速下载地址;空则用默认 Cloudflare 测速。
type TestURL func(ctx context.Context) string

// NewProbeMeasurer 经临时 sing-box 出站下载测带宽(与延迟探测同一套 probe)。
func NewProbeMeasurer(mat ConfigMaterial, run ProbeRunner, url TestURL) Measurer {
	return func(ctx context.Context, node string, maxBytes int64) MeasureResult {
		if mat == nil || run == nil {
			return MeasureResult{Failed: true, Error: "measurer not configured", Reason: "unavailable"}
		}
		if maxBytes <= 0 {
			maxBytes = 50 << 20
		}
		m, err := mat.MaterialFor(ctx, []string{node})
		if err != nil {
			return MeasureResult{Failed: true, Error: err.Error(), Reason: "invalid"}
		}
		if m == nil || !m.Has(node) {
			return MeasureResult{Failed: true, Error: "找不到这个节点", Reason: "not-found"}
		}
		u := defaultBWURL
		if url != nil {
			if s := strings.TrimSpace(url(ctx)); s != "" {
				u = s
			}
		}
		// 按上限估超时:至少 30s,大约按 2 Mbps 下完,封顶 3 分钟
		sec := int(maxBytes / (256 * 1024)) // ~2Mbps
		if sec < 30 {
			sec = 30
		}
		if sec > 180 {
			sec = 180
		}
		res, err := run.Run(ctx, m, []probe.Job{{Tag: node, URL: u, Discard: maxBytes}}, probe.Options{
			Timeout: time.Duration(sec) * time.Second,
		})
		if err != nil {
			return MeasureResult{Failed: true, Error: err.Error(), Reason: "failed"}
		}
		if len(res) == 0 {
			return MeasureResult{Failed: true, Error: "无结果", Reason: "failed"}
		}
		r := res[0]
		if !r.OK {
			return MeasureResult{Failed: true, Error: r.Error, Reason: r.Reason}
		}
		if r.Ms < 1 {
			r.Ms = 1
		}
		mbps := (float64(r.Bytes) * 8) / (float64(r.Ms) / 1000.0) / 1e6
		lower := r.Bytes >= maxBytes
		return MeasureResult{Mbps: mbps, LowerBound: lower}
	}
}

// MaterialFunc 把函数适配为 ConfigMaterial。
type MaterialFunc func(ctx context.Context, tags []string) (*probe.Material, error)

func (f MaterialFunc) MaterialFor(ctx context.Context, tags []string) (*probe.Material, error) {
	return f(ctx, tags)
}

// RunnerFunc 适配。
type RunnerFunc func(ctx context.Context, m *probe.Material, jobs []probe.Job, opt probe.Options) ([]probe.Result, error)

func (f RunnerFunc) Run(ctx context.Context, m *probe.Material, jobs []probe.Job, opt probe.Options) ([]probe.Result, error) {
	return f(ctx, m, jobs, opt)
}
