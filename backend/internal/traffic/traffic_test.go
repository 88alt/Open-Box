package traffic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

var loc = time.FixedZone("CST", 8*3600)

func at(day string, h, m int) time.Time {
	t, _ := time.ParseInLocation(dayLayout, day, loc)
	return t.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func newStore(t *testing.T, now time.Time) (*Store, *clock, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "traffic")
	ck := &clock{t: now}
	s, err := Open(dir, ck.Now)
	if err != nil {
		t.Fatal(err)
	}
	return s, ck, dir
}

func item(client, node, host string, up, down, conns int64, direct bool) Item {
	return Item{Client: client, Node: node, Host: host, Up: up, Down: down, Conns: conns, Direct: direct}
}

// 一天的样例:10 点有 A 经节点 x 访问 a.com 和 b.com,B 走直连访问 c.com;11 点 A 再访问 a.com。
func seed(t *testing.T, s *Store) {
	t.Helper()
	tags := []string{"直连"}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Record(at("2026-10-28", 10, 5), Delta{Up: 130, Down: 1100, Conns: 4, DirectTags: tags, Items: []Item{
		item("192.168.1.5", "x", "a.com", 100, 1000, 2, false),
		item("192.168.1.5", "x", "b.com", 10, 50, 1, false),
		item("192.168.1.6", "直连", "c.com", 5, 20, 1, true),
	}}))
	must(s.Record(at("2026-10-28", 11, 0), Delta{Up: 20, Down: 200, Conns: 1, DirectTags: tags, Items: []Item{
		item("192.168.1.5", "x", "a.com", 20, 200, 1, false),
	}}))
}

