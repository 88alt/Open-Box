package subs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// Revision 是订阅数据的「版本」,给面板的长轮询(GET /api/openbox/subscriptions/updates)用:
// Revision 随订阅列表或节点的任何变化而变(含刷新时流量用量、更新时间变了),NodesRevision 只随节点变。
// 两者都是内容的摘要,不是计数器,所以后端重启后同样的数据还是同一个版本号。
type Revision struct {
	Revision      string `json:"revision"`
	NodesRevision string `json:"nodesRevision"`
}

func digest(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Revisions 读出当前的版本。
func (s *Service) Revisions() (Revision, error) {
	recs, nodes, err := s.loadAll()
	if err != nil {
		return Revision{}, err
	}
	if recs == nil {
		recs = []Record{}
	}
	// 只看还有订阅引用的节点(persist 也只留这些),孤儿节点不算变化
	live := make(map[string][]sub.Node, len(recs))
	for _, r := range recs {
		n := nodes[r.ID]
		if n == nil {
			n = []sub.Node{}
		}
		live[r.ID] = n
	}
	nb, err := json.Marshal(live) // map 的键按字母序输出,同样的内容摘要稳定
	if err != nil {
		return Revision{}, fmt.Errorf("计算节点版本失败: %w", err)
	}
	rb, err := json.Marshal(recs)
	if err != nil {
		return Revision{}, fmt.Errorf("计算订阅版本: %w", err)
	}
	nrev := digest(nb)
	return Revision{Revision: digest(rb, []byte(nrev)), NodesRevision: nrev}, nil
}

// changed 返回「下一次写盘」会关闭的通道。要在读版本之前拿,这样读完版本到开始等待之间的写不会漏掉。
func (s *Service) changed() <-chan struct{} {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.wake == nil {
		s.wake = make(chan struct{})
	}
	return s.wake
}

// notify 唤醒所有在等版本变化的人。订阅 / 节点每次写盘之后调(失败的写也调:可能已经写了一半)。
func (s *Service) notify() {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.wake != nil {
		close(s.wake)
		s.wake = nil
	}
}

// WaitRevision 是长轮询:since 为空或和当前版本不同就立刻返回当前版本;相同就等到版本变了、wait 到了
// (到点原样返回当前版本,由调用方再发下一轮)、或 ctx 取消(返回 ctx.Err())。
func (s *Service) WaitRevision(ctx context.Context, since string, wait time.Duration) (Revision, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		ch := s.changed()
		cur, err := s.Revisions()
		if err != nil {
			return Revision{}, err
		}
		if cur.Revision != since { // since 为空一定不等于真实的版本,也是立刻返回
			return cur, nil
		}
		select {
		case <-ch:
		case <-timer.C:
			return cur, nil
		case <-ctx.Done():
			return Revision{}, ctx.Err()
		}
	}
}
