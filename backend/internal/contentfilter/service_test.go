package contentfilter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const surgeText = "[Rule]\nDOMAIN-SUFFIX,{{{host}}},REJECT\nDOMAIN,b.com,REJECT\n"

type env struct {
	svc   *Service
	st    *store.Store
	dir   string
	pages map[string]string
	hits  int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, dir: filepath.Join(root, "cf"), pages: map[string]string{}}
	n := 0
	e.svc = &Service{
		Store: st, Dir: e.dir,
		Fetch: func(_ context.Context, u string) ([]byte, error) {
			e.hits++
			p, ok := e.pages[u]
			if !ok {
				return nil, &rulelist.FetchError{Msg: "无法连接 " + u}
			}
			return []byte(p), nil
		},
		Now:   func() time.Time { return time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC) },
		NewID: func() string { n++; return fmt.Sprintf("cf%d", n) },
	}
	return e
}

func ctx() context.Context { return context.Background() }

func TestEmptyList(t *testing.T) {
	e := newEnv(t)
	l, err := e.svc.List(ctx())
	if err != nil || l.Sets == nil || len(l.Sets) != 0 || l.Size != 0 || len(l.SHA256) != 64 {
		t.Fatalf("空列表不对: %+v %v", l, err)
	}
}

func TestSaveFromTextAndSource(t *testing.T) {
	e := newEnv(t)
	txt := "[Rule]\r\nDOMAIN,a.com,REJECT  \r\nfoo\r\n"
	l, err := e.svc.Save(ctx(), SaveInput{Name: " 甲 ", Dialect: "surge", Text: &txt})
	if err != nil || len(l.Sets) != 1 {
		t.Fatal(l, err)
	}
	s := l.Sets[0]
	if s.ID != "cf1" || s.Name != "甲" || !s.Enabled || s.RuleCount != 1 || s.Skipped != 1 || s.UpdatedAt != "2026-11-04T08:00:00Z" {
		t.Fatalf("记录不对: %+v", s)
	}
	got, err := e.svc.Source(ctx(), "cf1")
	if err != nil || got != "[Rule]\nDOMAIN,a.com,REJECT\nfoo\n" {
		t.Fatalf("文本应规整后存盘: %q %v", got, err)
	}
	if e.hits != 0 {
		t.Fatal("给了文本不该联网")
	}
}

func TestSaveFetchesAndSubstitutes(t *testing.T) {
	e := newEnv(t)
	e.pages["https://x/r"] = surgeText
	l, err := e.svc.Save(ctx(), SaveInput{Name: "乙", Dialect: "loon", SourceURL: "https://x/r", Arguments: map[string]any{"host": "ads.com"}})
	if err != nil {
		t.Fatal(err)
	}
	s := l.Sets[0]
	if s.SourceURL != "https://x/r" || s.RuleCount != 2 || s.Arguments == nil || s.Arguments.Values["host"] != "ads.com" {
		t.Fatalf("记录不对: %+v", s)
	}
	if txt, _ := e.svc.Source(ctx(), s.ID); !strings.Contains(txt, "DOMAIN-SUFFIX,ads.com,REJECT") {
		t.Fatalf("应代入参数: %q", txt)
	}
	// 「立即更新」:只带 id / name / dialect / sourceUrl,没带参数 → 沿用已存的值
	e.pages["https://x/r"] = "[Rule]\nDOMAIN-SUFFIX,{{{host}}},REJECT\nDOMAIN,c.com,REJECT\nDOMAIN,d.com,REJECT\n"
	l, err = e.svc.Save(ctx(), SaveInput{ID: s.ID, Name: "乙", Dialect: "loon", SourceURL: "https://x/r"})
	if err != nil || l.Sets[0].RuleCount != 3 || l.Sets[0].Arguments.Values["host"] != "ads.com" {
		t.Fatalf("刷新应沿用参数: %+v %v", l, err)
	}
	if txt, _ := e.svc.Source(ctx(), s.ID); !strings.Contains(txt, "ads.com") {
		t.Fatalf("刷新后的文本应仍代入: %q", txt)
	}
	// 显式给空参数 = 整个替换成空
	l, err = e.svc.Save(ctx(), SaveInput{ID: s.ID, Name: "乙", Dialect: "loon", SourceURL: "https://x/r", Arguments: map[string]any{}})
	if err != nil || l.Sets[0].Arguments != nil {
		t.Fatalf("空参数应清掉: %+v %v", l, err)
	}
}

