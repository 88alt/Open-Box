package clientapp

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

var (
	pairRe  = regexp.MustCompile(`^[A-HJ-NP-Z2-9]{8}$`)
	nonceRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func TestClientConfigFirstReadGeneratesAndPersists(t *testing.T) {
	s := newSvc(t, map[string]any{"serverInfo": map[string]any{"name": "客厅"}})
	ctx := context.Background()
	a, err := s.ClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pairRe.MatchString(a.PairCode) || !nonceRe.MatchString(a.Nonce) {
		t.Fatalf("格式不对: %+v", a)
	}
	if a.Enabled || a.LastSyncAt != 0 {
		t.Fatalf("开关默认关、从没同步过: %+v", a)
	}
	if a.RouterName != "客厅" || a.PanelPort != 3036 || len(a.LanAddresses) != 1 || !validRouterID(a.RouterID) {
		t.Fatalf("路由器信息: %+v", a)
	}
	// 再读、换实例(模拟重启)读:不变
	b, _ := s.ClientConfig(ctx)
	c, _ := (&Service{Store: s.Store, Profile: s.Profile}).ClientConfig(ctx)
	if a.PairCode != b.PairCode || a.Nonce != b.Nonce || a.PairCode != c.PairCode || a.Nonce != c.Nonce {
		t.Fatalf("两次读到不同的码: %+v %+v %+v", a, b, c)
	}
	// 路由器 ID 与配置状态共用一个文件,互不覆盖
	if id, _ := s.RouterID(ctx); id != a.RouterID {
		t.Fatalf("路由器 ID 变了: %s %s", id, a.RouterID)
	}
}

func TestSetClientConfigEnabled(t *testing.T) {
	s := newSvc(t, nil)
	ctx := context.Background()
	before, _ := s.ClientConfig(ctx)
	on, err := s.SetClientConfigEnabled(ctx, true)
	if err != nil || !on {
		t.Fatalf("on=%v err=%v", on, err)
	}
	after, _ := s.ClientConfig(ctx)
	if !after.Enabled || after.PairCode != before.PairCode || after.Nonce != before.Nonce {
		t.Fatalf("开关不该动码: %+v %+v", before, after)
	}
	if on, _ := s.SetClientConfigEnabled(ctx, true); !on { // 重复设同一个值没问题
		t.Fatal("重复打开")
	}
	off, _ := s.SetClientConfigEnabled(ctx, false)
	if off {
		t.Fatal("应已关闭")
	}
	if c, _ := s.ClientConfig(ctx); c.Enabled {
		t.Fatal("重读应仍是关")
	}
}

// 没先读过就直接设开关:码也要补齐,不能存出一个没有码的开着的状态。
func TestSetEnabledBeforeFirstRead(t *testing.T) {
	s := newSvc(t, nil)
	ctx := context.Background()
	if _, err := s.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	c, _ := s.ClientConfig(ctx)
	if !c.Enabled || !pairRe.MatchString(c.PairCode) || !nonceRe.MatchString(c.Nonce) {
		t.Fatalf("%+v", c)
	}
}

func TestRotateClientConfig(t *testing.T) {
	s := newSvc(t, nil)
	ctx := context.Background()
	a, _ := s.ClientConfig(ctx)
	s.SetClientConfigEnabled(ctx, true)
	// 模拟 App 同步过一次
	var st state
	if err := s.Store.Update(stateFile, &st, func() error { st.Config.LastSyncAt = 1700000000000; return nil }); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ClientConfig(ctx); c.LastSyncAt != 1700000000000 {
		t.Fatalf("最后同步时刻没存住: %+v", c)
	}
	b, err := s.RotateClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.PairCode == a.PairCode || b.Nonce == a.Nonce || !pairRe.MatchString(b.PairCode) || !nonceRe.MatchString(b.Nonce) {
		t.Fatalf("应换新码: %+v → %+v", a, b)
	}
	if !b.Enabled || b.LastSyncAt != 0 || b.RouterID != a.RouterID {
		t.Fatalf("开关保持、同步时刻清零、路由器 ID 不变: %+v", b)
	}
	// 换完的值落盘了
	if c, _ := s.ClientConfig(ctx); c.PairCode != b.PairCode || c.Nonce != b.Nonce || c.LastSyncAt != 0 {
		t.Fatalf("没落盘: %+v", c)
	}
	// 连换几次,每次都和上一次不同
	prev := b.PairCode
	for i := 0; i < 5; i++ {
		n, _ := s.RotateClientConfig(ctx)
		if n.PairCode == prev {
			t.Fatal("连着两次一样")
		}
		prev = n.PairCode
	}
}

// 随机源给不出和旧码不同的码(这里是常数源):报错,旧码原样保留。
func TestRotateFailureKeepsOld(t *testing.T) {
	s := newSvc(t, nil)
	ctx := context.Background()
	s.Rand = constReader{}
	a, err := s.ClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateClientConfig(ctx); err == nil {
		t.Fatal("换不出不同的码应报错")
	}
	if b, _ := s.ClientConfig(ctx); b.PairCode != a.PairCode || b.Nonce != a.Nonce {
		t.Fatalf("失败后旧码要原样: %+v %+v", a, b)
	}
	// 随机源不够长也是报错
	s.Rand = bytes.NewReader([]byte("ab"))
	if _, err := s.RotateClientConfig(ctx); err == nil {
		t.Fatal("随机源不够应报错")
	}
	s.Rand = nil
	if b, _ := s.ClientConfig(ctx); b.PairCode != a.PairCode {
		t.Fatalf("失败后旧码要原样: %+v", b)
	}
}

type constReader struct{}

func (constReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 7
	}
	return len(p), nil
}

