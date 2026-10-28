// Package traffic 记「每日流量」:从内核的连接快照里按天、按小时统计,按终端 / 节点 / 访问目标三个维度拆开,
// 供 /api/openbox/traffic/{month,day,drill,usage} 读。
//
// 自己的设计,依据前端 OpenboxTrafficMonth / OpenboxTrafficDay / OpenboxTrafficDrill / OpenboxTrafficUsage 的类型和字段注释,
// 以及 sing-box 官方 Clash API 的 /connections(chains[0] 是实际出口、末项是路由选中的组;uploadTotal / downloadTotal 是内核累计总量),
// 没有参照上游。
//
// 存储(D6:JSON 文件 + 原子写):数据目录下 traffic/,每天两个文件加一份索引——
//   - YYYY-MM-DD.json:这一天的 24 个小时合计,和一张「终端 × 节点 × 访问目标」的三维表(键用 \x1f 连接)。三个维度的列表、
//     钻取都由这张表聚合出来,所以「不统计直连」时能把走直连出站的部分整块扣掉。
//   - YYYY-MM-DD.hours.json:每个小时各自的三维表,只留最近 HourKeepDays 天(看某个小时的明细用)。
//   - index.json:每天的合计和文件大小(月视图、占用估算只读它),以及配置里 direct 类型出站的名字。
//
// 总量取自内核的累计总量(含已经关掉的短连接),三维表来自各连接的增量,所以「总量 − 各节点之和」就是没采样到的短连接。
// 超过 CollapseAfterDays 天的日子只保留访问量最大的 keepHosts 个访问目标,其余合并成「其他」;超过保留月数的整天删除。
package traffic

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	// HourKeepDays 是小时明细保留的天数。
	HourKeepDays = 7
	// CollapseAfterDays 是超过这么多天的日子合并冷门访问目标。
	CollapseAfterDays = 30
	// OtherLabel 是被合并的访问目标、以及放不下的新条目的名字。
	OtherLabel = "其他"

	keepHosts       = 200
	maxDayEntries   = 30000 // 一天的三维表最多这么多条
	maxHourEntries  = 5000  // 一个小时的三维表最多这么多条
	sep             = "\x1f"
	dayLayout       = "2006-01-02"
	indexName       = "index.json"
	defaultKeepMths = 12
)

// Counter 是一组计数。Up = 发往外网的字节(出口),Down = 收到的字节(入口)。
type Counter struct {
	Up    int64 `json:"u"`
	Down  int64 `json:"d"`
	Conns int64 `json:"c"`
}

func (c *Counter) add(o Counter) { c.Up += o.Up; c.Down += o.Down; c.Conns += o.Conns }
func (c Counter) sub(o Counter) Counter {
	return Counter{Up: c.Up - o.Up, Down: c.Down - o.Down, Conns: c.Conns - o.Conns}
}
func (c Counter) bytes() int64 { return c.Up + c.Down }

// hourTotal 是一个小时的合计;Direct 是其中走直连出站的部分。
type hourTotal struct {
	Total  Counter `json:"t"`
	Direct Counter `json:"x"`
}

type table map[string]*Counter

// dayFile 是 YYYY-MM-DD.json 的内容。
type dayFile struct {
	Day       string        `json:"day"`
	Collapsed bool          `json:"collapsed,omitempty"`
	Hours     [24]hourTotal `json:"hours"`
	Table     table         `json:"table"`
}

// DayMeta 是索引里一天的摘要。
type DayMeta struct {
	Bytes     int64   `json:"bytes"`
	HourBytes int64   `json:"hourBytes,omitempty"`
	Rows      int     `json:"rows"`
	Total     Counter `json:"total"`
	Direct    Counter `json:"direct"`
}

type indexFile struct {
	Days       map[string]*DayMeta `json:"days"`
	DirectTags []string            `json:"directTags,omitempty"`
}

// Item 是一次采样里某个(终端,节点,访问目标)的增量。
type Item struct {
	Client, Node, Host string
	Up, Down, Conns    int64
	Direct             bool // 走的是 direct 类型出站
}

// Delta 是一次采样的全部增量:总量取自内核累计总量,Items 来自各连接。
type Delta struct {
	Up, Down, Conns int64
	Items           []Item
	DirectTags      []string
}

// InputError 表示请求参数不合法(接口回 400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Store 是每日流量的存储。所有方法并发安全。
type Store struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	idx      indexFile
	cur      *dayFile
	curHours map[int]table
	dirtyDay bool
	dirtyHrs bool
}

