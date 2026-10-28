package traffic

import (
	"regexp"
	"sort"
)

// Row 对应前端 OpenboxTrafficRow(Name、self 由接口层补)。
type Row struct {
	Key   string `json:"key"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Conns int64  `json:"conns"`
	Name  string `json:"name,omitempty"`
}

// Sum 是 up / down 两个数。
type Sum struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// Total 是 up / down / conns 三个数。
type Total struct {
	Up    int64 `json:"up"`
	Down  int64 `json:"down"`
	Conns int64 `json:"conns"`
}

// DirectInfo 对应前端的 direct 字段:Excluded 为真表示走直连出站的流量已从数字里扣掉。
type DirectInfo struct {
	Excluded bool   `json:"excluded"`
	Tag      string `json:"tag"`
}

// DaySummary 是月视图里的一天。
type DaySummary struct {
	Day   string `json:"day"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Conns int64  `json:"conns"`
}

// MonthResult 对应前端 OpenboxTrafficMonth。
type MonthResult struct {
	Month   string       `json:"month"`
	Today   string       `json:"today"`
	Days    []DaySummary `json:"days"`
	Total   Total        `json:"total"`
	Avg     Sum          `json:"avg"`
	AvgDays int          `json:"avgDays"`
	Direct  DirectInfo   `json:"direct"`
}

// HourPoint 对应前端 OpenboxTrafficHour。
type HourPoint struct {
	Hour  int   `json:"hour"`
	Up    int64 `json:"up"`
	Down  int64 `json:"down"`
	Conns int64 `json:"conns"`
}

// DayResult 对应前端 OpenboxTrafficDay。
type DayResult struct {
	Day                string      `json:"day"`
	Today              string      `json:"today"`
	Total              Total       `json:"total"`
	Nodes              []Row       `json:"nodes"`
	Hosts              []Row       `json:"hosts"`
	Clients            []Row       `json:"clients"`
	HostsCount         int         `json:"hostsCount"`
	ClientsCount       int         `json:"clientsCount"`
	Other              Sum         `json:"other"`
	Hours              []HourPoint `json:"hours"`
	NowHour            int         `json:"nowHour"`
	Hour               *int        `json:"hour"`
	HourDetailKeepDays int         `json:"hourDetailKeepDays"`
	Direct             DirectInfo  `json:"direct"`
}

// DrillResult 对应前端 OpenboxTrafficDrill。
type DrillResult struct {
	Day   string `json:"day"`
	Hour  *int   `json:"hour"`
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	By    string `json:"by"`
	Count int    `json:"count"`
	Sum   Sum    `json:"sum"`
	Rows  []Row  `json:"rows"`
}

// UsageResult 对应前端 OpenboxTrafficUsage。
type UsageResult struct {
	Rows              int    `json:"rows"`
	Days              int    `json:"days"`
	Bytes             int64  `json:"bytes"`
	PerDay            int64  `json:"perDay"`
	HourPerDay        int64  `json:"hourPerDay"`
	HourKeepDays      int    `json:"hourKeepDays"`
	CollapseAfterDays int    `json:"collapseAfterDays"`
	OldPerDay         int64  `json:"oldPerDay"`
	OldestDay         string `json:"oldestDay"`
	NewestDay         string `json:"newestDay"`
}

var monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

const (
	dimClient = "client"
	dimNode   = "node"
	dimHost   = "host"
)

func (s *Store) directSet() map[string]bool {
	m := map[string]bool{}
	for _, t := range s.idx.DirectTags {
		m[t] = true
	}
	return m
}

func (s *Store) directTag() string {
	if len(s.idx.DirectTags) > 0 {
		return s.idx.DirectTags[0]
	}
	return "direct"
}