// 手工改坏的文件:坏的码补成新的,开关和同步时刻不丢。
func TestClientConfigRepairsBadStoredValues(t *testing.T) {
	s := newSvc(t, nil)
	if err := s.Store.Save(stateFile, map[string]any{"clientConfig": map[string]any{
		"enabled": true, "pairCode": "abc", "nonce": "zzzz", "lastSyncAt": 5,
	}}); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClientConfig(context.Background())
	if err != nil || !pairRe.MatchString(c.PairCode) || !nonceRe.MatchString(c.Nonce) || !c.Enabled || c.LastSyncAt != 5 {
		t.Fatalf("%+v %v", c, err)
	}
}

// 读不到档案(路由器名称要从档案取):整个调用报错,什么都不写。
func TestClientConfigProfileFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: st, Profile: func(context.Context) (map[string]any, error) { return nil, errors.New("档案坏了") }}
	if _, err := s.ClientConfig(context.Background()); err == nil || !strings.Contains(err.Error(), "档案坏了") {
		t.Fatalf("err=%v", err)
	}
	if _, err := s.RotateClientConfig(context.Background()); err == nil {
		t.Fatal("换码也应报错")
	}
	if _, err := s.SetClientConfigEnabled(context.Background(), true); err == nil {
		t.Fatal("开关也应报错")
	}
	// 路由器 ID 在读档案之前就生成并保存了(原有行为,不变);但配对码、开关一概不能写
	var got state
	if _, err := st.Load(stateFile, &got); err != nil {
		t.Fatal(err)
	}
	if got.Config != nil {
		t.Fatalf("报错时不该写配对码或开关: %+v", got.Config)
	}
}

func TestClientConfigConcurrentFirstRead(t *testing.T) {
	s := newSvc(t, nil)
	var wg sync.WaitGroup
	codes := make([]string, 20)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := s.ClientConfig(context.Background())
			codes[i] = c.PairCode
		}()
	}
	wg.Wait()
	for _, c := range codes {
		if c == "" || c != codes[0] {
			t.Fatalf("并发首读拿到不同的码: %v", codes)
		}
	}
}

func TestPairCodeDistribution(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 400; i++ {
		c, err := newPairCode(nil)
		if err != nil || !pairRe.MatchString(c) {
			t.Fatalf("%q %v", c, err)
		}
		for j := 0; j < len(c); j++ {
			seen[c[j]] = true
		}
	}
	if len(seen) != len(pairCodeAlphabet) {
		t.Fatalf("字符表里的 %d 个字符只出现了 %d 个", len(pairCodeAlphabet), len(seen))
	}
}