func TestSaveKeepsTextWhenOnlyRenaming(t *testing.T) {
	e := newEnv(t)
	txt := "DOMAIN,a.com\n"
	l, _ := e.svc.Save(ctx(), SaveInput{Name: "甲", Dialect: "surge", Text: &txt})
	l, err := e.svc.Save(ctx(), SaveInput{ID: l.Sets[0].ID, Name: "改名", Dialect: "shadowrocket"})
	if err != nil || l.Sets[0].Name != "改名" || l.Sets[0].Dialect != Shadowrocket || l.Sets[0].RuleCount != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	if got, _ := e.svc.Source(ctx(), l.Sets[0].ID); got != txt {
		t.Fatalf("文本应保持: %q", got)
	}
}

func TestSaveErrors(t *testing.T) {
	e := newEnv(t)
	ok := "DOMAIN,a.com\n"
	empty, blank := "", "  \n"
	long := strings.Repeat("DOMAIN,a.com\n", MaxTextBytes/10)
	cases := []struct {
		name string
		in   SaveInput
		is   func(error) bool
	}{
		{"没名字", SaveInput{Dialect: "surge", Text: &ok}, isInput},
		{"名字太长", SaveInput{Name: strings.Repeat("长", 81), Dialect: "surge", Text: &ok}, isInput},
		{"格式不认识", SaveInput{Name: "a", Dialect: "clash", Text: &ok}, isInput},
		{"链接不是 http", SaveInput{Name: "a", Dialect: "surge", SourceURL: "ftp://x/y"}, isInput},
		{"文本和链接都没有", SaveInput{Name: "a", Dialect: "surge", Text: &empty}, isInput},
		{"只有空白", SaveInput{Name: "a", Dialect: "surge", Text: &blank}, isInput},
		{"认不出任何规则", SaveInput{Name: "a", Dialect: "surge", Text: strp("hello\nworld\n")}, isInput},
		{"文本太大", SaveInput{Name: "a", Dialect: "surge", Text: &long}, isInput},
		{"不是 UTF-8", SaveInput{Name: "a", Dialect: "surge", Text: strp("DOMAIN,a\xff.com\n")}, isInput},
		{"参数类型不对", SaveInput{Name: "a", Dialect: "surge", Text: &ok, Arguments: map[string]any{"k": []any{1}}}, isInput},
		{"参数名带花括号", SaveInput{Name: "a", Dialect: "surge", Text: &ok, Arguments: map[string]any{"{k}": "v"}}, isInput},
		{"不存在的 id", SaveInput{ID: "nope", Name: "a", Dialect: "surge", Text: &ok}, func(err error) bool { return errors.Is(err, ErrNotFound) }},
		{"抓取失败原样传出", SaveInput{Name: "a", Dialect: "surge", SourceURL: "https://down/x"}, func(err error) bool {
			var fe *rulelist.FetchError
			return errors.As(err, &fe)
		}},
	}
	for _, c := range cases {
		if _, err := e.svc.Save(ctx(), c.in); err == nil || !c.is(err) {
			t.Errorf("%s: 错误不对: %v", c.name, err)
		}
	}
	if l, _ := e.svc.List(ctx()); len(l.Sets) != 0 {
		t.Fatalf("失败不该留下记录: %+v", l.Sets)
	}
	if ents, _ := os.ReadDir(e.dir); len(ents) != 0 {
		t.Fatalf("失败不该留下文本文件: %v", ents)
	}
}