// Month 返回一个月里每天的合计。month 为空是本月;没有数据的日子不列。
func (s *Store) Month(month string, countDirect bool) (MonthResult, error) {
	now := s.now()
	if month == "" {
		month = now.Format("2006-01")
	}
	if !monthRe.MatchString(month) {
		return MonthResult{}, &InputError{"month 要写成 YYYY-MM"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := MonthResult{Month: month, Today: now.Format(dayLayout), Days: []DaySummary{}, Direct: DirectInfo{Excluded: !countDirect, Tag: s.directTag()}}
	metas := map[string]DayMeta{}
	for day, m := range s.idx.Days {
		if len(day) == 10 && day[:7] == month {
			metas[day] = *m
		}
	}
	if s.cur != nil && len(s.cur.Day) == 10 && s.cur.Day[:7] == month { // 当天以内存为准(还没落盘的部分也算)
		t, d := sumHours(s.cur, nil)
		metas[s.cur.Day] = DayMeta{Total: t, Direct: d}
	}
	for day, m := range metas {
		t := m.Total
		if !countDirect {
			t = t.sub(m.Direct)
		}
		if t.Up == 0 && t.Down == 0 && t.Conns == 0 {
			continue
		}
		res.Days = append(res.Days, DaySummary{Day: day, Up: t.Up, Down: t.Down, Conns: t.Conns})
	}
	sort.Slice(res.Days, func(i, j int) bool { return res.Days[i].Day < res.Days[j].Day })
	for _, d := range res.Days {
		res.Total.Up += d.Up
		res.Total.Down += d.Down
		res.Total.Conns += d.Conns
	}
	res.AvgDays = len(res.Days)
	if res.AvgDays > 0 {
		res.Avg = Sum{Up: res.Total.Up / int64(res.AvgDays), Down: res.Total.Down / int64(res.AvgDays)}
	}
	return res, nil
}

// view 是读一天(或其中某个小时)时用的数据视图。
type view struct {
	total Counter
	tbl   table
	hours [24]hourTotal
}

func (s *Store) viewLocked(day string, hour *int, countDirect bool) (*view, error) {
	if hour != nil && (*hour < 0 || *hour > 23) {
		return nil, &InputError{"hour 要在 0 到 23 之间"}
	}
	d, hrs, err := s.loadDayLocked(day)
	if err != nil {
		return nil, err
	}
	v := &view{tbl: table{}}
	if d == nil {
		return v, nil
	}
	v.hours = d.Hours
	t, dir := sumHours(d, hour)
	v.total = t
	if !countDirect {
		v.total = t.sub(dir)
	}
	if hour != nil {
		v.tbl = hrs[*hour]
	} else {
		v.tbl = d.Table
	}
	return v, nil
}

// each 遍历表里的条目,「不统计直连」时跳过走直连出站的。
func (s *Store) each(v *view, countDirect bool, fn func(client, node, host string, c *Counter)) {
	direct := s.directSet()
	for k, c := range v.tbl {
		p := splitKey(k)
		if !countDirect && direct[p[1]] {
			continue
		}
		fn(p[0], p[1], p[2], c)
	}
}

func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].Up+rows[i].Down, rows[j].Up+rows[j].Down
		if a != b {
			return a > b
		}
		return rows[i].Key < rows[j].Key
	})
}

func rowsOf(m map[string]*Counter) []Row {
	rows := make([]Row, 0, len(m))
	for k, c := range m {
		rows = append(rows, Row{Key: k, Up: c.Up, Down: c.Down, Conns: c.Conns})
	}
	sortRows(rows)
	return rows
}

