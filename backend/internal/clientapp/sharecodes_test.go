package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	linkA = "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@203.0.113.7:8388#%E5%AE%B6"
	linkB = "vless://11111111-2222-3333-4444-555555555555@203.0.113.7:443?security=reality#b"
)

type shareEnv struct {
	s       *Service
	dir     string
	servers *[]any
}

// shareSvc 搭一个档案里有 a、b 两台共享服务器的服务;*servers 可以在测试里改(模拟面板里增删服务器)。
func shareSvc(t *testing.T) *shareEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	servers := []any{map[string]any{"id": "a", "name": "A"}, map[string]any{"id": "b", "name": "B"}}
	s := &Service{
		Store:     st,
		Profile:   func(context.Context) (map[string]any, error) { return map[string]any{"servers": servers}, nil },
		LAN:       func() ([]string, []string) { return []string{"192.168.5.1"}, []string{"192.168.5.0/24"} },
		PanelPort: 3036,
		KDFIter:   testIter,
		Now:       func() time.Time { return time.UnixMilli(1760000000000) },
	}
	return &shareEnv{s: s, dir: dir, servers: &servers}
}

func openShare(t *testing.T, s *Service, sid string, sl ShareSeal) sharePlain {
	t.Helper()
	rid, err := s.RouterID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(sl.Sealed), &env); err != nil {
		t.Fatalf("sealed 应是封包 JSON: %v", err)
	}
	plain, err := OpenShare(env, sl.PairCode, rid, sid, sl.Gen, testIter, testIter)
	if err != nil {
		t.Fatalf("配对码应能解开 %s: %v", sid, err)
	}
	var p sharePlain
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestShareCodesFirstIssue(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	out, err := e.s.ShareCodes(ctx, map[string]string{"a": linkA, "b": linkB})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("两台都应有: %+v", out)
	}
	for _, id := range []string{"a", "b"} {
		sl := out[id]
		if !validPairCode(sl.PairCode) || sl.Gen != firstGen || !validGen(sl.Gen) {
			t.Fatalf("%s 的配对码 / 代数不合格: %+v", id, sl)
		}
		p := openShare(t, e.s, id, sl)
		want := map[string]string{"a": linkA, "b": linkB}[id]
		if p.V != 1 || p.Kind != "share-link" || p.SID != id || p.Gen != firstGen || p.Link != want || p.GeneratedAt != 1760000000000 {
			t.Fatalf("%s 明文: %+v", id, p)
		}
		if strings.Contains(sl.Sealed, "203.0.113.7") {
			t.Fatalf("%s 的封包里不能有明文链接", id)
		}
	}
	if out["a"].PairCode == out["b"].PairCode {
		t.Fatal("每台服务器自己的配对码,不该相同")
	}
}

