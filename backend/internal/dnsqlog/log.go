package dnsqlog

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 前端 DnsFilterRecord.result 认得的取值里,本包会产生这几种。
const (
	ResultBlocked = "blocked" // 内核的过滤回了 NXDOMAIN,且域名确实在已应用的名单里
	ResultAllowed = "allowed" // 上游给了正常应答(含 NODATA 和真正的 NXDOMAIN)
	ResultCached  = "cached"  // dnsmasq 用缓存答的
	ResultLocal   = "local"   // dnsmasq 用本地记录(hosts / DHCP 名字 / config)答的
	ResultError   = "error"   // SERVFAIL / REFUSED 等
	ResultUnknown = "unknown" // 记下了查询,但没等到应答
)

const (
	// MaxRecords 是内存里保留的查询记录条数(最新的优先)。
	MaxRecords = 2000
	// 统计保留的小时数;摘要只回最近 24 个。
	keepHours = 48
	// 每个小时桶里最多记多少个不同的域名(满了以后只给已有的域名加数),给热门域名排行用。
	maxHourDomains = 300
	// 查询多久没等到应答就不再等。
	pendingTTL = 15 * time.Second
	// 同一个域名同时挂着的查询上限(防止异常日志撑爆内存)。
	maxPendingPerName = 16
	maxPendingNames   = 4096
)

// Record 对应前端 DnsFilterRecord。At 是毫秒时间戳;Elapsed 是毫秒,没测到是 null。
type Record struct {
	ID      int64    `json:"id"`
	At      int64    `json:"at"`
	Domain  string   `json:"domain"`
	QType   string   `json:"qtype"`
	Source  string   `json:"source"`
	Result  string   `json:"result"`
	List    string   `json:"list"`
	Elapsed *float64 `json:"elapsed"`
	Via     string   `json:"via,omitempty"`
}

type hourAgg struct {
	Queries int            `json:"queries"`
	Blocked int            `json:"blocked"`
	Elapsed float64        `json:"elapsed"`
	Timed   int            `json:"timed"`
	Domains map[string]int `json:"domains,omitempty"`
}

type pend struct {
	rec  *Record
	at   time.Time
	hour int64
}

// Classifier 判断一个域名是不是被已应用的名单拦截,返回命中的名单名。只在应答是 NXDOMAIN 时才会问。
type Classifier func(domain string) (blocked bool, list string)

// Log 收日志行、关联「查询 → 应答」、保存最近的记录和按小时统计。零值不可用,用 New。并发安全。
type Log struct {
	// Classify 为空时所有 NXDOMAIN 都按 allowed 记(没有名单可对)。创建后、开始 Feed 之前设置。
	Classify Classifier

	// TrimAbove 大于 0 时,跟读到文件末尾、文件已超过这么多字节,并且 Owned 返回真,就把文件清空(dnsmasq 以追加方式写,
	// 清空后接着从头写),免得内存盘上的日志越写越大。Owned 为空当作假:不是我们让 dnsmasq 写的文件(用户自己指定的)不碰。
	// 创建后、开始跟读之前设置。
	TrimAbove int64
	Owned     func() bool

	connected atomic.Bool

	mu      sync.Mutex
	nextID  int64
	recs    []*Record // 新的在后,最多 MaxRecords
	hours   map[int64]*hourAgg
	pending map[string][]pend
	dirty   bool
}

// New 创建空的 Log。
func New() *Log {
	return &Log{nextID: 1, hours: map[int64]*hourAgg{}, pending: map[string][]pend{}}
}

// SetConnected 由日志来源(文件跟读)设置:日志文件现在读得到。
func (l *Log) SetConnected(v bool) { l.connected.Store(v) }

// Connected 报告日志来源是否在工作(没配路径、文件不存在或读不了都是假)。
func (l *Log) Connected() bool { return l.connected.Load() }

func hourOf(t time.Time) int64 { return t.Truncate(time.Hour).UnixMilli() }

