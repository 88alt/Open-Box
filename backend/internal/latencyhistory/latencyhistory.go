// Package latencyhistory 攒每个节点 / 节点组最近 10 次测速结果,后端记、所有浏览器共享。
// 响应形状取自前端 api/openbox.ts 的 OpenboxLatencyHistory 和 store/latencyHistory.ts 的用法,没有参照上游:
//   - 样本 {time, delay, node?}:time 是 RFC3339 时间,delay 是毫秒,0 表示不通;
//     节点组的样本带 node(当时选中的节点),节点自己的没有;
//   - 每个名字只留最新 10 条,按时间从旧到新;同一时刻只留一条;
//   - updatedAt 是最近一次真有内容变化的时刻(毫秒),前端每 15 秒拿它和手头的比,变了才拉整份。
package latencyhistory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	// MaxSamples 每个名字最多留几条(与前端 MAX_LATENCY_HISTORY 一致)。
	MaxSamples = 10
	// MaxNames 最多记多少个名字,防止文件无限长大。
	MaxNames = 5000
	// MaxPerRequest 一次 POST samples 最多几条。
	MaxPerRequest = 500
	maxNameLen    = 200
	maxDelayMs    = 10 * 60 * 1000
	fileName      = "latency-history"
)

// Sample 是一次测速结果。
type Sample struct {
	Time  string `json:"time"`
	Delay int    `json:"delay"`
	Node  string `json:"node,omitempty"`
}

// History 是名字 → 样本(从旧到新)。
type History map[string][]Sample

// Input 是面板上报的一条(超时):没有 node。
type Input struct {
	Name  string `json:"name"`
	Time  string `json:"time"`
	Delay int    `json:"delay"`
}

// InputError 是上报的内容有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

type file struct {
	History   History `json:"history"`
	UpdatedAt int64   `json:"updatedAt"`
}

// Service 读写延迟历史。
type Service struct {
	Store *store.Store
	// Kernel 读内核当前的代理和测速记录;nil 或返回 error 时 Sync 只回现有的历史。
	Kernel func(ctx context.Context) ([]singbox.KernelProxy, error)
	Now    func() time.Time
	// File 是存放历史的文件名(在 store 里);空 = "latency-history"。站点延迟用另一个文件,和节点历史互不混。
	File string
}