func clip(rows []Row, limit int) []Row {
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func (s *Store) checkLimit(limit int) (int, error) {
	if limit == 0 {
		return 500, nil
	}
	if limit < 0 || limit > 5000 {
		return 0, &InputError{"limit 要在 1 到 5000 之间"}
	}
	return limit, nil
}

// DayView 返回一天(hour 不为空时只看那个小时)的节点、访问目标、终端三份榜单和 24 小时曲线。
func (s *Store) DayView(day string, limit int, hour *int, countDirect bool) (DayResult, error) {
	limit, err := s.checkLimit(limit)
	if err != nil {
		return DayResult{}, err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.viewLocked(day, hour, countDirect)
	if err != nil {
		return DayResult{}, err
	}
	nodes, hosts, clients := map[string]*Counter{}, map[string]*Counter{}, map[string]*Counter{}
	bump := func(m map[string]*Counter, k string, c *Counter) {
		e := m[k]
		if e == nil {
			e = &Counter{}
			m[k] = e
		}
		e.add(*c)
	}
	var nodeSum Counter
	s.each(v, countDirect, func(client, node, host string, c *Counter) {
		bump(nodes, node, c)
		bump(hosts, host, c)
		bump(clients, client, c)
		nodeSum.add(*c)
	})
	res := DayResult{
		Day: day, Today: now.Format(dayLayout),
		Total: Total{Up: v.total.Up, Down: v.total.Down, Conns: v.total.Conns},
		Nodes: clip(rowsOf(nodes), limit), Hosts: clip(rowsOf(hosts), limit), Clients: clip(rowsOf(clients), limit),
		HostsCount: len(hosts), ClientsCount: len(clients),
		Other:   Sum{Up: max64(0, v.total.Up-nodeSum.Up), Down: max64(0, v.total.Down-nodeSum.Down)},
		Hours:   make([]HourPoint, 24),
		NowHour: now.Hour(), Hour: hour, HourDetailKeepDays: HourKeepDays,
		Direct: DirectInfo{Excluded: !countDirect, Tag: s.directTag()},
	}
	for h := 0; h < 24; h++ {
		c := v.hours[h].Total
		if !countDirect {
			c = c.sub(v.hours[h].Direct)
		}
		res.Hours[h] = HourPoint{Hour: h, Up: c.Up, Down: c.Down, Conns: c.Conns}
	}
	return res, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Drill 把一条记录(kind 维度里的 key)按另一个维度 by 拆开。kind 和 by 必须不同。
func (s *Store) Drill(day string, hour *int, kind, key, by string, limit int, countDirect bool) (DrillResult, error) {
	valid := func(d string) bool { return d == dimClient || d == dimNode || d == dimHost }
	if !valid(kind) || !valid(by) || kind == by {
		return DrillResult{}, &InputError{"kind 和 by 要在 client / node / host 里选,并且不能相同"}
	}
	if key == "" {
		return DrillResult{}, &InputError{"key 不能为空"}
	}
	limit, err := s.checkLimit(limit)
	if err != nil {
		return DrillResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.viewLocked(day, hour, countDirect)
	if err != nil {
		return DrillResult{}, err
	}
	pick := func(client, node, host, dim string) string {
		switch dim {
		case dimClient:
			return client
		case dimNode:
			return node
		}
		return host
	}
	rows := map[string]*Counter{}
	var sum Counter
	s.each(v, countDirect, func(client, node, host string, c *Counter) {
		if pick(client, node, host, kind) != key {
			return
		}
		k := pick(client, node, host, by)
		e := rows[k]
		if e == nil {
			e = &Counter{}
			rows[k] = e
		}
		e.add(*c)
		sum.add(*c)
	})
	all := rowsOf(rows)
	return DrillResult{Day: day, Hour: hour, Kind: kind, Key: key, By: by, Count: len(all), Sum: Sum{Up: sum.Up, Down: sum.Down}, Rows: clip(all, limit)}, nil
}

// Usage 估算库里存了多少、每天大概涨多少(只读索引,不读各天的文件)。
func (s *Store) Usage() UsageResult {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil && (s.dirtyDay || s.dirtyHrs) {
		_ = s.flushLocked() // 当天还没落盘的部分也算进去
	}
	res := UsageResult{HourKeepDays: HourKeepDays, CollapseAfterDays: CollapseAfterDays}
	cut := now.AddDate(0, 0, -CollapseAfterDays).Format(dayLayout)
	days := make([]string, 0, len(s.idx.Days))
	for d := range s.idx.Days {
		days = append(days, d)
	}
	sort.Strings(days)
	var recent, old, hours []int64
	for _, d := range days {
		m := s.idx.Days[d]
		res.Rows += m.Rows
		res.Bytes += m.Bytes + m.HourBytes
		if m.HourBytes > 0 {
			hours = append(hours, m.HourBytes)
		}
		if d < cut {
			old = append(old, m.Bytes)
		} else {
			recent = append(recent, m.Bytes)
		}
	}
	res.Days = len(days)
	if len(days) > 0 {
		res.OldestDay, res.NewestDay = days[0], days[len(days)-1]
	}
	res.PerDay, res.HourPerDay, res.OldPerDay = avgTail(recent, 14), avgTail(hours, 14), avgTail(old, 14)
	return res
}

// avgTail 取最后 n 个数的平均(日子按时间从早到晚排,所以是最近 n 天)。
func avgTail(v []int64, n int) int64 {
	if len(v) == 0 {
		return 0
	}
	if len(v) > n {
		v = v[len(v)-n:]
	}
	var s int64
	for _, x := range v {
		s += x
	}
	return s / int64(len(v))
}
