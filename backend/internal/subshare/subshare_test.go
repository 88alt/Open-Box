package subshare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

type env struct {
	svc   *Service
	dir   string
	now   time.Time
	exist []string
	n     int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{dir: dir, now: time.UnixMilli(1_800_000_000_000), exist: []string{"s1", "s2", "s3"}}
	e.svc = &Service{
		Store:           st,
		SubscriptionIDs: func(context.Context) ([]string, error) { return e.exist, nil },
		Now:             func() time.Time { return e.now },
		NewToken: func() string {
			e.n++
			return fmt.Sprintf("generated-token-%020d-xxxxxxxxxx", e.n)
		},
	}
	return e
}

func p[T any](v T) *T { return &v }

func good() Input {
	return Input{Name: p(" 家里 "), Host: p("router.lan:3036"), Protocol: p("http"), SubscriptionIDs: p([]string{"s1", "s2"})}
}

func isInput(err error) bool { var ie *InputError; return errors.As(err, &ie) }

func TestCreateAndList(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if l, err := e.svc.List(ctx); err != nil || l == nil || len(l) != 0 {
		t.Fatalf("空列表应是非 nil 空切片: %#v %v", l, err)
	}
	in := good()
	in.Token = p(strings.Repeat("ab", 24)) // 48 位十六进制,与前端草稿同样的形状
	sh, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Name != "家里" || !sh.Enabled || sh.Token != strings.Repeat("ab", 24) || len(sh.ID) != 16 ||
		sh.CreatedAt != 1_800_000_000_000 || sh.UpdatedAt != sh.CreatedAt || sh.Host != "router.lan:3036" || sh.Protocol != "http" {
		t.Fatalf("%+v", sh)
	}
	l, _ := e.svc.List(ctx)
	if len(l) != 1 || l[0].ID != sh.ID {
		t.Fatalf("%+v", l)
	}
}

func TestCreateTokenRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mine := strings.Repeat("c", 40)
	a, _ := e.svc.Create(ctx, Input{Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Token: p(mine)})
	if a.Token != mine {
		t.Fatalf("合格且没占用的草稿 token 应被采用: %q", a.Token)
	}
	// 被占用 → 换一个
	b, _ := e.svc.Create(ctx, Input{Name: p("b"), SubscriptionIDs: p([]string{"s1"}), Token: p(mine)})
	if b.Token == mine || !strings.HasPrefix(b.Token, "generated-token-") {
		t.Fatalf("被占用应换新: %q", b.Token)
	}
	// 太短 / 字符不合格 → 不收,换一个
	for _, bad := range []string{"abc", strings.Repeat("a", 31), strings.Repeat("a", 129), strings.Repeat("a", 31) + "/", strings.Repeat("中", 40), ""} {
		c, err := e.svc.Create(ctx, Input{Name: p("c"), SubscriptionIDs: p([]string{"s1"}), Token: p(bad)})
		if err != nil || !strings.HasPrefix(c.Token, "generated-token-") {
			t.Errorf("token %q 应被替换: %+v %v", bad, c, err)
		}
	}
}

