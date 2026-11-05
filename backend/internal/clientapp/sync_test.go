package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func syncSvc(t *testing.T) (*Service, Issue, *time.Time) {
	t.Helper()
	s := fileSvc(t)
	clock := time.UnixMilli(1760000000000)
	s.Now = func() time.Time { return clock }
	ctx := context.Background()
	if _, err := s.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	is, err := s.ClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, is, &clock
}

func TestSyncSuccessOpensAndRecordsLastSync(t *testing.T) {
	s, is, _ := syncSvc(t)
	ctx := context.Background()
	if is.LastSyncAt != 0 {
		t.Fatal("从没同步过应是 0")
	}
	res, err := s.Sync(ctx, is.Nonce, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != BundleFormat || res.Version != BundleVersion || res.Router.ID != is.RouterID || res.ExportedAt != 1760000000000 {
		t.Fatalf("响应头: %+v", res)
	}
	plain, err := OpenEnvelope(res.Seal(), is.PairCode, testIter, testIter)
	if err != nil {
		t.Fatalf("配对码应能解开: %v", err)
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil || p.Kind != "client-config" || len(p.ShareRegions) != 3 {
		t.Fatalf("明文: %v %+v", err, p)
	}
	after, _ := s.ClientConfig(ctx)
	if after.LastSyncAt != 1760000000000 || !after.Enabled || after.PairCode != is.PairCode {
		t.Fatalf("成功后 lastSyncAt 应更新、别的不变: %+v", after)
	}
}

func TestSyncDisabledNeverTouchesCrypto(t *testing.T) {
	s, is, _ := syncSvc(t)
	ctx := context.Background()
	s.SetClientConfigEnabled(ctx, false)
	s.keys = keyCache{}       // 清掉缓存:如果被动了密码运算,缓存里就会出现密钥
	for i := 0; i < 30; i++ { // 即使乱试很多次,关着也只回 disabled,不记失败、不限速
		if _, err := s.Sync(ctx, "ffffffffffffffff", "1.2.3.4"); !errors.Is(err, ErrDisabled) {
			t.Fatalf("关着应回 ErrDisabled: %v", err)
		}
	}
	if _, err := s.Sync(ctx, is.Nonce, "1.2.3.4"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("nonce 对也一样: %v", err)
	}
	if s.keys.key != nil {
		t.Fatal("开关关着不该进任何密码运算")
	}
	if got, _ := s.ClientConfig(ctx); got.LastSyncAt != 0 {
		t.Fatal("关着不该更新 lastSyncAt")
	}
}

func TestSyncNeverConfiguredIsDisabled(t *testing.T) {
	s := fileSvc(t) // 从没读过客户端配置:没有配对码,也没有任何文件
	if _, err := s.Sync(context.Background(), "0123456789abcdef", "1.2.3.4"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("%v", err)
	}
}

func TestSyncWrongNonceThenLimited(t *testing.T) {
	s, is, clock := syncSvc(t)
	ctx := context.Background()
	s.keys = keyCache{}
	for i := 0; i < syncFreeFails; i++ {
		if _, err := s.Sync(ctx, "ffffffffffffffff", "9.9.9.9"); !errors.Is(err, ErrSyncNotFound) {
			t.Fatalf("第 %d 次: %v", i, err)
		}
	}
	if s.keys.key != nil {
		t.Fatal("nonce 对不上不该进密码运算")
	}
	// 满额后锁定:这时连对的 nonce 也先被限速挡住(限速在派生之前)
	var le *LimitedError
	if _, err := s.Sync(ctx, is.Nonce, "9.9.9.9"); !errors.As(err, &le) || le.After <= 0 || le.After > syncBaseLock {
		t.Fatalf("应被限速: %v", err)
	}
	if s.keys.key != nil {
		t.Fatal("被限速时不该进密码运算")
	}
	// 别的来源不受影响
	if _, err := s.Sync(ctx, is.Nonce, "9.9.9.8"); err != nil {
		t.Fatalf("别的 IP: %v", err)
	}
	// 锁定过期后恢复
	*clock = clock.Add(syncBaseLock + time.Second)
	if _, err := s.Sync(ctx, is.Nonce, "9.9.9.9"); err != nil {
		t.Fatalf("过期后应恢复: %v", err)
	}
}

func TestSyncLockDoublesAndCaps(t *testing.T) {
	var l syncLimiter
	now := time.UnixMilli(1)
	for i := 0; i < syncFreeFails; i++ {
		l.fail("ip", now)
	}
	if d := l.blocked("ip", now); d != syncBaseLock {
		t.Fatalf("第一次锁 %v", d)
	}
	l.fail("ip", now)
	if d := l.blocked("ip", now); d != 2*syncBaseLock {
		t.Fatalf("第二次锁 %v", d)
	}
	for i := 0; i < 20; i++ {
		l.fail("ip", now)
	}
	if d := l.blocked("ip", now); d != syncMaxLock {
		t.Fatalf("封顶 %v", d)
	}
	// 隔了很久没有新失败,清零
	later := now.Add(syncForget + time.Hour)
	l.fail("ip", later)
	if d := l.blocked("ip", later); d != 0 {
		t.Fatalf("应已清零: %v", d)
	}
}

func TestSyncLimiterBoundedTable(t *testing.T) {
	var l syncLimiter
	now := time.UnixMilli(1)
	for i := 0; i < syncMaxTracked*3; i++ {
		l.fail(string(rune('a'+i%26))+string(rune(i)), now)
	}
	if len(l.entries) > syncMaxTracked {
		t.Fatalf("表不能无限增长: %d", len(l.entries))
	}
}

func TestSyncAfterRotateOldNonceRejected(t *testing.T) {
	s, is, _ := syncSvc(t)
	ctx := context.Background()
	is2, err := s.RotateClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, is.Nonce, "1.1.1.1"); !errors.Is(err, ErrSyncNotFound) {
		t.Fatalf("旧 nonce: %v", err)
	}
	res, err := s.Sync(ctx, is2.Nonce, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEnvelope(res.Seal(), is.PairCode, testIter, testIter); err == nil {
		t.Fatal("旧配对码不该解得开新封包")
	}
	if _, err := OpenEnvelope(res.Seal(), is2.PairCode, testIter, testIter); err != nil {
		t.Fatal(err)
	}
}

func TestSyncDoesNotRestoreLastSyncAfterRotate(t *testing.T) {
	s, is, _ := syncSvc(t)
	ctx := context.Background()
	// 先同步一次,再换码:lastSyncAt 清零,旧 nonce 同步不进来,不会把它写回去
	if _, err := s.Sync(ctx, is.Nonce, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateClientConfig(ctx); err != nil {
		t.Fatal(err)
	}
	s.Sync(ctx, is.Nonce, "1.1.1.1")
	if got, _ := s.ClientConfig(ctx); got.LastSyncAt != 0 {
		t.Fatalf("换码后 lastSyncAt 应是 0: %d", got.LastSyncAt)
	}
}

func TestWarmFillsCacheOnlyWhenPaired(t *testing.T) {
	// salt 每个封包随机,启动时无法预派生;Warm 是空操作。
	s := fileSvc(t)
	s.Warm(context.Background())
	if s.keys.key != nil {
		t.Fatal("Warm 不应派生")
	}
	_, _ = s.ClientConfig(context.Background())
	s.Warm(context.Background())
	if s.keys.key != nil {
		t.Fatal("有配对码时 Warm 仍不派生(等第一次封包)")
	}
}