func (s *Service) file() string {
	if s.File != "" {
		return s.File
	}
	return fileName
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Get 返回整份历史和 updatedAt(从没记过是空表和 0)。
func (s *Service) Get(context.Context) (History, int64, error) {
	f, err := s.load()
	if err != nil {
		return nil, 0, err
	}
	return f.History, f.UpdatedAt, nil
}

func (s *Service) load() (*file, error) {
	f := &file{}
	if _, err := s.Store.Load(s.file(), f); err != nil {
		return nil, fmt.Errorf("读取延迟历史失败: %w", err)
	}
	if f.History == nil {
		f.History = History{}
	}
	return f, nil
}

// Add 记面板上报的样本(超时),返回整份最新的历史。任何一条不合法整批拒绝,什么都不记。
func (s *Service) Add(_ context.Context, in []Input) (History, error) {
	if len(in) > MaxPerRequest {
		return nil, &InputError{Msg: fmt.Sprintf("一次最多上报 %d 条", MaxPerRequest)}
	}
	type item struct {
		name string
		smp  Sample
	}
	items := make([]item, 0, len(in))
	for i, x := range in {
		name := strings.TrimSpace(x.Name)
		if name == "" || len([]rune(name)) > maxNameLen {
			return nil, &InputError{Msg: fmt.Sprintf("第 %d 条的名字为空或太长", i+1)}
		}
		t, err := parseTime(x.Time)
		if err != nil {
			return nil, &InputError{Msg: fmt.Sprintf("第 %d 条的时间不是有效的 RFC3339 时间", i+1)}
		}
		if x.Delay < 0 || x.Delay > maxDelayMs {
			return nil, &InputError{Msg: fmt.Sprintf("第 %d 条的延迟不在 0 – %d 毫秒内", i+1, maxDelayMs)}
		}
		items = append(items, item{name, Sample{Time: t.UTC().Format(time.RFC3339Nano), Delay: x.Delay}})
	}
	var out History
	err := s.mutate(func(f *file) bool {
		changed := false
		for _, it := range items {
			changed = merge(f, it.name, it.smp) || changed
		}
		out = f.History
		return changed
	})
	return out, err
}

// Item 是要记的一条带线路的样本(站点延迟用)。
type Item struct {
	Name   string
	Sample Sample
}

// Record 记几条样本(调用方保证内容合法:名字非空、时间是 RFC3339、延迟在范围内),返回整份最新的历史。
func (s *Service) Record(_ context.Context, items []Item) (History, error) {
	var out History
	err := s.mutate(func(f *file) bool {
		changed := false
		for _, it := range items {
			changed = merge(f, it.Name, it.Sample) || changed
		}
		out = f.History
		return changed
	})
	if err != nil {
		return nil, err
	}
	if out == nil { // 没有变化时 mutate 不回调结果:读现有的
		h, _, err := s.Get(context.Background())
		return h, err
	}
	return out, nil
}

// Sync 读一次内核,把它记着的测速结果合进来,返回整份历史。内核读不到不是错误(没有新东西可合),照样回现有的。
// 内核里 delay 为 0 的不记(测失败时内核是删记录,不会留 0;超时由面板自己上报)。
func (s *Service) Sync(ctx context.Context) (History, error) {
	var proxies []singbox.KernelProxy
	if s.Kernel != nil {
		if p, err := s.Kernel(ctx); err == nil {
			proxies = p
		}
	}
	var out History
	err := s.mutate(func(f *file) bool {
		changed := false
		for _, p := range proxies {
			if strings.TrimSpace(p.Name) == "" || len([]rune(p.Name)) > maxNameLen {
				continue
			}
			for _, pt := range p.History {
				t, err := parseTime(pt.Time)
				if err != nil || pt.Delay <= 0 || pt.Delay > maxDelayMs {
					continue
				}
				smp := Sample{Time: t.UTC().Format(time.RFC3339Nano), Delay: pt.Delay}
				if isGroup(p.Type) {
					smp.Node = p.Now
				}
				changed = merge(f, p.Name, smp) || changed
			}
		}
		out = f.History
		return changed
	})
	return out, err
}

func isGroup(typ string) bool {
	switch strings.ToLower(typ) {
	case "selector", "urltest", "fallback", "loadbalance":
		return true
	}
	return false
}

var errNoChange = errors.New("no change")

// mutate 在 store 的锁里读改写;fn 返回 false 表示没变化:不写盘(路由器闪存)、不动 updatedAt。
func (s *Service) mutate(fn func(f *file) bool) error {
	f := &file{}
	err := s.Store.Update(s.file(), f, func() error {
		if f.History == nil {
			f.History = History{}
		}
		if !fn(f) {
			return errNoChange
		}
		f.UpdatedAt = s.now().UnixMilli()
		return nil
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

// merge 把一条样本并进 name 的列表:同一时刻已有就不动;超出 10 条丢最旧的。返回是否有变化。
func merge(f *file, name string, smp Sample) bool {
	list, exists := f.History[name]
	if !exists && len(f.History) >= MaxNames {
		return false
	}
	nt, _ := parseTime(smp.Time)
	for _, e := range list {
		if et, err := parseTime(e.Time); err == nil && et.Equal(nt) {
			return false
		}
	}
	list = append(append(make([]Sample, 0, len(list)+1), list...), smp) // 复制一份:别在 map 里那个切片的底层数组上排序
	sort.SliceStable(list, func(i, j int) bool {
		a, _ := parseTime(list[i].Time)
		b, _ := parseTime(list[j].Time)
		return a.Before(b)
	})
	if len(list) > MaxSamples {
		list = list[len(list)-MaxSamples:]
		kept := false
		for _, e := range list {
			kept = kept || e.Time == smp.Time
		}
		if !kept { // 比现有的最旧一条还旧,挤进来就被截掉:等于没变化
			return false
		}
	}
	f.History[name] = list
	return true
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	return time.Parse(time.RFC3339Nano, s)
}