func TestGeneratedTokenSkipsUsedOnes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	used := strings.Repeat("d", 40)
	a, _ := e.svc.Create(ctx, Input{Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Token: p(used)})
	seq := []string{used, used, strings.Repeat("e", 40)}
	e.svc.NewToken = func() string { t := seq[0]; seq = seq[1:]; return t }
	b, err := e.svc.Create(ctx, Input{Name: p("b"), SubscriptionIDs: p([]string{"s1"})})
	if err != nil || b.Token != strings.Repeat("e", 40) || a.Token == b.Token {
		t.Fatalf("生成器撞上已占用的 token 应继续生成: %+v %v", b, err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	long := strings.Repeat("长", maxNameLen+1)
	cases := map[string]Input{
		"没传名称":   {SubscriptionIDs: p([]string{"s1"})},
		"没传订阅":   {Name: p("a")},
		"名称全空白":  {Name: p("  "), SubscriptionIDs: p([]string{"s1"})},
		"名称太长":   {Name: p(long), SubscriptionIDs: p([]string{"s1"})},
		"订阅为空":   {Name: p("a"), SubscriptionIDs: p([]string{})},
		"订阅全是空白": {Name: p("a"), SubscriptionIDs: p([]string{" ", ""})},
		"订阅不存在":  {Name: p("a"), SubscriptionIDs: p([]string{"s1", "nope"})},
		"协议不对":   {Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Protocol: p("ftp")},
		"主机带协议":  {Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Host: p("http://a.com")},
		"主机带路径":  {Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Host: p("a.com/x")},
		"主机带账号":  {Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Host: p("u@a.com")},
		"主机带空格":  {Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Host: p("a .com")},
		"主机端口不对": {Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Host: p("a.com:port")},
		"主机只有端口": {Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Host: p(":80")},
		"订阅太多":   {Name: p("a"), SubscriptionIDs: p(manyIDs(MaxSubscriptions + 1))},
	}
	for name, in := range cases {
		if _, err := e.svc.Create(ctx, in); !isInput(err) {
			t.Errorf("%s: 应是 InputError,got %v", name, err)
		}
	}
	if l, _ := e.svc.List(ctx); len(l) != 0 {
		t.Fatalf("校验不过不应写入: %+v", l)
	}
	for _, h := range []string{"", "a.com", "a.com:3036", "192.168.1.1", "192.168.1.1:80", "[fd00::1]:3036", "[fd00::1]"} {
		if _, err := e.svc.Create(ctx, Input{Name: p("a"), SubscriptionIDs: p([]string{"s1"}), Host: p(h)}); err != nil {
			t.Errorf("主机 %q 应合法: %v", h, err)
		}
	}
}

func manyIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("id%d", i)
	}
	return out
}

func TestCreateDedupesSubscriptionsKeepingOrder(t *testing.T) {
	e := newEnv(t)
	sh, err := e.svc.Create(context.Background(), Input{Name: p("a"), SubscriptionIDs: p([]string{"s2", " s1 ", "s2", "s1"})})
	if err != nil || fmt.Sprint(sh.SubscriptionIDs) != "[s2 s1]" {
		t.Fatalf("%+v %v", sh, err)
	}
}