func TestDayView(t *testing.T) {
	s, _, _ := newStore(t, at("2026-10-28", 12, 0))
	seed(t, s)
	r, err := s.DayView("2026-10-28", 0, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != (Total{Up: 150, Down: 1300, Conns: 5}) || r.Today != "2026-10-28" || r.NowHour != 12 || r.Hour != nil || r.HourDetailKeepDays != HourKeepDays {
		t.Errorf("总量等: %+v", r)
	}
	if want := []Row{{Key: "x", Up: 130, Down: 1250, Conns: 4}, {Key: "直连", Up: 5, Down: 20, Conns: 1}}; !reflect.DeepEqual(r.Nodes, want) {
		t.Errorf("节点: %+v", r.Nodes)
	}
	if len(r.Hosts) != 3 || r.Hosts[0].Key != "a.com" || r.Hosts[0].Down != 1200 || r.HostsCount != 3 {
		t.Errorf("访问目标: %+v", r.Hosts)
	}
	if len(r.Clients) != 2 || r.Clients[0].Key != "192.168.1.5" || r.ClientsCount != 2 {
		t.Errorf("终端: %+v", r.Clients)
	}
	if r.Other != (Sum{Up: 15, Down: 30}) { // 总量减各节点之和(150-135、1300-1270)
		t.Errorf("other: %+v", r.Other)
	}
	if len(r.Hours) != 24 || r.Hours[10] != (HourPoint{Hour: 10, Up: 130, Down: 1100, Conns: 4}) || r.Hours[11].Down != 200 || r.Hours[3].Up != 0 {
		t.Errorf("小时曲线: %+v", r.Hours[9:12])
	}
	if r.Direct != (DirectInfo{Excluded: false, Tag: "直连"}) {
		t.Errorf("direct: %+v", r.Direct)
	}

	// limit 只截榜单,不影响 *Count 和总量
	r, _ = s.DayView("2026-10-28", 1, nil, true)
	if len(r.Hosts) != 1 || r.HostsCount != 3 || r.Total.Up != 150 {
		t.Errorf("limit: %+v", r)
	}

	// 不统计直连:总量、小时曲线、榜单都扣掉走直连出站的部分
	r, _ = s.DayView("2026-10-28", 0, nil, false)
	if r.Total != (Total{Up: 145, Down: 1280, Conns: 4}) || len(r.Nodes) != 1 || r.Nodes[0].Key != "x" || r.HostsCount != 2 || r.ClientsCount != 1 || !r.Direct.Excluded {
		t.Errorf("扣直连: %+v", r)
	}
	if r.Hours[10].Up != 125 || r.Hours[10].Down != 1080 {
		t.Errorf("扣直连的小时: %+v", r.Hours[10])
	}

	// 只看 10 点
	h := 10
	r, _ = s.DayView("2026-10-28", 0, &h, true)
	if r.Total != (Total{Up: 130, Down: 1100, Conns: 4}) || r.Hour == nil || *r.Hour != 10 || r.Hosts[0].Down != 1000 || len(r.Hours) != 24 {
		t.Errorf("小时视图: %+v", r)
	}
	// 没有数据的日子和小时:空榜单(切片不是 null)、总量 0
	h = 3
	r, _ = s.DayView("2026-10-28", 0, &h, true)
	if r.Total != (Total{}) || r.Nodes == nil || len(r.Nodes) != 0 {
		t.Errorf("空小时: %+v", r)
	}
	r, err = s.DayView("2026-01-01", 0, nil, true)
	if err != nil || r.Total != (Total{}) || len(r.Hours) != 24 {
		t.Errorf("没有数据的日子: %+v %v", r, err)
	}
}

func TestInputErrors(t *testing.T) {
	s, _, _ := newStore(t, at("2026-10-28", 12, 0))
	bad, over := 24, 5001
	check := func(name string, err error) {
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%s: 想要 *InputError,得到 %v", name, err)
		}
	}
	_, err := s.DayView("2026-13-01", 0, nil, true)
	check("日期", err)
	_, err = s.DayView("../x", 0, nil, true)
	check("路径穿越", err)
	_, err = s.DayView("2026-10-28", 0, &bad, true)
	check("小时", err)
	_, err = s.DayView("2026-10-28", over, nil, true)
	check("limit 太大", err)
	_, err = s.DayView("2026-10-28", -1, nil, true)
	check("limit 为负", err)
	_, err = s.Month("2026-13", true)
	check("月份", err)
	_, err = s.Drill("2026-10-28", nil, "client", "k", "client", 0, true)
	check("kind=by", err)
	_, err = s.Drill("2026-10-28", nil, "foo", "k", "host", 0, true)
	check("kind 不认识", err)
	_, err = s.Drill("2026-10-28", nil, "client", "", "host", 0, true)
	check("key 为空", err)
}

