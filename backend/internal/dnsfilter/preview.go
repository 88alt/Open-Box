package dnsfilter

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	previewTTL      = 60 * time.Second
	previewCacheMax = 2 // 一份名单最多 20 万条,路由器内存小,只留最近两份
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Fetcher 拉取名单文本(*rulelist.Fetch 的包装;测试里换假的)。
type Fetcher func(ctx context.Context, rawURL string) ([]byte, error)

type cachedList struct {
	p  *Parsed
	at time.Time
}

// Previewer 下载并解析名单、按条件分页返回。带一个很小的内存缓存:
// 前端搜索框边打边搜,每次都带同一个链接请求,不能每次重新下载。
type Previewer struct {
	Fetch Fetcher
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]cachedList
}

// NewPreviewer 创建预览服务。
func NewPreviewer(f Fetcher) *Previewer {
	return &Previewer{Fetch: f, now: time.Now, cache: map[string]cachedList{}}
}

// PreviewQuery 是预览的查询条件。
type PreviewQuery struct {
	URL      string
	Search   string
	Action   string // all / allow / block,空按 all
	Page     int
	PageSize int
}

// Preview 对应前端 DnsFilterPreview。
type Preview struct {
	Rows                []Entry  `json:"rows"`
	Count               int      `json:"count"`    // 名单里解析出的条目总数(不受搜索和动作影响)
	Total               int      `json:"total"`    // 符合当前搜索和动作的条数(分页用)
	Page                int      `json:"page"`     // 实际返回的页(越界时收回到最后一页)
	PageSize            int      `json:"pageSize"` // 实际使用的每页条数
	RuleCount           int      `json:"ruleCount"`
	Unsupported         int      `json:"unsupported"`
	UnsupportedExamples []string `json:"unsupportedExamples"`
	Source              string   `json:"source"`
	UpdatedAt           *int64   `json:"updatedAt"`
}

// Preview 现拉(60 秒内同一链接走缓存)、解析、过滤、分页。
// 名单还没有「下载并应用」这一步,所以 source 恒为 url、updatedAt 恒为 null。
func (p *Previewer) Preview(ctx context.Context, q PreviewQuery) (*Preview, error) {
	parsed, err := p.load(ctx, q.URL)
	if err != nil {
		return nil, err
	}
	size := q.PageSize
	if size < 1 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	action := q.Action
	if action != ActionAllow && action != ActionBlock {
		action = "all"
	}
	needle := strings.ToLower(strings.TrimSpace(q.Search))

	matched := make([]int, 0, 64)
	for i := range parsed.Entries {
		e := &parsed.Entries[i]
		if action != "all" && e.Action != action {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(e.Value), needle) && !strings.Contains(strings.ToLower(e.Rule), needle) {
			continue
		}
		matched = append(matched, i)
	}
	total := len(matched)
	pages := (total + size - 1) / size
	page := q.Page
	if page > pages {
		page = pages
	}
	if page < 1 {
		page = 1
	}
	rows := []Entry{}
	start := (page - 1) * size
	for i := start; i < total && i < start+size; i++ {
		rows = append(rows, parsed.Entries[matched[i]])
	}
	return &Preview{
		Rows: rows, Count: len(parsed.Entries), Total: total, Page: page, PageSize: size,
		RuleCount: parsed.RuleCount(), Unsupported: parsed.Unsupported,
		UnsupportedExamples: parsed.UnsupportedExamples, Source: "url",
	}, nil
}

func (p *Previewer) load(ctx context.Context, rawURL string) (*Parsed, error) {
	p.mu.Lock()
	if c, ok := p.cache[rawURL]; ok && p.now().Sub(c.at) < previewTTL {
		p.mu.Unlock()
		return c.p, nil
	}
	p.mu.Unlock()

	body, err := p.Fetch(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseList(body)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, has := p.cache[rawURL]; !has && len(p.cache) >= previewCacheMax {
		oldest, first := "", true
		var at time.Time
		for k, v := range p.cache {
			if first || v.at.Before(at) {
				oldest, at, first = k, v.at, false
			}
		}
		delete(p.cache, oldest)
	}
	p.cache[rawURL] = cachedList{p: parsed, at: p.now()}
	return parsed, nil
}
