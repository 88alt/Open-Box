package kernelswitch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/kernelgen"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func TestSwitchRejectsBadInput(t *testing.T) {
	d := SwitchDeps{}
	if r := d.Switch(context.Background(), "openbox"); r.OK || !strings.Contains(r.Stderr, "不支持") {
		t.Fatalf("r = %+v", r)
	}
	if r := d.Switch(context.Background(), "mihomo"); r.OK || !strings.Contains(r.Stderr, "没有配置完整") {
		t.Fatalf("r = %+v", r)
	}
}

// 预检没过就不动任何文件、不碰服务。
func TestSwitchStopsOnPreflightFailure(t *testing.T) {
	dir := t.TempDir()
	kf := filepath.Join(dir, "router-kernel")
	if err := os.WriteFile(kf, []byte("mihomo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &fakeService{}
	d := SwitchDeps{
		Engine:   New(Deps{KernelFile: kf, Platform: "openwrt"}), // 目标=当前,预检必失败
		Gen:      kernelgen.New(kernelgen.Deps{}),
		Sbx:      &singbox.Generator{},
		Services: svc, ServiceName: "sing-box", KernelFile: kf,
	}
	r := d.Switch(context.Background(), "mihomo")
	if r.OK || !strings.Contains(r.Stderr, "预检没通过") {
		t.Fatalf("r = %+v", r)
	}
	if svc.calls != 0 {
		t.Fatalf("预检失败还动了服务: %d 次", svc.calls)
	}
}

func TestBackupRestore(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.conf")
	if err := os.WriteFile(keep, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.conf")
	d := SwitchDeps{}
	b := d.backup([]string{keep, missing, ""})
	if err := os.WriteFile(keep, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(missing, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(keep); string(got) != "old" {
		t.Errorf("keep = %q", got)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("切换前不存在的文件应当被删掉,err=%v", err)
	}
}

func TestFailedReason(t *testing.T) {
	checks := []Check{
		{ID: "target", Status: StatusPassed},
		{ID: "conflicts", Status: StatusFailed, Details: []string{"Nikki"}},
		{ID: "rulesets", Status: StatusBlocked},
	}
	if got := failedReason(checks); got != "切换前预检没通过:conflicts: Nikki" {
		t.Fatalf("got = %q", got)
	}
	if failedReason([]Check{{ID: "target", Status: StatusPassed}}) != "" {
		t.Fatal("全过时不该有原因")
	}
}

type fakeService struct {
	calls int
	fail  map[string]bool
}

func (f *fakeService) Status(context.Context, string) (system.ServiceInfo, error) {
	return system.ServiceInfo{Running: true, Raw: "running"}, nil
}

func (f *fakeService) Do(_ context.Context, _, action string) (system.ActionResult, error) {
	f.calls++
	if f.fail[action] {
		return system.ActionResult{Code: 1, Stderr: "boom"}, nil
	}
	return system.ActionResult{OK: true}, nil
}
