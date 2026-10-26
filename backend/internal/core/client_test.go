package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake 返回一个直接应答固定内容的 Client,并记录收到的 stdin。
func fake(t *testing.T, reply string, opts ...Option) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := &[]string{}
	c := New("unused", opts...)
	c.run = func(_ context.Context, stdin []byte) ([]byte, error) {
		mu.Lock()
		*seen = append(*seen, string(stdin))
		mu.Unlock()
		return []byte(reply), nil
	}
	return c, seen
}

func TestCallSuccessAndRequestShape(t *testing.T) {
	c, seen := fake(t, `{"protocol":1,"ok":true,"value":{"a":[1,2]}}`+"\n")
	raw, err := c.Call(context.Background(), "builtinTags", []any{[]string{}, map[string]string{"k": "<&>"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"a":[1,2]}` {
		t.Fatalf("value = %s", raw)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte((*seen)[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req["protocol"] != float64(1) || req["method"] != "builtinTags" || len(req["args"].([]any)) != 2 {
		t.Fatalf("请求内容不对: %v", req)
	}
	if _, has := req["geoTags"]; has {
		t.Fatal("没传 geoTags 就不应出现该字段")
	}
	if !strings.Contains((*seen)[0], `"<&>"`) {
		t.Fatalf("不应转义 < > &: %s", (*seen)[0])
	}
}

func TestNilArgsIsEmptyArray(t *testing.T) {
	c, seen := fake(t, `{"protocol":1,"ok":true,"value":1}`)
	if _, err := c.Call(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*seen)[0], `"args":[]`) {
		t.Fatalf("args 必须是数组而不是 null: %s", (*seen)[0])
	}
}

func TestGeoTags(t *testing.T) {
	c, seen := fake(t, `{"protocol":1,"ok":true,"value":null}`)
	_, _ = c.Call(context.Background(), "emitGroups", nil, WithGeoTags([]string{"geosite-cn"}))
	_, _ = c.Call(context.Background(), "emitGroups", nil, WithGeoTags(nil))
	if !strings.Contains((*seen)[0], `"geoTags":["geosite-cn"]`) {
		t.Fatal((*seen)[0])
	}
	if !strings.Contains((*seen)[1], `"geoTags":[]`) {
		t.Fatalf("带了但为空的 geoTags 应序列化成 []: %s", (*seen)[1])
	}
}

func TestCoreErrorIsDistinct(t *testing.T) {
	c, _ := fake(t, `{"protocol":1,"ok":false,"error":"出站重名: hk-1"}`)
	_, err := c.Call(context.Background(), "configDetailed", nil)
	var ce *CoreError
	if !errors.As(err, &ce) || ce.Message != "出站重名: hk-1" || ce.Method != "configDetailed" {
		t.Fatalf("应是 *CoreError: %#v", err)
	}
	c, _ = fake(t, `{"protocol":1,"ok":false}`)
	_, err = c.Call(context.Background(), "x", nil)
	if !errors.As(err, &ce) || ce.Message == "" {
		t.Fatalf("没有错误文案时也要给出说明: %v", err)
	}
}

func TestBadOutputs(t *testing.T) {
	cases := map[string]string{
		"空输出":     "",
		"只有空白":    " \n",
		"不是 JSON": "oops",
		"协议版本不对":  `{"protocol":2,"ok":true,"value":1}`,
		"没有协议字段":  `{"ok":true,"value":1}`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := fake(t, out)
			_, err := c.Call(context.Background(), "x", nil)
			var ce *CoreError
			if err == nil || errors.As(err, &ce) {
				t.Fatalf("应是系统错误而不是 CoreError: %v", err)
			}
		})
	}
	c, _ := fake(t, `{"protocol":2,"ok":true}`)
	if _, err := c.Call(context.Background(), "x", nil); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("协议版本不符应是 ErrIncompatible: %v", err)
	}
}

func TestOkWithoutValueIsNull(t *testing.T) {
	c, _ := fake(t, `{"protocol":1,"ok":true}`)
	raw, err := c.Call(context.Background(), "x", nil)
	if err != nil || string(raw) != "null" {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestCallInto(t *testing.T) {
	c, _ := fake(t, `{"protocol":1,"ok":true,"value":{"direct":"DIRECT","directEnabled":true}}`)
	var out struct {
		Direct        string `json:"direct"`
		DirectEnabled bool   `json:"directEnabled"`
	}
	if err := c.CallInto(context.Background(), "builtinTags", nil, &out); err != nil || out.Direct != "DIRECT" || !out.DirectEnabled {
		t.Fatalf("%+v %v", out, err)
	}
	var wrong []int
	if err := c.CallInto(context.Background(), "builtinTags", nil, &wrong); err == nil {
		t.Fatal("类型不符应报错")
	}
}

func TestInputTooLargeDoesNotSpawn(t *testing.T) {
	c := New("unused")
	called := false
	c.run = func(context.Context, []byte) ([]byte, error) { called = true; return nil, nil }
	huge := strings.Repeat("x", MaxInputBytes)
	if _, err := c.Call(context.Background(), "x", []any{huge}); !errors.Is(err, ErrInputTooLarge) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestTimeout(t *testing.T) {
	c := New("unused", WithTimeout(50*time.Millisecond))
	c.run = func(ctx context.Context, _ []byte) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	start := time.Now()
	_, err := c.Call(context.Background(), "slow", nil)
	if err == nil || !strings.Contains(err.Error(), "超时") || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestCallerCancel(t *testing.T) {
	c := New("unused")
	c.run = func(ctx context.Context, _ []byte) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if _, err := c.Call(ctx, "slow", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled: %v", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	c := New("unused", WithMaxConcurrent(2))
	var cur, peak atomic.Int32
	c.run = func(context.Context, []byte) ([]byte, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return []byte(`{"protocol":1,"ok":true,"value":1}`), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Call(context.Background(), "x", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() != 2 {
		t.Fatalf("同时运行的进程数峰值应为 2, 得到 %d", peak.Load())
	}
}

func TestCancelWhileWaitingForSlot(t *testing.T) {
	c := New("unused", WithMaxConcurrent(1))
	release := make(chan struct{})
	c.run = func(context.Context, []byte) ([]byte, error) {
		<-release
		return []byte(`{"protocol":1,"ok":true,"value":1}`), nil
	}
	go func() { _, _ = c.Call(context.Background(), "hold", nil) }()
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "wait", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("排队等待时被取消应立即返回: %v", err)
	}
	close(release)
}

func TestHello(t *testing.T) {
	good, _ := fake(t, `{"protocol":1,"ok":true,"value":{"implementation":"go-native","protocol":1,"buildID":"anything"}}`)
	if info, err := good.Hello(context.Background()); err != nil || info.BuildID != "anything" {
		t.Fatalf("D5:不校验 buildID: %+v %v", info, err)
	}
	for name, reply := range map[string]string{
		"别的实现":   `{"protocol":1,"ok":true,"value":{"implementation":"node","protocol":1}}`,
		"协议版本不同": `{"protocol":1,"ok":true,"value":{"implementation":"go-native","protocol":2}}`,
	} {
		c, _ := fake(t, reply)
		if _, err := c.Hello(context.Background()); !errors.Is(err, ErrIncompatible) {
			t.Errorf("%s: 应是 ErrIncompatible: %v", name, err)
		}
	}
}

func TestMethodsList(t *testing.T) {
	if len(Methods) != 15 {
		t.Fatalf("核心有 15 个方法, 清单里是 %d 个", len(Methods))
	}
	seen := map[string]bool{}
	for _, m := range Methods {
		if seen[m] || m == "hello" {
			t.Fatalf("重复或多余: %s", m)
		}
		seen[m] = true
	}
}

func TestLimitedAndTailBuffers(t *testing.T) {
	lb := &limitedBuffer{limit: 5}
	if _, err := lb.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.Write([]byte("def")); err == nil || !lb.exceeded {
		t.Fatal("超限应报错并标记")
	}
	tb := &tailBuffer{keep: 4}
	_, _ = tb.Write([]byte("hello "))
	_, _ = tb.Write([]byte("world"))
	if tb.String() != "orld" {
		t.Fatalf("只保留末尾: %q", tb.String())
	}
}

func TestAvailable(t *testing.T) {
	if Available("definitely-not-a-real-binary-xyz") {
		t.Fatal("不存在的程序不应可用")
	}
	exe, _ := os.Executable()
	if !Available(exe) {
		t.Fatal("当前测试程序应可用")
	}
	if Available(t.TempDir()) {
		t.Fatal("目录不应算可执行文件")
	}
}
