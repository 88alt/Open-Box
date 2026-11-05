package contentfilter

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// 同步给 App 的内容过滤快照:启用的集合连文本一起交出去(路由器只存不执行,规则在 iOS 上执行)。
// 自己的设计,依据前端 api/clientApp.ts 对内容过滤的说明(「规则集和脚本快照存在路由器上,同步到 iOS 后在手机执行」)
// 和 OpenboxContentFilterList 的字段,没有参照上游。
//
// 上限:启用集合的规范化内容总大小(就是 GET /content-filter 里的 size)最多 MaxSnapshotSize。
// 单份文本在存盘时已限在 MaxTextBytes(512 KiB),所以总量才是要防的:50 份各 512 KiB 能到 25 MiB,
// 一次同步要在内存里留明文、密文、base64 好几份,路由器上吃不消。超了不截断、不挑几份发,
// 如实报 *TooLargeError,让用户自己停用或删掉一些。

// MaxSnapshotSize 是快照里启用集合的规范化内容总大小上限(字节)。
const MaxSnapshotSize = 4 << 20

// SnapshotSet 是快照里的一份规则集:只给 App 执行要用的字段,不带 sourceUrl(链接里常有 token)和 arguments
// (文本在存盘时已经代入过参数)。
type SnapshotSet struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Dialect     Dialect `json:"dialect"`
	RuleCount   int     `json:"ruleCount"`
	ScriptCount int     `json:"scriptCount"`
	UpdatedAt   string  `json:"updatedAt"`
	Text        string  `json:"text"`
}

// Snapshot 是启用集合的快照。SHA256 / Size 与 GET /content-filter 的同名字段同一口径,App 拿它判断本地的快照要不要换;
// Sets 按 id 排序,没有启用的集合时是空数组(不是 null)。
type Snapshot struct {
	SHA256 string        `json:"sha256"`
	Size   int           `json:"size"`
	Sets   []SnapshotSet `json:"sets"`
}

// TooLargeError:启用集合的总大小超过上限,快照不给。
type TooLargeError struct{ Size, Max int }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("启用的内容过滤规则共 %s,超过同步上限 %s", humanSize(e.Size), humanSize(e.Max))
}

func humanSize(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", (n+1023)>>10)
}

// Snapshot 返回启用集合的快照。limit <= 0 用 MaxSnapshotSize。先用列表里缓存的 size 判断,超了就不读任何文本。
// 全程持着 s.mu:新建 / 更新 / 启停 / 删除都要拿同一把锁,所以拿到的记录、文本、摘要是同一时刻的,不会半新半旧。
func (s *Service) Snapshot(_ context.Context, limit int) (Snapshot, error) {
	if limit <= 0 {
		limit = MaxSnapshotSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.listLocked()
	if err != nil {
		return Snapshot{}, err
	}
	if l.Size > limit {
		return Snapshot{}, &TooLargeError{Size: l.Size, Max: limit}
	}
	on := make([]Set, 0, len(l.Sets))
	for _, st := range l.Sets {
		if st.Enabled {
			on = append(on, st)
		}
	}
	slices.SortFunc(on, func(a, b Set) int { return strings.Compare(a.ID, b.ID) })
	out := make([]SnapshotSet, 0, len(on))
	for _, st := range on {
		text, err := s.readText(st.ID)
		if err != nil {
			return Snapshot{}, err
		}
		out = append(out, SnapshotSet{
			ID: st.ID, Name: st.Name, Dialect: st.Dialect,
			RuleCount: st.RuleCount, ScriptCount: st.ScriptCount, UpdatedAt: st.UpdatedAt, Text: text,
		})
	}
	return Snapshot{SHA256: l.SHA256, Size: l.Size, Sets: out}, nil
}