func (l *Log) bucket(h int64) *hourAgg {
	b := l.hours[h]
	if b == nil {
		b = &hourAgg{}
		l.hours[h] = b
		if len(l.hours) > keepHours {
			keys := make([]int64, 0, len(l.hours))
			for k := range l.hours {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
			for _, k := range keys[:len(keys)-keepHours] {
				delete(l.hours, k)
			}
		}
	}
	return b
}

// Feed 处理一行日志;now 是读到这一行的时间(用来算耗时和归到哪个小时)。
func (l *Log) Feed(line string, now time.Time) {
	p := ParseLine(line)
	if p.Kind == KindIgnored || p.Kind == KindForwarded {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(now)
	switch p.Kind {
	case KindQuery:
		l.onQuery(p, now)
	case KindReply, KindCached, KindLocal:
		l.onAnswer(p, now)
	}
}

func (l *Log) onQuery(p Line, now time.Time) {
	r := &Record{ID: l.nextID, At: now.UnixMilli(), Domain: p.Domain, QType: p.QType, Source: p.Source, Result: ResultUnknown, Via: "dnsmasq"}
	l.nextID++
	l.recs = append(l.recs, r)
	if len(l.recs) > MaxRecords {
		l.recs = append(l.recs[:0:0], l.recs[len(l.recs)-MaxRecords:]...)
	}
	h := hourOf(now)
	b := l.bucket(h)
	b.Queries++
	if b.Domains == nil {
		b.Domains = map[string]int{}
	}
	if _, ok := b.Domains[p.Domain]; ok || len(b.Domains) < maxHourDomains {
		b.Domains[p.Domain]++
	}
	if len(l.pending) < maxPendingNames || len(l.pending[p.Domain]) > 0 {
		q := l.pending[p.Domain]
		if len(q) >= maxPendingPerName {
			q = q[1:]
		}
		l.pending[p.Domain] = append(q, pend{rec: r, at: now, hour: h})
	}
	l.dirty = true
}

// onAnswer 把应答配给同一个域名最早还在等的那条查询(日志没有序号时只能这样配;
// 同一个域名并发查 A 和 AAAA 时,谁先拿到应答不重要,两条都会被填上)。
func (l *Log) onAnswer(p Line, now time.Time) {
	q := l.pending[p.Domain]
	if len(q) == 0 {
		return
	}
	w := q[0]
	if len(q) == 1 {
		delete(l.pending, p.Domain)
	} else {
		l.pending[p.Domain] = q[1:]
	}
	res, list := l.classify(p)
	w.rec.Result, w.rec.List = res, list
	b := l.hours[w.hour]
	if res == ResultBlocked && b != nil {
		b.Blocked++
	}
	if p.Kind == KindReply { // 缓存和本地记录几乎不耗时,算进平均值只会把数字拉低到没有意义
		ms := float64(now.Sub(w.at).Microseconds()) / 1000
		w.rec.Elapsed = &ms
		if b != nil {
			b.Elapsed += ms
			b.Timed++
		}
	}
	l.dirty = true
}

func (l *Log) classify(p Line) (result, list string) {
	ans := strings.ToUpper(p.Answer)
	if p.Kind == KindLocal {
		return ResultLocal, ""
	}
	switch {
	case ans == "NXDOMAIN":
		if l.Classify != nil {
			if blocked, name := l.Classify(p.Domain); blocked {
				return ResultBlocked, name
			}
		}
	case strings.Contains(ans, "SERVFAIL"), strings.Contains(ans, "REFUSED"), strings.Contains(ans, "FORMERR"), strings.Contains(ans, "NOTIMP"), strings.Contains(ans, "ERROR"):
		return ResultError, ""
	}
	if p.Kind == KindCached {
		return ResultCached, ""
	}
	return ResultAllowed, ""
}

func (l *Log) expire(now time.Time) {
	for d, q := range l.pending {
		i := 0
		for i < len(q) && now.Sub(q[i].at) > pendingTTL {
			i++
		}
		switch {
		case i == len(q):
			delete(l.pending, d)
		case i > 0:
			l.pending[d] = q[i:]
		}
	}
}

// Query 是记录列表的筛选条件。Search 在域名、来源地址、名单名里找子串(不分大小写);
// Result 为空是全部,"dnsmasq" 是 dnsmasq 自己答的(缓存 + 本地记录),其余按 result 精确匹配。
type Query struct {
	Search   string
	Result   string
	Page     int
	PageSize int
}

// Page 是一页记录。
type Page struct {
	Rows     []Record `json:"rows"`
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
}

// Records 返回一页记录,新的在前。页码和每页条数收回到合理范围(每页最多 100,默认 20)。
func (l *Log) Records(q Query) Page {
	size := q.PageSize
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	want := strings.TrimSpace(q.Result)
	l.mu.Lock()
	var hit []*Record
	for i := len(l.recs) - 1; i >= 0; i-- {
		r := l.recs[i]
		if !matchResult(r.Result, want) {
			continue
		}
		if search != "" && !strings.Contains(r.Domain, search) && !strings.Contains(strings.ToLower(r.Source), search) && !strings.Contains(strings.ToLower(r.List), search) {
			continue
		}
		hit = append(hit, r)
	}
	total := len(hit)
	page := q.Page
	if last := (total + size - 1) / size; page > last {
		page = last
	}
	if page < 1 {
		page = 1
	}
	from := (page - 1) * size
	to := from + size
	if from > total {
		from = total
	}
	if to > total {
		to = total
	}
	rows := make([]Record, 0, to-from)
	for _, r := range hit[from:to] {
		rows = append(rows, *r)
	}
	l.mu.Unlock()
	return Page{Rows: rows, Total: total, Page: page, PageSize: size}
}

func matchResult(got, want string) bool {
	switch want {
	case "":
		return true
	case "dnsmasq":
		return got == ResultCached || got == ResultLocal
	}
	return got == want
}

// Hour 是摘要里的一个小时桶。Hour 是这个小时开始的毫秒时间戳;Elapsed 是耗时总和(毫秒),Timed 是有耗时的条数。
type Hour struct {
	Hour    int64   `json:"hour"`
	Queries int     `json:"queries"`
	Blocked int     `json:"blocked"`
	Elapsed float64 `json:"elapsed"`
	Timed   int     `json:"timed"`
}

// Domain 是热门域名里的一项。
type Domain struct {
	Domain string `json:"domain"`
	Count  int    `json:"count"`
}

// Summary 是最近 24 小时的统计(不含 enabled / connected,那两项由接口层补)。
type Summary struct {
	Queries    int      `json:"queries"`
	Blocked    int      `json:"blocked"`
	AverageMs  *float64 `json:"averageMs"`
	Hourly     []Hour   `json:"hourly"`
	TopDomains []Domain `json:"topDomains"`
}

// TopN 是热门域名的条数。
const TopN = 10

// Summarize 汇总最近 24 个小时(含 now 所在的这一个),hourly 恒为 24 项、旧的在前。
func (l *Log) Summarize(now time.Time) Summary {
	end := hourOf(now)
	l.mu.Lock()
	defer l.mu.Unlock()
	out := Summary{Hourly: make([]Hour, 0, 24), TopDomains: []Domain{}}
	var elapsed float64
	var timed int
	counts := map[string]int{}
	for i := 23; i >= 0; i-- {
		h := end - int64(i)*int64(time.Hour/time.Millisecond)
		row := Hour{Hour: h}
		if b := l.hours[h]; b != nil {
			row.Queries, row.Blocked, row.Elapsed, row.Timed = b.Queries, b.Blocked, b.Elapsed, b.Timed
			for d, n := range b.Domains {
				counts[d] += n
			}
		}
		out.Queries += row.Queries
		out.Blocked += row.Blocked
		elapsed += row.Elapsed
		timed += row.Timed
		out.Hourly = append(out.Hourly, row)
	}
	if timed > 0 {
		avg := elapsed / float64(timed)
		out.AverageMs = &avg
	}
	for d, n := range counts {
		out.TopDomains = append(out.TopDomains, Domain{d, n})
	}
	sort.Slice(out.TopDomains, func(i, j int) bool {
		a, b := out.TopDomains[i], out.TopDomains[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Domain < b.Domain
	})
	if len(out.TopDomains) > TopN {
		out.TopDomains = out.TopDomains[:TopN]
	}
	return out
}

// Snapshot 是要落盘的内容(重启后记录和统计还在;还在等应答的查询不保存)。
type Snapshot struct {
	Version int                `json:"version"`
	NextID  int64              `json:"nextId"`
	Records []*Record          `json:"records"`
	Hours   map[int64]*hourAgg `json:"hours"`
}

// Snapshot 取一份拷贝,并把「有改动」标记清掉。
func (l *Log) Snapshot() (Snapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty {
		return Snapshot{}, false
	}
	l.dirty = false
	s := Snapshot{Version: 1, NextID: l.nextID, Records: make([]*Record, len(l.recs)), Hours: make(map[int64]*hourAgg, len(l.hours))}
	for i, r := range l.recs {
		c := *r
		s.Records[i] = &c
	}
	for k, b := range l.hours {
		c := *b
		c.Domains = make(map[string]int, len(b.Domains))
		for d, n := range b.Domains {
			c.Domains[d] = n
		}
		s.Hours[k] = &c
	}
	return s, true
}

// Restore 载入落盘的内容(只在启动时、开始 Feed 之前调用)。版本不认识就当没有。
func (l *Log) Restore(s Snapshot) {
	if s.Version != 1 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if s.NextID > l.nextID {
		l.nextID = s.NextID
	}
	for _, r := range s.Records {
		if r != nil && r.Domain != "" {
			l.recs = append(l.recs, r)
		}
	}
	if len(l.recs) > MaxRecords {
		l.recs = l.recs[len(l.recs)-MaxRecords:]
	}
	for k, b := range s.Hours {
		if b != nil {
			l.hours[k] = b
		}
	}
}