func TestMaxShares(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < MaxShares; i++ {
		if _, err := e.svc.Create(ctx, Input{Name: p("a"), SubscriptionIDs: p([]string{"s1"})}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := e.svc.Create(ctx, Input{Name: p("a"), SubscriptionIDs: p([]string{"s1"})}); !isInput(err) {
		t.Fatalf("超过上限应 InputError: %v", err)
	}
}

func TestUpdatePartialAndEnabled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sh, _ := e.svc.Create(ctx, good())
	e.now = e.now.Add(time.Minute)
	got, err := e.svc.Update(ctx, sh.ID, Input{Enabled: p(false)})
	if err != nil || got.Enabled || got.Name != "家里" || got.Token != sh.Token || got.UpdatedAt != e.now.UnixMilli() || got.CreatedAt != sh.CreatedAt {
		t.Fatalf("只改 enabled: %+v %v", got, err)
	}
	// 改名 / 主机 / 协议 / 订阅(token 不变)
	got, err = e.svc.Update(ctx, sh.ID, Input{Name: p("公司"), Host: p(""), Protocol: p("https"), SubscriptionIDs: p([]string{"s3"})})
	if err != nil || got.Name != "公司" || got.Host != "" || got.Protocol != "https" || fmt.Sprint(got.SubscriptionIDs) != "[s3]" || got.Token != sh.Token {
		t.Fatalf("%+v %v", got, err)
	}
	// 校验不过:不改任何东西
	before, _ := e.svc.List(ctx)
	if _, err := e.svc.Update(ctx, sh.ID, Input{Name: p("新名字"), Protocol: p("ftp")}); !isInput(err) {
		t.Fatalf("%v", err)
	}
	after, _ := e.svc.List(ctx)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("校验不过不应改动:\n%v\n%v", before, after)
	}
	if _, err := e.svc.Update(ctx, "nope", Input{Enabled: p(true)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestUpdateNoChangeDoesNotWrite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sh, _ := e.svc.Create(ctx, good())
	path := filepath.Join(e.dir, fileName+".json")
	st1, err := os.Stat(path)
	if err != nil {
		t.Skipf("存储文件名与预期不同: %v", err)
	}
	e.now = e.now.Add(time.Hour)
	time.Sleep(10 * time.Millisecond)
	same := good()
	same.Name = p("家里")
	got, err := e.svc.Update(ctx, sh.ID, same)
	if err != nil || got.UpdatedAt != sh.UpdatedAt {
		t.Fatalf("没变化 updatedAt 不应动: %+v %v", got, err)
	}
	st2, _ := os.Stat(path)
	if !st2.ModTime().Equal(st1.ModTime()) {
		t.Errorf("没变化不应写盘")
	}
}

func TestUpdateKeepsSavedSubscriptionsEvenIfDeleted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sh, _ := e.svc.Create(ctx, good()) // s1 s2
	e.exist = []string{"s1"}           // s2 的订阅被删了
	// 前端的启停会把原来的订阅 id 原样带回来:不能因为 s2 没了就报错
	got, err := e.svc.Update(ctx, sh.ID, Input{SubscriptionIDs: p([]string{"s1", "s2"}), Enabled: p(false)})
	if err != nil || fmt.Sprint(got.SubscriptionIDs) != "[s1 s2]" || got.Enabled {
		t.Fatalf("%+v %v", got, err)
	}
	// 但新加一个不存在的仍然不行
	if _, err := e.svc.Update(ctx, sh.ID, Input{SubscriptionIDs: p([]string{"s1", "s3"})}); !isInput(err) {
		t.Fatalf("新选的不存在的订阅应 InputError: %v", err)
	}
}

func TestRegenerateAndDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, _ := e.svc.Create(ctx, good())
	b, _ := e.svc.Create(ctx, good())
	e.now = e.now.Add(time.Minute)
	got, err := e.svc.Regenerate(ctx, a.ID)
	if err != nil || got.Token == a.Token || got.ID != a.ID || got.UpdatedAt != e.now.UnixMilli() || got.Name != a.Name {
		t.Fatalf("%+v %v", got, err)
	}
	// 通过 Update 的 regenerate 也一样,且不会和别的分享撞 token
	got2, err := e.svc.Update(ctx, a.ID, Input{Regenerate: true})
	if err != nil || got2.Token == got.Token || got2.Token == b.Token {
		t.Fatalf("%+v %v", got2, err)
	}
	if _, err := e.svc.Regenerate(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := e.svc.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := e.svc.List(ctx); len(l) != 1 || l[0].ID != b.ID {
		t.Fatalf("%+v", l)
	}
	if err := e.svc.Delete(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删应 ErrNotFound: %v", err)
	}
}

func TestSubscriptionListErrorIsNotInputError(t *testing.T) {
	e := newEnv(t)
	e.svc.SubscriptionIDs = func(context.Context) ([]string, error) { return nil, errors.New("读不到") }
	_, err := e.svc.Create(context.Background(), good())
	if err == nil || isInput(err) {
		t.Fatalf("读不到订阅列表是服务端错误,不是 400: %v", err)
	}
}

func TestCorruptedFileIsErrorNotOverwritten(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(e.dir, fileName+".json")
	if err := os.WriteFile(path, []byte("{坏的"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.List(context.Background()); err == nil {
		t.Fatal("损坏的文件应报错")
	}
	if _, err := e.svc.Create(context.Background(), good()); err == nil {
		t.Fatal("损坏的文件不能被新建覆盖")
	}
	if b, _ := os.ReadFile(path); string(b) != "{坏的" {
		t.Fatalf("文件被改动: %q", b)
	}
}

func TestConcurrentCreatesKeepAll(t *testing.T) {
	e := newEnv(t)
	e.svc.NewToken = nil // 用真随机
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.svc.Create(context.Background(), good()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	l, _ := e.svc.List(context.Background())
	toks := map[string]bool{}
	ids := map[string]bool{}
	for _, s := range l {
		toks[s.Token], ids[s.ID] = true, true
		if len(s.Token) != 48 {
			t.Errorf("随机 token 应是 48 位: %q", s.Token)
		}
	}
	if len(l) != 20 || len(toks) != 20 || len(ids) != 20 {
		t.Fatalf("并发新建丢了或重复: %d %d %d", len(l), len(toks), len(ids))
	}
}
