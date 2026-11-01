package singbox

import (
	"context"
	"errors"
	"testing"
)

func TestExclusiveBlocksDeployWhileRunning(t *testing.T) {
	d := &Deployer{}
	ran := false
	err := d.Exclusive(func() error {
		ran = true
		if r := d.Deploy(context.Background()); !r.Busy {
			t.Errorf("Exclusive 期间部署应被拒绝(Busy): %+v", r)
		}
		if err := d.Exclusive(func() error { t.Error("嵌套的 Exclusive 不该执行"); return nil }); !errors.Is(err, ErrDeployBusy) {
			t.Errorf("嵌套的 Exclusive 应返回 ErrDeployBusy: %v", err)
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("fn 应执行且无错: ran=%v err=%v", ran, err)
	}
	// fn 返回的错误原样带出,锁也释放了
	boom := errors.New("boom")
	if err := d.Exclusive(func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("应带出 fn 的错误: %v", err)
	}
	if r := d.Deploy(context.Background()); r.Busy {
		t.Fatalf("Exclusive 结束后部署不该再是 Busy: %+v", r)
	}
}

func TestExclusiveRefusesWhileDeploying(t *testing.T) {
	d := &Deployer{}
	d.mu.Lock() // 假装有一次部署正在进行
	defer d.mu.Unlock()
	if err := d.Exclusive(func() error { t.Error("部署在进行时不该执行 fn"); return nil }); !errors.Is(err, ErrDeployBusy) {
		t.Fatalf("应返回 ErrDeployBusy: %v", err)
	}
}
