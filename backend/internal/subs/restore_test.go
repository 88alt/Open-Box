package subs

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

func rnode(tag string) sub.Node {
	return sub.Node{Tag: tag, OriginalTag: tag, Type: "trojan", Server: tag + ".test", ServerPort: 443, Fields: map[string]any{"password": "x"}}
}

func rrec(id, name string, enabled bool) Record {
	return Record{ID: id, Name: name, URL: "https://" + id + ".test/sub?token=1", Enabled: enabled, Format: "base64"}
}

func rrn(subID string, tags ...string) []RestoreNode {
	out := []RestoreNode{}
	for _, t := range tags {
		out = append(out, RestoreNode{Subscription: subID, Node: rnode(t)})
	}
	return out
}

// seed 先放进两份订阅:a(启用,n1 n2)、b(停用,n3)。
func seed(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil)
	if err := s.persist([]Record{rrec("a", "A", true), rrec("b", "B", false)}, map[string][]sub.Node{
		"a": {rnode("n1"), rnode("n2")}, "b": {rnode("n3")},
	}); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func ids(recs []Record) []string {
	out := []string{}
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

func TestRestoreReplaceClearsExistingAndWritesFileContent(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	in := RestoreInput{Mode: RestoreReplace,
		Records: []Record{
			func() Record {
				r := rrec("a", "  新A  ", true)
				r.NodeCount = 99 // 以实际节点数为准
				r.Account = map[string]any{"user": "u"}
				r.UserAgent = "  clash  "
				r.URLs = []string{"https://a.test/sub?token=1", "https://a2.test/x"}
				return r
			}(),
			rrec("z", "Z", false),
		},
		Nodes: append(rrn("a", "x1", "x2", "x3"), rrn("z", "z1")...),
	}
	res, err := s.Restore(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Subscriptions != 2 || res.Nodes != 4 || res.Renamed != 0 {
		t.Fatalf("结果数量不对: %+v", res)
	}
	if !reflect.DeepEqual(res.EnabledTags, map[string]bool{"x1": true, "x2": true, "x3": true}) {
		t.Fatalf("只有已启用订阅的节点算数: %v", res.EnabledTags)
	}
	recs, nodes, err := s.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(recs), []string{"a", "z"}) {
		t.Fatalf("应清空旧的、按文件顺序写入: %v", ids(recs))
	}
	a := recs[0]
	if a.Name != "新A" || a.NodeCount != 3 || a.Account != nil || a.UserAgent != "clash" || len(a.URLs) != 2 {
		t.Fatalf("订阅整理不对: %+v", a)
	}
	if a.CreatedAt == 0 || a.UpdatedAt == 0 {
		t.Fatalf("缺的时间戳应补上: %+v", a)
	}
	if len(nodes["a"]) != 3 || len(nodes["z"]) != 1 || len(nodes["b"]) != 0 {
		t.Fatalf("节点不对: %v", nodes)
	}
	if nodes["a"][0].Fields["password"] != "x" {
		t.Fatalf("节点内容应原样保存: %+v", nodes["a"][0])
	}
}

func TestRestoreReplaceWithEmptyFileClearsEverything(t *testing.T) {
	s, _ := seed(t)
	res, err := s.Restore(context.Background(), RestoreInput{Mode: RestoreReplace, Records: []Record{}})
	if err != nil || res.Subscriptions != 0 || res.Nodes != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if recs, nodes, _ := s.Dump(context.Background()); len(recs) != 0 || len(nodes) != 0 {
		t.Fatalf("应全部清空: %v %v", recs, nodes)
	}
}

func TestRestoreAppendRenamesIDsAndTags(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	in := RestoreInput{Mode: RestoreAppend,
		// a 的 id 与现有冲突;n1 与现有节点重名;「n1 2」是文件里自己的节点,改名不能撞上它
		Records: []Record{rrec("a", "A-backup", true), rrec("c", "C", true)},
		Nodes:   append(rrn("a", "n1", "n1 2", "only-new"), rrn("c", "n3")...),
	}
	res, err := s.Restore(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Subscriptions != 2 || res.Nodes != 4 || res.Renamed != 2 {
		t.Fatalf("append 的数量只算新加的: %+v", res)
	}
	recs, nodes, _ := s.Dump(ctx)
	if len(recs) != 4 || recs[0].ID != "a" || recs[1].ID != "b" || recs[3].ID != "c" {
		t.Fatalf("现有的在前、新的在后: %v", ids(recs))
	}
	newA := recs[2]
	if newA.ID == "a" || newA.Name != "A-backup" || newA.NodeCount != 3 {
		t.Fatalf("冲突的 id 应换新: %+v", newA)
	}
	if len(nodes["a"]) != 2 || nodes["a"][0].Tag != "n1" {
		t.Fatalf("现有订阅的节点不能动: %v", nodes["a"])
	}
	tags := []string{}
	for _, n := range nodes[newA.ID] {
		tags = append(tags, n.Tag)
	}
	if !reflect.DeepEqual(tags, []string{"n1 3", "n1 2", "only-new"}) {
		t.Fatalf("重名的改名(并避开文件里已有的名字): %v", tags)
	}
	if nodes[newA.ID][0].OriginalTag != "n1" {
		t.Fatalf("改名不改 originalTag: %+v", nodes[newA.ID][0])
	}
	if got := nodes["c"][0].Tag; got != "n3 2" {
		t.Fatalf("与停用订阅的节点重名也要改: %s", got)
	}
	// 全局唯一
	seen := map[string]bool{}
	for _, r := range recs {
		for _, n := range nodes[r.ID] {
			if seen[n.Tag] {
				t.Fatalf("节点名重复: %s", n.Tag)
			}
			seen[n.Tag] = true
		}
	}
}

func TestCheckRestoreWritesNothingAndMatchesRestore(t *testing.T) {
	s, st := seed(t)
	ctx := context.Background()
	var before subsFile
	_, _ = st.Load(fileSubs, &before)
	in := RestoreInput{Mode: RestoreAppend, Records: []Record{rrec("a", "A2", true)}, Nodes: rrn("a", "n1", "k")}
	chk, err := s.CheckRestore(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var after subsFile
	_, _ = st.Load(fileSubs, &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("CheckRestore 不该写盘")
	}
	got, err := s.Restore(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chk, got) {
		t.Fatalf("预检和真正写入的结果应一致\n%+v\n%+v", chk, got)
	}
	if !chk.EnabledTags["n1"] || !chk.EnabledTags["n1 2"] || !chk.EnabledTags["k"] || chk.EnabledTags["n3"] {
		t.Fatalf("EnabledTags(现有启用的 + 新加启用的,不含停用订阅的): %v", chk.EnabledTags)
	}
}

func TestRestoreRejectsBadInputAndChangesNothing(t *testing.T) {
	s, st := seed(t)
	ctx := context.Background()
	var before subsFile
	_, _ = st.Load(fileSubs, &before)
	ok := func() Record { return rrec("a", "A", true) }
	with := func(f func(r *Record)) []Record { r := ok(); f(&r); return []Record{r} }
	long := strings.Repeat("长", 101)
	cases := map[string]RestoreInput{
		"未知方式":       {Mode: "merge", Records: []Record{ok()}},
		"id 空":       {Mode: RestoreReplace, Records: with(func(r *Record) { r.ID = "" })},
		"id 含空格":     {Mode: RestoreReplace, Records: with(func(r *Record) { r.ID = "a b" })},
		"id 太长":      {Mode: RestoreReplace, Records: with(func(r *Record) { r.ID = strings.Repeat("a", 65) })},
		"id 重复":      {Mode: RestoreReplace, Records: []Record{ok(), ok()}},
		"名字空":        {Mode: RestoreReplace, Records: with(func(r *Record) { r.Name = "  " })},
		"名字太长":       {Mode: RestoreReplace, Records: with(func(r *Record) { r.Name = long })},
		"没有地址也没有内容":  {Mode: RestoreReplace, Records: with(func(r *Record) { r.URL = "" })},
		"地址不是 http":  {Mode: RestoreReplace, Records: with(func(r *Record) { r.URL = "ftp://x.test/a" })},
		"自动更新不合法":    {Mode: RestoreReplace, Records: with(func(r *Record) { r.AutoUpdate = map[string]any{"enabled": "yes"} })},
		"节点 DNS 不合法": {Mode: RestoreReplace, Records: with(func(r *Record) { r.NodeDNS = map[string]any{"url": "http://x"} })},
		"格式名太长":      {Mode: RestoreReplace, Records: with(func(r *Record) { r.Format = strings.Repeat("f", 33) })},
		"节点属于不存在的订阅": {Mode: RestoreReplace, Records: []Record{ok()}, Nodes: rrn("nope", "x")},
		"节点名为空":      {Mode: RestoreReplace, Records: []Record{ok()}, Nodes: rrn("a", "")},
		"节点名重复":      {Mode: RestoreReplace, Records: []Record{ok()}, Nodes: rrn("a", "x", "x")},
		"节点端口超范围":    {Mode: RestoreReplace, Records: []Record{ok()}, Nodes: []RestoreNode{{Subscription: "a", Node: sub.Node{Tag: "x", Type: "ss", ServerPort: 70000}}}},
		"节点类型空":      {Mode: RestoreReplace, Records: []Record{ok()}, Nodes: []RestoreNode{{Subscription: "a", Node: sub.Node{Tag: "x", ServerPort: 1}}}},
	}
	for name, in := range cases {
		if _, err := s.Restore(ctx, in); err == nil {
			t.Errorf("%s: 应被拒绝", name)
		} else if _, ok := err.(*InputError); !ok {
			t.Errorf("%s: 应是 InputError(400),得到 %T %v", name, err, err)
		}
		if _, err := s.CheckRestore(ctx, in); err == nil {
			t.Errorf("%s: CheckRestore 也应拒绝", name)
		}
	}
	var after subsFile
	_, _ = st.Load(fileSubs, &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("被拒绝的导入不该改动已有订阅")
	}
	if _, nodes, _ := s.Dump(ctx); len(nodes["a"]) != 2 {
		t.Fatalf("已有节点不该变: %v", nodes)
	}
}

func TestRestoreAcceptsPastedContentSource(t *testing.T) {
	s, _ := seed(t)
	r := Record{ID: "p", Name: "粘贴", Enabled: true, Content: "trojan://pw@h.test:443#n"}
	if _, err := s.Restore(context.Background(), RestoreInput{Mode: RestoreReplace, Records: []Record{r}, Nodes: rrn("p", "n")}); err != nil {
		t.Fatal(err)
	}
	recs, _, _ := s.Dump(context.Background())
	if recs[0].Content == "" || recs[0].URL != "" {
		t.Fatalf("粘贴内容的订阅应保留 content: %+v", recs[0])
	}
}

func TestRestoreLimits(t *testing.T) {
	s, _ := seed(t)
	many := make([]Record, MaxRestoreSubs+1)
	for i := range many {
		many[i] = rrec("s"+strings.Repeat("x", 1)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676)), "N", true)
	}
	_, err := s.CheckRestore(context.Background(), RestoreInput{Mode: RestoreReplace, Records: many})
	var ie *InputError
	if !errors.As(err, &ie) || !strings.Contains(ie.Msg, "订阅太多") {
		t.Fatalf("订阅数超限应被拒绝: %v", err)
	}
}