// Open 打开(必要时创建)目录 dir 下的流量库。now 为空用 time.Now(按本机时区分天)。
func Open(dir string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建流量目录失败: %w", err)
	}
	s := &Store{dir: dir, now: now, idx: indexFile{Days: map[string]*DayMeta{}}}
	if b, err := os.ReadFile(filepath.Join(dir, indexName)); err == nil {
		if json.Unmarshal(b, &s.idx) != nil || s.idx.Days == nil {
			s.idx = indexFile{Days: map[string]*DayMeta{}} // 索引坏了不影响记新数据;旧日子读不到月视图,但文件还在
		}
	}
	return s, nil
}

// ---- 写 ----

// Record 把一次采样的增量记进 now 所在的那天、那个小时。
func (s *Store) Record(now time.Time, d Delta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := now.Format(dayLayout)
	if err := s.useDay(day); err != nil {
		return err
	}
	h := now.Hour()
	ht := &s.cur.Hours[h]
	ht.Total.add(Counter{Up: d.Up, Down: d.Down, Conns: d.Conns})
	if len(d.DirectTags) > 0 {
		s.idx.DirectTags = append([]string(nil), d.DirectTags...)
	}
	hours := s.curHours[h]
	if hours == nil {
		hours = table{}
		s.curHours[h] = hours
	}
	for _, it := range d.Items {
		c := Counter{Up: it.Up, Down: it.Down, Conns: it.Conns}
		if it.Direct {
			ht.Direct.add(c)
		}
		addEntry(s.cur.Table, it.Client, it.Node, it.Host, c, maxDayEntries)
		addEntry(hours, it.Client, it.Node, it.Host, c, maxHourEntries)
	}
	s.dirtyDay, s.dirtyHrs = true, true
	return nil
}

func addEntry(t table, client, node, host string, c Counter, limit int) {
	key := client + sep + node + sep + host
	if _, ok := t[key]; !ok && len(t) >= limit {
		key = client + sep + node + sep + OtherLabel
	}
	e := t[key]
	if e == nil {
		e = &Counter{}
		t[key] = e
	}
	e.add(c)
}

// useDay 保证内存里的「当天」是 day:换天时先把旧的落盘,再读新的那天(已有文件就接着记)。
func (s *Store) useDay(day string) error {
	if s.cur != nil && s.cur.Day == day {
		return nil
	}
	if s.cur != nil {
		if err := s.flushLocked(); err != nil {
			return err
		}
	}
	d, hrs, err := s.loadDayLocked(day)
	if err != nil {
		return err
	}
	if d == nil {
		d = &dayFile{Day: day, Table: table{}}
	}
	if hrs == nil {
		hrs = map[int]table{}
	}
	s.cur, s.curHours = d, hrs
	return nil
}

// Flush 把有改动的当天写盘(原子写)。
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	if s.cur == nil || !s.dirtyDay && !s.dirtyHrs {
		return nil
	}
	meta := s.idx.Days[s.cur.Day]
	if meta == nil {
		meta = &DayMeta{}
		s.idx.Days[s.cur.Day] = meta
	}
	b, err := json.Marshal(s.cur)
	if err != nil {
		return err
	}
	if err := store.WriteFileAtomic(s.dayPath(s.cur.Day), b, 0o600); err != nil {
		return fmt.Errorf("写流量文件失败: %w", err)
	}
	meta.Bytes, meta.Rows = int64(len(b)), len(s.cur.Table)
	meta.Total, meta.Direct = sumHours(s.cur, nil)
	if len(s.curHours) > 0 {
		hb, err := json.Marshal(s.curHours)
		if err != nil {
			return err
		}
		if err := store.WriteFileAtomic(s.hoursPath(s.cur.Day), hb, 0o600); err != nil {
			return fmt.Errorf("写流量小时明细失败: %w", err)
		}
		meta.HourBytes = int64(len(hb))
		for _, t := range s.curHours {
			meta.Rows += len(t)
		}
	}
	s.dirtyDay, s.dirtyHrs = false, false
	return s.saveIndexLocked()
}

func (s *Store) saveIndexLocked() error {
	b, err := json.Marshal(s.idx)
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(filepath.Join(s.dir, indexName), b, 0o600)
}