func TestShareCodesSkipsUnacceptable(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	long := strings.Repeat("x", MaxShareLinkLen+1)
	atLimit := strings.Repeat("y", MaxShareLinkLen)
	out, err := e.s.ShareCodes(ctx, map[string]string{
		"a":       linkA,
		"b":       "",              // 空链接
		"ghost":   linkB,           // 档案里没有
		"bad id!": linkB,           // id 不合格
		"c":       linkB,           // 档案里没有
		"long":    long,            // 太长
		"bad":     "ss://\xff\xfe", // 不是合法文本
		"ok":      atLimit,         // 恰好 4096 字符但档案里没有,仍被跳过
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out["a"].PairCode == "" {
		t.Fatalf("只应有 a: %+v", out)
	}
	// 恰好 4096 字符的链接(服务器在档案里)可以出码,多一个字符不行
	*e.servers = append(*e.servers, map[string]any{"id": "edge"}, map[string]any{"id": "over"})
	out, err = e.s.ShareCodes(ctx, map[string]string{"edge": atLimit, "over": long})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["edge"]; !ok || len(out) != 1 {
		t.Fatalf("4096 字符应收、4097 不收: %+v", out)
	}
	// 被跳过的不建状态
	var st state
	e.s.Store.Load(stateFile, &st)
	if len(st.Shares) != 2 || st.Shares["a"] == nil || st.Shares["edge"] == nil {
		t.Fatalf("状态里只应有 a 和 edge: %+v", st.Shares)
	}
}

func TestShareCodesTooMany(t *testing.T) {
	e := shareSvc(t)
	links := map[string]string{}
	for i := 0; i <= MaxShareLinks; i++ {
		links[string(rune('a'+i%26))+strings.Repeat("0", i/26)] = linkA
	}
	_, err := e.s.ShareCodes(context.Background(), links)
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("超过上限应是 *InputError: %v", err)
	}
}

func TestShareCodesStableAndRefreshOnLinkChange(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	first, err := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(e.dir, "client-app.json")
	before, _ := os.ReadFile(statePath)
	// 同一条链接再交一次:配对码、代数、封包都不变,也不写盘(面板每次打开页面都会交)
	again, err := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	if err != nil {
		t.Fatal(err)
	}
	if again["a"] != first["a"] {
		t.Fatalf("同一条链接应原样返回: %+v vs %+v", again["a"], first["a"])
	}
	if after, _ := os.ReadFile(statePath); string(after) != string(before) {
		t.Fatal("没有变化不该写盘")
	}
	// 链接变了(服务器改了密码):配对码和代数不变,封包换新,新封包解出新链接
	e.s.Now = func() time.Time { return time.UnixMilli(1760000999000) }
	changed, err := e.s.ShareCodes(ctx, map[string]string{"a": linkA + "&x=1"})
	if err != nil {
		t.Fatal(err)
	}
	c := changed["a"]
	if c.PairCode != first["a"].PairCode || c.Gen != first["a"].Gen || c.Sealed == first["a"].Sealed {
		t.Fatalf("链接变了应只换封包: %+v", c)
	}
	if p := openShare(t, e.s, "a", c); p.Link != linkA+"&x=1" || p.GeneratedAt != 1760000999000 {
		t.Fatalf("新封包明文: %+v", p)
	}
}

func TestShareCodesSurviveRestart(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	first, _ := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	// 换一个服务实例(重启),同一个数据目录
	s2 := &Service{Store: e.s.Store, Profile: e.s.Profile, KDFIter: testIter, Now: e.s.Now}
	again, err := s2.ShareCodes(ctx, map[string]string{"a": linkA})
	if err != nil {
		t.Fatal(err)
	}
	if again["a"] != first["a"] {
		t.Fatalf("重启后同一条链接应认得出来、原样返回: %+v vs %+v", again["a"], first["a"])
	}
}

func TestShareCodesStateHasNoPlainLink(t *testing.T) {
	e := shareSvc(t)
	if _, err := e.s.ShareCodes(context.Background(), map[string]string{"a": linkA, "b": linkB}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, "client-app.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "203.0.113.7") || strings.Contains(string(raw), "password") {
		t.Fatal("状态文件里不能有明文链接")
	}
}

func TestShareSealBindings(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	out, _ := e.s.ShareCodes(ctx, map[string]string{"a": linkA, "b": linkB})
	rid, _ := e.s.RouterID(ctx)
	var env Envelope
	json.Unmarshal([]byte(out["a"].Sealed), &env)
	open := func(pair, router, sid, gen string) error {
		_, err := OpenShare(env, pair, router, sid, gen, testIter, testIter)
		return err
	}
	if err := open(out["a"].PairCode, rid, "a", out["a"].Gen); err != nil {
		t.Fatalf("对的应能解开: %v", err)
	}
	// 封包没有 AAD,绑定靠明文里的 routerId / sid / gen:配对码对、别的对不上也得拒绝
	for name, err := range map[string]error{
		"别台服务器的配对码": open(out["b"].PairCode, rid, "a", out["a"].Gen),
		"别的服务器 id":  open(out["a"].PairCode, rid, "b", out["a"].Gen),
		"别的代数":      open(out["a"].PairCode, rid, "a", "0000000000000002"),
		"别的路由器":     open(out["a"].PairCode, strings.Repeat("c", 32), "a", out["a"].Gen),
	} {
		if !errors.Is(err, ErrSealOpen) {
			t.Errorf("%s 不该解得开: %v", name, err)
		}
	}
	// 本地分流的封包不是 share-link,即使配对码碰巧一样也不被 OpenShare 认
	plain, _ := json.Marshal(map[string]any{"v": 1, "kind": "client-config"})
	local, err := seal(out["a"].PairCode, testIter, plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenShare(local, out["a"].PairCode, rid, "a", out["a"].Gen, testIter, testIter); !errors.Is(err, ErrSealOpen) {
		t.Errorf("别的用途的封包不该被 OpenShare 认: %v", err)
	}
}

func TestShareCodesPruneRemovedServers(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	e.s.ShareCodes(ctx, map[string]string{"a": linkA, "b": linkB})
	if _, err := e.s.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	var st state
	e.s.Store.Load(stateFile, &st)
	genB := st.Shares["b"].Gen
	*e.servers = (*e.servers)[:1] // 删掉 b
	// 取回:b 已不在档案里,即使状态里还有也不给
	if _, err := e.s.Share(ctx, "b", genB, "10.0.0.9"); !errors.Is(err, ErrSyncNotFound) {
		t.Fatalf("已删除的服务器应 404: %v", err)
	}
	// 下一次出码时清掉 b 的状态
	e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	st = state{}
	e.s.Store.Load(stateFile, &st)
	if st.Shares["b"] != nil || st.Shares["a"] == nil {
		t.Fatalf("b 的状态应被清掉、a 保留: %+v", st.Shares)
	}
}

func TestShareDisabledDoesNotCountFailures(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	out, _ := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	for i := 0; i < 30; i++ {
		if _, err := e.s.Share(ctx, "a", out["a"].Gen, "1.2.3.4"); !errors.Is(err, ErrDisabled) {
			t.Fatalf("开关关着应回 ErrDisabled: %v", err)
		}
	}
	// 关着时不记失败:打开后马上能用,不被限速
	e.s.SetClientConfigEnabled(ctx, true)
	if _, err := e.s.Share(ctx, "a", out["a"].Gen, "1.2.3.4"); err != nil {
		t.Fatalf("开关关着时不该累计失败: %v", err)
	}
}

func TestShareRetrieve(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	out, _ := e.s.ShareCodes(ctx, map[string]string{"a": linkA, "b": linkB})
	e.s.SetClientConfigEnabled(ctx, true)
	rid, _ := e.s.RouterID(ctx)

	res, err := e.s.Share(ctx, "a", out["a"].Gen, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != ShareFormat || res.V != ShareVersion || res.RouterID != rid || res.SID != "a" || res.Gen != out["a"].Gen || res.ExportedAt != 1760000000000 {
		t.Fatalf("响应头: %+v", res)
	}
	plain, err := OpenShare(res.Seal, out["a"].PairCode, rid, "a", res.Gen, testIter, testIter)
	if err != nil {
		t.Fatalf("配对码应能解开: %v", err)
	}
	var p sharePlain
	if err := json.Unmarshal(plain, &p); err != nil || p.Link != linkA {
		t.Fatalf("明文: %v %+v", err, p)
	}
	body, _ := json.Marshal(res)
	if strings.Contains(string(body), out["a"].PairCode) || strings.Contains(string(body), "203.0.113.7") {
		t.Fatal("响应里不能出现配对码或明文链接")
	}
	// 取回不改状态(不动本地分流的 lastSyncAt)
	if is, _ := e.s.ClientConfig(ctx); is.LastSyncAt != 0 {
		t.Fatalf("取回共享服务器不该更新本地分流的同步时刻: %d", is.LastSyncAt)
	}
}

func TestShareRetrieveRejects(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	out, _ := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	e.s.SetClientConfigEnabled(ctx, true)
	good := out["a"].Gen
	for name, c := range map[string][2]string{
		"代数不对":     {"a", "0000000000000002"},
		"代数格式不对":   {"a", "xyz"},
		"代数空":      {"a", ""},
		"没出过码的服务器": {"b", good},
		"不认识的服务器":  {"nope", good},
		"id 不合格":   {"a/../b", good},
		"id 空":     {"", good},
	} {
		if _, err := e.s.Share(ctx, c[0], c[1], "10.0.0.3"); !errors.Is(err, ErrSyncNotFound) {
			t.Errorf("%s 应是 ErrSyncNotFound: %v", name, err)
		}
	}
}

func TestShareRetrieveRateLimit(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	out, _ := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
	e.s.SetClientConfigEnabled(ctx, true)
	for i := 0; i < syncFreeFails; i++ {
		if _, err := e.s.Share(ctx, "a", "0000000000000002", "10.0.0.4"); !errors.Is(err, ErrSyncNotFound) {
			t.Fatalf("第 %d 次: %v", i, err)
		}
	}
	var le *LimitedError
	if _, err := e.s.Share(ctx, "a", out["a"].Gen, "10.0.0.4"); !errors.As(err, &le) {
		t.Fatalf("连续失败后即使这次对了也该被锁: %v", err)
	}
	// 别的来源不受影响
	if _, err := e.s.Share(ctx, "a", out["a"].Gen, "10.0.0.5"); err != nil {
		t.Fatalf("别的来源不该被锁: %v", err)
	}
}

func TestShareCodesConcurrentFirstIssue(t *testing.T) {
	e := shareSvc(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	res := make([]map[string]ShareSeal, 8)
	for i := range res {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.s.ShareCodes(ctx, map[string]string{"a": linkA})
			if err != nil {
				t.Error(err)
			}
			res[i] = r
		}()
	}
	wg.Wait()
	for i := 1; i < len(res); i++ {
		if res[i]["a"] != res[0]["a"] {
			t.Fatalf("并发的第一次出码应得到同一份: %+v vs %+v", res[i]["a"], res[0]["a"])
		}
	}
}
