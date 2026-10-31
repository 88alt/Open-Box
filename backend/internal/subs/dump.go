package subs

import (
	"context"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// Dump 返回全部订阅(按用户排序)和它们各自的节点(订阅 id → 节点),给备份导出用。
// 只读、不联网;返回的是存着的原样数据(含订阅链接和节点凭据),调用方要当敏感数据处理。
func (s *Service) Dump(_ context.Context) ([]Record, map[string][]sub.Node, error) {
	recs, nodes, err := s.loadAll()
	if err != nil {
		return nil, nil, err
	}
	if recs == nil {
		recs = []Record{}
	}
	out := make(map[string][]sub.Node, len(recs))
	for _, r := range recs {
		out[r.ID] = nodes[r.ID] // 只带还有订阅在引用的节点
	}
	return recs, out, nil
}