// Maintain 做每天一次的清理:删掉超过 keepMonths 个月的日子、超过 HourKeepDays 天的小时明细,
// 并把超过 CollapseAfterDays 天的日子合并冷门访问目标。keepMonths <= 0 按 12 个月。
func (s *Store) Maintain(now time.Time, keepMonths int) error {
	if keepMonths <= 0 {
		keepMonths = defaultKeepMths
	}
	if keepMonths > 36 {
		keepMonths = 36
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushLocked(); err != nil {
		return err
	}
	today := now.Format(dayLayout)
	cutDel := now.AddDate(0, -keepMonths, 0).Format(dayLayout)
	cutHours := now.AddDate(0, 0, -HourKeepDays).Format(dayLayout)
	cutCollapse := now.AddDate(0, 0, -CollapseAfterDays).Format(dayLayout)
	var firstErr error
	note := func(err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	for day, meta := range s.idx.Days {
		switch {
		case day < cutDel:
			note(os.Remove(s.dayPath(day)))
			note(os.Remove(s.hoursPath(day)))
			delete(s.idx.Days, day)
			continue
		case day < cutHours && meta.HourBytes > 0:
			note(os.Remove(s.hoursPath(day)))
			meta.HourBytes = 0
		}
		if day < cutCollapse && day != today {
			note(s.collapseLocked(day, meta))
		}
	}
	if s.cur != nil && s.cur.Day < cutHours {
		s.curHours = map[int]table{}
	}
	note(s.saveIndexLocked())
	return firstErr
}

// collapseLocked 把一天里访问量排在 keepHosts 名之后的访问目标合并成「其他」。已合并过的不再动。
func (s *Store) collapseLocked(day string, meta *DayMeta) error {
	d, _, err := s.loadDayLocked(day)
	if err != nil || d == nil || d.Collapsed {
		return err
	}
	byHost := map[string]int64{}
	for k, c := range d.Table {
		byHost[splitKey(k)[2]] += c.bytes()
	}
	d.Collapsed = true
	if len(byHost) > keepHosts {
		hosts := make([]string, 0, len(byHost))
		for h := range byHost {
			hosts = append(hosts, h)
		}
		sort.Slice(hosts, func(i, j int) bool {
			if byHost[hosts[i]] != byHost[hosts[j]] {
				return byHost[hosts[i]] > byHost[hosts[j]]
			}
			return hosts[i] < hosts[j]
		})
		keep := map[string]bool{OtherLabel: true}
		for _, h := range hosts[:keepHosts] {
			keep[h] = true
		}
		merged := table{}
		for k, c := range d.Table {
			p := splitKey(k)
			if !keep[p[2]] {
				p[2] = OtherLabel
			}
			addEntry(merged, p[0], p[1], p[2], *c, int(^uint(0)>>1))
		}
		d.Table = merged
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := store.WriteFileAtomic(s.dayPath(day), b, 0o600); err != nil {
		return err
	}
	meta.Bytes, meta.Rows = int64(len(b)), len(d.Table)
	return nil
}

// ---- 文件 ----

func (s *Store) dayPath(day string) string   { return filepath.Join(s.dir, day+".json") }
func (s *Store) hoursPath(day string) string { return filepath.Join(s.dir, day+".hours.json") }

func validDay(day string) bool {
	t, err := time.Parse(dayLayout, day)
	return err == nil && t.Format(dayLayout) == day
}

// loadDayLocked 读一天的两个文件;文件不存在返回 nil。当天直接取内存里的(含还没落盘的部分)。
func (s *Store) loadDayLocked(day string) (*dayFile, map[int]table, error) {
	if s.cur != nil && s.cur.Day == day {
		return s.cur, s.curHours, nil
	}
	if !validDay(day) {
		return nil, nil, &InputError{"日期格式不对,要写成 YYYY-MM-DD"}
	}
	b, err := os.ReadFile(s.dayPath(day))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var d dayFile
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, nil, fmt.Errorf("流量文件 %s 损坏: %w", day, err)
	}
	if d.Table == nil {
		d.Table = table{}
	}
	var hrs map[int]table
	if hb, err := os.ReadFile(s.hoursPath(day)); err == nil {
		if json.Unmarshal(hb, &hrs) != nil {
			hrs = nil // 小时明细坏了只当没有
		}
	}
	return &d, hrs, nil
}

func splitKey(k string) [3]string {
	p := strings.SplitN(k, sep, 3)
	var out [3]string
	copy(out[:], p)
	return out
}

// sumHours 合计一天(hour 为空)或某个小时,返回总量和其中走直连的部分。
func sumHours(d *dayFile, hour *int) (total, direct Counter) {
	for h := range d.Hours {
		if hour != nil && *hour != h {
			continue
		}
		total.add(d.Hours[h].Total)
		direct.add(d.Hours[h].Direct)
	}
	return
}
