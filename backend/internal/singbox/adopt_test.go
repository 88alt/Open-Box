package singbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// AdoptBaseline 是「换实现接管」时的基线对齐:官方面板(或手动)部署过、自研后端没记过指纹时,
// 不这样做一启动就会报一堆「有改动要重启内核」(2026-11-27 真机现象:官方面板角标 0,
// 换自研后端立刻变 2,applied 记录停在 11-26、mihomo.yaml 是 16:15 的)。
// fixedFingerprint 是 FingerprintSource 的测试替身:永远返回同一份指纹。
type fixedFingerprint Fingerprint

func (f fixedFingerprint) Fingerprint(context.Context) (Fingerprint, error) {
	return Fingerprint(f), nil
}

func newAppliedForTest(t *testing.T, fp Fingerprint) (*Applied, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	deployed := filepath.Join(dir, "mihomo.yaml")
	if err := os.WriteFile(deployed, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Applied{Store: st, Source: fixedFingerprint(fp)}
	return a, deployed
}

// 没有记录(自研后端从没部署过):把现状当基线,避免一开始就报全部改动。
func TestAdoptBaselineWithoutRecord(t *testing.T) {
	a, deployed := newAppliedForTest(t, Fingerprint{"d:nodes_and_groups": "abc"})
	adopted, err := a.AdoptBaseline(context.Background(), deployed)
	if err != nil {
		t.Fatal(err)
	}
	if !adopted {
		t.Fatal("没有记录时应接管现状当基线")
	}
	// 接管之后再算 pending,应该没有待重启
	got, err := a.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending {
		t.Fatalf("接管后不该报待重启: %+v", got)
	}
}

// 配置文件比记录新:说明中间被外部(官方面板)部署过,接管现状。
func TestAdoptBaselineWhenDeployedFileIsNewer(t *testing.T) {
	a, deployed := newAppliedForTest(t, Fingerprint{"d:nodes_and_groups": "new"})
	// 先写一条很旧的记录
	old := time.Now().Add(-24 * time.Hour)
	a.Now = func() time.Time { return old }
	if err := a.Record(Fingerprint{"d:nodes_and_groups": "old"}); err != nil {
		t.Fatal(err)
	}
	// 部署文件比记录新(刚写的)
	if err := os.Chtimes(deployed, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	adopted, err := a.AdoptBaseline(context.Background(), deployed)
	if err != nil {
		t.Fatal(err)
	}
	if !adopted {
		t.Fatal("配置文件比记录新时应接管现状")
	}
}

// 记录比配置文件新(自研后端自己部署的):不要动基线,否则会把真正的改动也抹掉。
func TestAdoptBaselineKeepsRecordWhenUpToDate(t *testing.T) {
	a, deployed := newAppliedForTest(t, Fingerprint{"d:nodes_and_groups": "current"})
	// 部署文件改到很久以前,记录是现在
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(deployed, past, past); err != nil {
		t.Fatal(err)
	}
	if err := a.Record(Fingerprint{"d:nodes_and_groups": "current"}); err != nil {
		t.Fatal(err)
	}

	adopted, err := a.AdoptBaseline(context.Background(), deployed)
	if err != nil {
		t.Fatal(err)
	}
	if adopted {
		t.Fatal("记录比配置文件新时不该改基线")
	}
	// 指纹一致,pending 仍是 false
	got, err := a.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending {
		t.Fatalf("不该报待重启: %+v", got)
	}
}

// 配置文件名传空 / 文件不存在:推不出结论就不动(不猜)。
func TestAdoptBaselineNoFile(t *testing.T) {
	a, _ := newAppliedForTest(t, Fingerprint{"d:x": "1"})
	for _, f := range []string{"", filepath.Join(t.TempDir(), "不存在.yaml")} {
		adopted, err := a.AdoptBaseline(context.Background(), f)
		if err != nil || adopted {
			t.Fatalf("%q: adopted=%v err=%v,应当什么都不做", f, adopted, err)
		}
	}
}