func TestDrill(t *testing.T) {
	s, _, _ := newStore(t, at("2026-10-28", 12, 0))
	seed(t, s)
	d, err := s.Drill("2026-10-28", nil, "client", "192.168.1.5", "host", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Count != 2 || d.Sum != (Sum{Up: 130, Down: 1250}) || d.Rows[0] != (Row{Key: "a.com", Up: 120, Down: 1200, Conns: 3}) || d.Hour != nil {
		t.Errorf("终端按访问目标: %+v", d)
	}
	d, _ = s.Drill("2026-10-28", nil, "host", "a.com", "client", 0, true)
	if d.Count != 1 || d.Rows[0].Key != "192.168.1.5" {
		t.Errorf("访问目标按终端: %+v", d)
	}
	d, _ = s.Drill("2026-10-28", nil, "node", "x", "host", 1, true)
	if d.Count != 2 || len(d.Rows) != 1 || d.Sum.Down != 1250 { // sum 不受 limit 影响
		t.Errorf("limit: %+v", d)
	}
	// 终端 .6 全是直连:不统计直连时没有构成
	d, _ = s.Drill("2026-10-28", nil, "client", "192.168.1.6", "host", 0, false)
	if d.Count != 0 || len(d.Rows) != 0 || d.Rows == nil {
		t.Errorf("扣直连: %+v", d)
	}
	h := 11
	d, _ = s.Drill("2026-10-28", &h, "client", "192.168.1.5", "node", 0, true)
	if d.Sum.Down != 200 || d.Hour == nil || *d.Hour != 11 {
		t.Errorf("小时: %+v", d)
	}
}

func TestMonthAndPersistence(t *testing.T) {
	s, ck, dir := newStore(t, at("2026-10-28", 12, 0))
	seed(t, s)
	// 另一天(先落盘,再换到新一天)
	if err := s.Record(at("2026-10-27", 9, 0), Delta{Up: 1, Down: 2, Conns: 1, Items: []Item{item("c", "n", "h", 1, 2, 1, false)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(at("2026-10-28", 13, 0), Delta{Up: 10, Down: 10, Conns: 1, Items: []Item{item("192.168.1.5", "x", "a.com", 10, 10, 1, false)}}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Month("", true) // 空 = 本月
	if err != nil {
		t.Fatal(err)
	}
	if m.Month != "2026-10" || m.Today != "2026-10-28" || len(m.Days) != 2 || m.Days[0].Day != "2026-10-27" || m.Days[1].Up != 160 || m.AvgDays != 2 ||
		m.Total != (Total{Up: 161, Down: 1312, Conns: 7}) || m.Avg != (Sum{Up: 80, Down: 656}) {
		t.Errorf("月视图: %+v", m)
	}
	m, _ = s.Month("2026-10", false) // 扣直连:28 日少 5 / 20 / 1
	if m.Days[1].Up != 155 || m.Direct.Excluded != true {
		t.Errorf("月视图扣直连: %+v", m)
	}
	if m, _ = s.Month("2026-09", true); len(m.Days) != 0 || m.AvgDays != 0 || m.Avg != (Sum{}) || m.Days == nil {
		t.Errorf("没有数据的月份: %+v", m)
	}

	// 落盘后重开,数据一样;文件权限 0600
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, ck.Now)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.DayView("2026-10-28", 0, nil, true)
	b, _ := s2.DayView("2026-10-28", 0, nil, true)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("重开后不一致:\n%+v\n%+v", a, b)
	}
	h := 10
	if hv, _ := s2.DayView("2026-10-28", 0, &h, true); hv.Total.Up != 130 || len(hv.Hosts) != 3 {
		t.Errorf("小时明细没落盘: %+v", hv)
	}
	if m2, _ := s2.Month("2026-10", true); len(m2.Days) != 2 {
		t.Errorf("索引没落盘: %+v", m2)
	}
	if fi, err := os.Stat(filepath.Join(dir, "2026-10-28.json")); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) { // Windows 没有 Unix 权限位
		t.Errorf("权限: %v %v", fi, err)
	}
	// 重开后接着记当天(不覆盖已有的)
	if err := s2.Record(at("2026-10-28", 14, 0), Delta{Up: 1, Down: 1, Items: []Item{item("192.168.1.5", "x", "a.com", 1, 1, 0, false)}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.DayView("2026-10-28", 0, nil, true); r.Total.Up != 151+10 {
		t.Errorf("接着记: %+v", r.Total)
	}

	// 日文件损坏:读它报错,不影响别的日子;索引坏了也能打开
	if err := os.WriteFile(filepath.Join(dir, "2026-10-27.json"), []byte("{坏"), 0o600); err != nil {
		t.Fatal(err)
	}
	s3, _ := Open(dir, ck.Now)
	if _, err := s3.DayView("2026-10-27", 0, nil, true); err == nil || !strings.Contains(err.Error(), "损坏") {
		t.Errorf("损坏的日文件: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, indexName), []byte("不是 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s4, err := Open(dir, ck.Now)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s4.DayView("2026-10-28", 0, nil, true); err != nil || r.Total.Up != 160 { // 落盘的是 160(s2 后来记的那 1 没落盘)
		t.Errorf("索引坏了: %+v %v", r, err)
	}
}

func TestMaintain(t *testing.T) {
	now := at("2026-10-28", 12, 0)
	s, ck, dir := newStore(t, at("2025-08-01", 12, 0))
	rec := func(day string, hosts int) {
		items := make([]Item, 0, hosts)
		var up int64
		for i := 0; i < hosts; i++ {
			items = append(items, item("c", "n", fmt.Sprintf("h%03d.com", i), int64(i+1), 0, 1, false))
			up += int64(i + 1)
		}
		if err := s.Record(at(day, 9, 0), Delta{Up: up, Conns: int64(hosts), Items: items}); err != nil {
			t.Fatal(err)
		}
	}
	rec("2025-08-01", 3)   // 超过 12 个月,会被删
	rec("2026-09-20", 300) // 超过 30 天:合并冷门;超过 7 天:删小时明细
	rec("2026-10-25", 5)   // 小时明细还在
	rec("2026-10-28", 5)   // 今天
	ck.t = now
	if err := s.Maintain(now, 12); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"2025-08-01.json", "2025-08-01.hours.json", "2026-09-20.hours.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s 该被删: %v", f, err)
		}
	}
	for _, f := range []string{"2026-09-20.json", "2026-10-25.json", "2026-10-25.hours.json", "2026-10-28.json", "2026-10-28.hours.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s 该保留: %v", f, err)
		}
	}
	// 10-01 已合并:最多 keepHosts+1 个访问目标,「其他」里有被合并的流量,合计不变
	r, err := s.DayView("2026-09-20", 5000, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	var other int64
	for _, h := range r.Hosts {
		sum += h.Up
		if h.Key == OtherLabel {
			other = h.Up
		}
	}
	if r.HostsCount != keepHosts+1 || sum != 300*301/2 || other == 0 {
		t.Errorf("合并后: 访问目标 %d 个,合计 %d,其他 %d", r.HostsCount, sum, other)
	}
	if other != 5050 { // h000 到 h099 的 up 之和
		t.Errorf("其他: %d", other)
	}
	found := false
	for _, h := range r.Hosts {
		found = found || h.Key == "h299.com"
	}
	if !found {
		t.Errorf("最大的访问目标没了: %+v", r.Hosts[:3])
	}
	// 再跑一遍不变(幂等)
	if err := s.Maintain(now, 12); err != nil {
		t.Fatal(err)
	}
	if r2, _ := s.DayView("2026-09-20", 5000, nil, true); r2.HostsCount != r.HostsCount {
		t.Errorf("不幂等: %d", r2.HostsCount)
	}

	u := s.Usage()
	if u.Days != 3 || u.OldestDay != "2026-09-20" || u.NewestDay != "2026-10-28" || u.Bytes <= 0 || u.Rows <= 0 || u.PerDay <= 0 || u.OldPerDay <= 0 || u.HourPerDay <= 0 ||
		u.HourKeepDays != HourKeepDays || u.CollapseAfterDays != CollapseAfterDays {
		t.Errorf("占用: %+v", u)
	}
	// 空库
	s0, _, _ := newStore(t, now)
	if u := s0.Usage(); u.Days != 0 || u.OldestDay != "" || u.PerDay != 0 {
		t.Errorf("空库: %+v", u)
	}
}

func TestAddEntryCap(t *testing.T) {
	tb := table{}
	for i := 0; i < 5; i++ {
		addEntry(tb, "c", "n", fmt.Sprintf("h%d", i), Counter{Up: 1}, 3)
	}
	if len(tb) != 4 || tb["c"+sep+"n"+sep+OtherLabel].Up != 2 { // 放满后的新访问目标进「其他」
		t.Errorf("%d 条: %+v", len(tb), tb)
	}
	addEntry(tb, "c", "n", "h0", Counter{Up: 5}, 3) // 已有的键照常累加
	if tb["c"+sep+"n"+sep+"h0"].Up != 6 {
		t.Errorf("已有的键: %+v", tb["c"+sep+"n"+sep+"h0"])
	}
}

func TestConcurrent(t *testing.T) {
	s, _, _ := newStore(t, at("2026-10-28", 12, 0))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.Record(at("2026-10-28", 10, j%60), Delta{Up: 1, Items: []Item{item("c", "n", fmt.Sprintf("h%d", i), 1, 1, 1, false)}})
				_, _ = s.DayView("2026-10-28", 0, nil, true)
				_, _ = s.Month("", true)
				if j%10 == 0 {
					_ = s.Flush()
					_ = s.Usage()
				}
			}
		}(i)
	}
	wg.Wait()
	if r, _ := s.DayView("2026-10-28", 0, nil, true); r.Total.Up != 200 {
		t.Errorf("总量: %+v", r.Total)
	}
}