func TestMaxSets(t *testing.T) {
	e := newEnv(t)
	ok := "DOMAIN,a.com\n"
	for i := 0; i < MaxSets; i++ {
		if _, err := e.svc.Save(ctx(), SaveInput{Name: "n", Dialect: "surge", Text: &ok}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := e.svc.Save(ctx(), SaveInput{Name: "n", Dialect: "surge", Text: &ok}); !isInput(err) {
		t.Fatalf("超过上限应 400: %v", err)
	}
	if ents, _ := os.ReadDir(e.dir); len(ents) != MaxSets {
		t.Fatalf("被拒的新建不该留下孤儿文件: %d", len(ents))
	}
}

func TestEnableDeleteAndDigest(t *testing.T) {
	e := newEnv(t)
	a, b := "DOMAIN,a.com\n", "DOMAIN,b.com\n"
	e.svc.Save(ctx(), SaveInput{Name: "A", Dialect: "surge", Text: &a})
	l, _ := e.svc.Save(ctx(), SaveInput{Name: "B", Dialect: "loon", Text: &b})
	both := l.SHA256
	if l.Size == 0 {
		t.Fatal("size 应大于 0")
	}
	l, err := e.svc.SetEnabled(ctx(), "cf2", false)
	if err != nil || l.Sets[1].Enabled || l.SHA256 == both {
		t.Fatalf("停用后摘要应变: %+v %v", l, err)
	}
	onlyA := l.SHA256
	l, _ = e.svc.SetEnabled(ctx(), "cf2", true)
	if l.SHA256 != both {
		t.Fatal("再启用应回到原摘要(内容相同则摘要相同,与新增顺序无关)")
	}
	// 改文本后摘要失效重算,即使 updatedAt 没变(测试里时钟固定)
	b2 := "DOMAIN,bb.com\n"
	l, _ = e.svc.Save(ctx(), SaveInput{ID: "cf2", Name: "B", Dialect: "loon", Text: &b2})
	if l.SHA256 == both {
		t.Fatal("改了文本摘要应变")
	}
	l, err = e.svc.Delete(ctx(), "cf2")
	if err != nil || len(l.Sets) != 1 || l.SHA256 != onlyA {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := os.Stat(e.svc.textPath("cf2")); !os.IsNotExist(err) {
		t.Fatal("删除应连文本一起删")
	}
	if _, err := e.svc.Delete(ctx(), "cf2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("重复删除应 404")
	}
	if _, err := e.svc.SetEnabled(ctx(), "../x", true); !errors.Is(err, ErrNotFound) {
		t.Fatal("坏 id 应 404")
	}
	if _, err := e.svc.Source(ctx(), "../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatal("路径穿越应 404")
	}
}

func TestDigestFollowsExternalWipe(t *testing.T) {
	e := newEnv(t)
	a := "DOMAIN,a.com\n"
	e.svc.Save(ctx(), SaveInput{Name: "A", Dialect: "surge", Text: &a})
	if l, _ := e.svc.List(ctx()); l.Size == 0 {
		t.Fatal("先有内容")
	}
	if _, err := e.st.Clear(); err != nil { // 恢复出厂设置会直接清掉 json
		t.Fatal(err)
	}
	l, err := e.svc.List(ctx())
	if err != nil || len(l.Sets) != 0 || l.Size != 0 {
		t.Fatalf("清掉后摘要应跟着变: %+v %v", l, err)
	}
}

func TestPreview(t *testing.T) {
	e := newEnv(t)
	e.pages["https://x/r"] = "\ufeff" + strings.ReplaceAll(surgeText, "\n", "\r\n")
	text, err := e.svc.Preview(ctx(), PreviewInput{Dialect: "surge", SourceURL: "https://x/r", Arguments: map[string]any{"host": "z.com"}})
	if err != nil || text != "[Rule]\nDOMAIN-SUFFIX,z.com,REJECT\nDOMAIN,b.com,REJECT\n" {
		t.Fatalf("%q %v", text, err)
	}
	e.pages["https://x/junk"] = "hello world"
	for name, in := range map[string]PreviewInput{
		"没链接":   {Dialect: "surge"},
		"格式不对":  {Dialect: "x", SourceURL: "https://x/r"},
		"认不出内容": {Dialect: "surge", SourceURL: "https://x/junk"},
		"链接坏":   {Dialect: "surge", SourceURL: "javascript:1"},
		"参数不合法": {Dialect: "surge", SourceURL: "https://x/r", Arguments: map[string]any{"a": nil}},
	} {
		if _, err := e.svc.Preview(ctx(), in); err == nil {
			t.Errorf("%s: 应报错", name)
		}
	}
	if l, _ := e.svc.List(ctx()); len(l.Sets) != 0 {
		t.Fatal("预览不该保存")
	}
}

func TestRestoreDefaults(t *testing.T) {
	e := newEnv(t)
	a := "DOMAIN,a.com\n"
	e.svc.Save(ctx(), SaveInput{Name: "自己的", Dialect: "surge", Text: &a})
	// 默认为空:恢复默认 = 清空列表,不报错(覆盖 BuiltinDefaults,单元测试不依赖随包数据)
	e.svc.Defaults = func() ([]Default, error) { return nil, nil }
	l, err := e.svc.RestoreDefaults(ctx())
	if err != nil || len(l.Sets) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
	if ents, _ := os.ReadDir(e.dir); len(ents) != 0 {
		t.Fatalf("清空应连文本一起删: %v", ents)
	}
	// 默认不为空:回到随包内容和启用状态,补回被删的,保留自己加的
	e.svc.Defaults = func() ([]Default, error) {
		return []Default{
			{ID: "def-a", Name: "默认甲", Dialect: Surge, Enabled: true, Text: "DOMAIN,d1.com\nDOMAIN,d2.com\n"},
			{ID: "def-b", Name: "默认乙", Dialect: Loon, Enabled: false, Text: "[Script]\nhttp-response ^h script-path=https://s/j.js\n"},
		}, nil
	}
	e.svc.Save(ctx(), SaveInput{Name: "自己的", Dialect: "surge", Text: &a})
	l, err = e.svc.RestoreDefaults(ctx())
	if err != nil || len(l.Sets) != 3 {
		t.Fatalf("%+v %v", l, err)
	}
	e.svc.SetEnabled(ctx(), "def-a", false)
	e.svc.Delete(ctx(), "def-b")
	e.svc.Save(ctx(), SaveInput{ID: "def-a", Name: "改过", Dialect: "surge", Text: &a})
	l, err = e.svc.RestoreDefaults(ctx())
	if err != nil || len(l.Sets) != 3 {
		t.Fatalf("补回后应是 3 份(2 默认 + 1 自己的): %+v %v", l, err)
	}
	for _, s := range l.Sets {
		if s.ID == "def-a" && (s.Name != "默认甲" || !s.Enabled || s.RuleCount != 2) {
			t.Fatalf("def-a 应回到随包内容: %+v", s)
		}
		if s.ID == "def-b" && (s.Enabled || s.ScriptCount != 1) {
			t.Fatalf("def-b 应补回且保持停用: %+v", s)
		}
	}
	// 默认本身坏了:报错,不动现有数据
	e.svc.Defaults = func() ([]Default, error) {
		return []Default{{ID: "bad", Name: "x", Dialect: Surge, Text: "nothing"}}, nil
	}
	if _, err := e.svc.RestoreDefaults(ctx()); err == nil {
		t.Fatal("坏的默认应报错")
	}
	if l, _ := e.svc.List(ctx()); len(l.Sets) != 3 {
		t.Fatal("报错时不该改动现有数据")
	}
}

func isInput(err error) bool {
	var ie *InputError
	return errors.As(err, &ie)
}

func strp(s string) *string { return &s }

func TestSaveEveryDialect(t *testing.T) {
	e := newEnv(t)
	texts := map[string]string{
		"surge":        "DOMAIN,a.com\n",
		"shadowrocket": "DOMAIN,a.com\n",
		"loon":         "DOMAIN,a.com\n",
		"quantumultx":  "host, a.com, reject\n",
		"stash":        "rules:\n  - DOMAIN,a.com,REJECT\n",
	}
	ids := map[string]string{} // texts 是 map,遍历顺序随机,所以按格式名记下每份的 id,不能写死
	for d, txt := range texts {
		l, err := e.svc.Save(ctx(), SaveInput{Name: d, Dialect: d, Text: strp(txt)})
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		got := l.Sets[len(l.Sets)-1]
		if got.Dialect != Dialect(d) || got.RuleCount != 1 {
			t.Fatalf("%s: %+v", d, got)
		}
		ids[d] = got.ID
	}
	// 同一份文本换格式要重数:quantumultx 的写法按 surge 认不出,保存被拒
	if _, err := e.svc.Save(ctx(), SaveInput{ID: ids["quantumultx"], Name: "x", Dialect: "surge"}); !isInput(err) {
		t.Fatalf("quantumultx 的文本改成 surge 应认不出而被拒: %v", err)
	}
}
