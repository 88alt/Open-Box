package rulelist

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	cacheTTL = 60 * time.Second
	cacheMax = 8
)

// Fetcher 拉取名单文本(测试里换成假的)。
type Fetcher func(ctx context.Context, rawURL string) ([]byte, error)

type cached struct {
	res *Result
	at  time.Time
}

// Service 带一个很小的内存缓存:前端搜索框边打边搜,每次按键都会带同一个链接请求预览,
// 不能每次都重新下载整份名单。
type Service struct {
	fetch Fetcher
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]cached
}

// NewService 创建服务。fetch 为 nil 时用 Fetch。
func NewService(fetch Fetcher) *Service {
	if fetch == nil {
		client := &http.Client{Timeout: fetchTimeout}
		fetch = func(ctx context.Context, u string) ([]byte, error) { return Fetch(ctx, client, u) }
	}
	return &Service{fetch: fetch, now: time.Now, cache: map[string]cached{}}
}

// Load 返回链接对应的解析结果(60 秒内同一链接走缓存)。
func (s *Service) Load(ctx context.Context, rawURL string) (*Result, error) {
	return s.load(ctx, rawURL, false)
}

// Reload 跳过缓存重新拉取(「立即更新」用),成功后用新结果刷新缓存,
// 之后的预览 / 域名穿透看到的就是刚拉到的这一份。拉取失败不动缓存。
func (s *Service) Reload(ctx context.Context, rawURL string) (*Result, error) {
	return s.load(ctx, rawURL, true)
}

func (s *Service) load(ctx context.Context, rawURL string, force bool) (*Result, error) {
	if !force {
		s.mu.Lock()
		if c, ok := s.cache[rawURL]; ok && s.now().Sub(c.at) < cacheTTL {
			s.mu.Unlock()
			return c.res, nil
		}
		s.mu.Unlock()
	}

	body, err := s.fetch(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	res, err := Parse(body)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, has := s.cache[rawURL]; !has && len(s.cache) >= cacheMax {
		oldest, first := "", true
		var at time.Time
		for k, v := range s.cache {
			if first || v.at.Before(at) {
				oldest, at, first = k, v.at, false
			}
		}
		delete(s.cache, oldest)
	}
	s.cache[rawURL] = cached{res: res, at: s.now()}
	return res, nil
}