// ---- 采集器 ----

type fakeSource struct {
	snaps []singbox.KernelSnapshot
	errs  []error
	i     int
}

func (f *fakeSource) get(context.Context) (singbox.KernelSnapshot, error) {
	i := f.i
	f.i++
	if i < len(f.errs) && f.errs[i] != nil {
		return singbox.KernelSnapshot{}, f.errs[i]
	}
	return f.snaps[i], nil
}

func conn(id, client, host string, up, down int64, chains ...string) singbox.KernelConn {
	return singbox.KernelConn{ID: id, Upload: up, Download: down, Chains: chains, Source: client, Host: host}
}

func TestCollector(t *testing.T) {
	s, ck, _ := newStore(t, at("2026-10-28", 10, 0))
	tags := []string{"直连"}
	src := &fakeSource{
		errs: []error{nil, nil, nil, errors.New("内核没在运行"), nil, nil, nil},
		snaps: []singbox.KernelSnapshot{
			// 0:建基线(内核已经跑了一阵,累计量很大,连接 a 已存在)——不记数
			{UploadTotal: 1000, DownloadTotal: 9000, DirectTags: tags, Conns: []singbox.KernelConn{conn("a", "192.168.1.5", "a.com", 100, 900, "x", "其他")}},
			// 1:a 涨了 10 / 90;新连接 b(直连、没有域名,有目的 IP)记全量;总量涨 50 / 300(含没采到的短连接)
			{UploadTotal: 1050, DownloadTotal: 9300, DirectTags: tags, Conns: []singbox.KernelConn{
				conn("a", "192.168.1.5", "a.com", 110, 990, "x", "其他"),
				{ID: "b", Upload: 5, Download: 20, Chains: []string{"直连", "其他"}, Source: "192.168.1.6", DestIP: "9.9.9.9"},
			}},
			// 2:a 关了(消失),b 涨了 1 / 2;新连接 c 没有来源和出站链
			{UploadTotal: 1056, DownloadTotal: 9330, DirectTags: tags, Conns: []singbox.KernelConn{
				{ID: "b", Upload: 6, Download: 22, Chains: []string{"直连", "其他"}, Source: "192.168.1.6", DestIP: "9.9.9.9"},
				conn("c", "", "", 3, 3),
			}},
			// 3:读不到(见 errs)
			{},
			// 4:断开之后重连:只建基线
			{UploadTotal: 7, DownloadTotal: 7, DirectTags: tags, Conns: []singbox.KernelConn{conn("z", "192.168.1.5", "z.com", 7, 7, "x", "其他")}},
			// 5:内核重启过(累计量比上次小):总量按从 0 算,新连接记全量
			{UploadTotal: 2, DownloadTotal: 3, DirectTags: tags, Conns: []singbox.KernelConn{conn("n1", "192.168.1.5", "n.com", 2, 3, "x", "其他")}},
			// 6:什么都没变:不记数
			{UploadTotal: 2, DownloadTotal: 3, DirectTags: tags, Conns: []singbox.KernelConn{conn("n1", "192.168.1.5", "n.com", 2, 3, "x", "其他")}},
		},
	}
	var errs []error
	c := &Collector{Source: src.get, Store: s, Now: ck.Now, KeepMonths: func() int { return 12 }, OnError: func(e error) { errs = append(errs, e) }}
	ctx := context.Background()
	step := func(min int) error { ck.t = at("2026-10-28", 10, min); return c.Tick(ctx) }

	if err := step(0); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.DayView("2026-10-28", 0, nil, true); r.Total != (Total{}) {
		t.Fatalf("基线不该记数: %+v", r.Total)
	}
	if err := step(1); err != nil {
		t.Fatal(err)
	}
	r, _ := s.DayView("2026-10-28", 0, nil, true)
	if r.Total != (Total{Up: 50, Down: 300, Conns: 1}) { // 只有 b 是新连接
		t.Errorf("第 1 次: %+v", r.Total)
	}
	byKey := func(rows []Row) map[string]Row {
		m := map[string]Row{}
		for _, x := range rows {
			m[x.Key] = x
		}
		return m
	}
	if n := byKey(r.Nodes); n["x"] != (Row{Key: "x", Up: 10, Down: 90}) || n["直连"] != (Row{Key: "直连", Up: 5, Down: 20, Conns: 1}) {
		t.Errorf("节点: %+v", r.Nodes)
	}
	if h := byKey(r.Hosts); h["a.com"].Up != 10 || h["9.9.9.9"].Down != 20 { // 没有域名用目的 IP
		t.Errorf("访问目标: %+v", r.Hosts)
	}
	if r.Other != (Sum{Up: 35, Down: 190}) { // 总量 350/ … 减去节点之和 15/110
		t.Errorf("other: %+v", r.Other)
	}
	if dr, _ := s.DayView("2026-10-28", 0, nil, false); dr.Total.Up != 45 || len(dr.Nodes) != 1 { // b 走的是直连
		t.Errorf("直连标记: %+v", dr)
	}

	if err := step(2); err != nil {
		t.Fatal(err)
	}
	r, _ = s.DayView("2026-10-28", 0, nil, true)
	if r.Total.Up != 56 || r.Total.Conns != 2 {
		t.Errorf("第 2 次: %+v", r.Total)
	}
	if n, h, cl := byKey(r.Nodes), byKey(r.Hosts), byKey(r.Clients); n[unknown].Up != 3 || h[unknown].Up != 3 || cl[unknown].Up != 3 || n["直连"].Up != 6 {
		t.Errorf("未知的归一: %+v %+v %+v", r.Nodes, r.Hosts, r.Clients)
	}

	if err := step(3); err == nil {
		t.Fatal("读不到该返回错误")
	}
	before, _ := s.DayView("2026-10-28", 0, nil, true)
	if err := step(4); err != nil { // 重连的第一次只建基线
		t.Fatal(err)
	}
	if after, _ := s.DayView("2026-10-28", 0, nil, true); after.Total != before.Total {
		t.Errorf("重连的基线不该记数: %+v → %+v", before.Total, after.Total)
	}
	if err := step(5); err != nil {
		t.Fatal(err)
	}
	r, _ = s.DayView("2026-10-28", 0, nil, true)
	if r.Total.Up != 56+2 || r.Total.Down != 330+3 || r.Total.Conns != 3 {
		t.Errorf("内核重启: %+v", r.Total)
	}
	if err := step(6); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.DayView("2026-10-28", 0, nil, true); again.Total != r.Total {
		t.Errorf("没变化不该记数: %+v", again.Total)
	}
	if len(errs) != 0 {
		t.Errorf("OnError: %v", errs)
	}
}

func TestCollectorRun(t *testing.T) {
	s, ck, dir := newStore(t, at("2026-10-28", 10, 0))
	var mu sync.Mutex
	n := int64(0)
	src := func(context.Context) (singbox.KernelSnapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		n += 10
		return singbox.KernelSnapshot{UploadTotal: n, DownloadTotal: n}, nil
	}
	c := &Collector{Source: src, Store: s, Now: ck.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, 5*time.Millisecond); close(done) }()
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done // 退出前会落盘
	s2, _ := Open(dir, ck.Now)
	if r, _ := s2.DayView("2026-10-28", 0, nil, true); r.Total.Up < 10 {
		t.Errorf("退出时没落盘: %+v", r.Total)
	}
}
