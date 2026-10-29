package nodeexit

import (
	"context"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// ProbeVia 造出 Service.Probe:用完整配置摘出 tag 的出站,起临时 sing-box,经它取 urls 里第一个答话的正文。
// 不碰运行中的内核、不联网配置、不落盘(和节点测速同一套做法)。
func ProbeVia(gen *singbox.Generator, runner *probe.Runner, perTry time.Duration) ProbeFunc {
	return func(ctx context.Context, tag string, urls []string) (string, string, error) {
		built, err := gen.Build(ctx)
		if err != nil {
			return "", "", err
		}
		m := probe.Subset(built.Config, []string{tag})
		if !m.Has(tag) {
			return "", "", ErrNoOutbound
		}
		job := probe.Job{Tag: tag, URL: urls[0], Capture: 16 << 10, Fallback: urls[1:]}
		res, err := runner.Run(ctx, m, []probe.Job{job}, probe.Options{Timeout: perTry})
		if err != nil {
			return "", "", err
		}
		if len(res) == 0 || !res[0].OK {
			msg := "没有取到出口地址"
			if len(res) > 0 && res[0].Error != "" {
				msg = res[0].Error
			}
			return "", "", &ProbeError{Msg: msg}
		}
		return res[0].URL, res[0].Body, nil
	}
}
