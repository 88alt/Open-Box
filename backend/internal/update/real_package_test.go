package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 这组测试用**真实构建出来的发布包**跑一遍 update/run 的后半段:
// 校验清单与 SHA256 → 解包到暂存目录 → 提交(换位)→ 回滚回原样。
//
// 以前的提交/回滚测试都是在临时目录里手搓文件结构,验的是算法;
// 这里验的是「我们自己 build-package.sh 打出来的目录布局能被 extract 认」,
// 也就是包格式与解包器之间没有对不上。包由环境变量指定,没有就跳过。
//
// 用法(本地打完包后):
//
//	OPENBOX_REAL_PACKAGE=/path/to/openbox-vX-linux-amd64.tar.gz go test ./internal/update/ -run TestRealPackage
func TestRealPackageRoundTrip(t *testing.T) {
	pkg := os.Getenv("OPENBOX_REAL_PACKAGE")
	if pkg == "" {
		t.Skip("没有指定 OPENBOX_REAL_PACKAGE(真实发布包路径)")
	}
	if _, err := os.Stat(pkg); err != nil {
		t.Fatalf("发布包读不到:%v", err)
	}

	// 模拟一台已经装了旧版本的机器:/opt/open-box 里放一份旧文件
	root := t.TempDir()
	old := map[string]string{
		"bin/openbox-backend":     "OLD-BACKEND",
		"bin/openbox-core":        "OLD-CORE",
		"panel/dist/index.html":   "<html>OLD</html>",
		"data/state/profile.json": `{"old":true}`,
		"etc/router-kernel":       "mihomo",
	}
	for rel, body := range old {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 1. 解包(边读边算 sha256、边校验清单)
	staging := filepath.Join(t.TempDir(), "staging")
	f, err := os.Open(pkg)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	res, err := Extract(context.Background(), f, st.Size(), staging, ExtractHooks{})
	if err != nil {
		t.Fatalf("解包失败:%v", err)
	}
	if res.Manifest.Version == "" || res.Manifest.Arch != "amd64" {
		t.Fatalf("清单没读对:%+v", res.Manifest)
	}
	// 包里必须有这几个关键文件,解出来的也得在
	for _, rel := range []string{
		"bin/openbox-backend", "bin/openbox-core", "bin/openbox-backend-run",
		"panel/dist/index.html", "manifest.json",
	} {
		if _, err := os.Stat(filepath.Join(res.Root, rel)); err != nil {
			t.Fatalf("解包后缺 %s:%v", rel, err)
		}
	}

	// 2. 提交:把暂存目录里的文件换到位
	journal := filepath.Join(t.TempDir(), "journal.json")
	stateDir := filepath.Join(root, "data", "state")
	jr, err := Commit(context.Background(), CommitRequest{
		Root:     root,
		Staging:  res.Root,
		Journal:  journal,
		StateDir: stateDir,
		From:     "v0.0.1-old",
		To:       res.Manifest.Version,
		Now:      time.Unix(1_800_000_000, 0).UTC(),
	}, Ops{})
	if err != nil {
		t.Fatalf("提交失败:%v", err)
	}
	// 提交换位完成后状态推进到 restarting(等重启并自检),各目录都要标记完成
	if jr == nil || jr.Status != JournalRestarting {
		t.Fatalf("提交后日志状态应为 restarting:%+v", jr)
	}
	for _, e := range jr.Entries {
		if !e.Done {
			t.Fatalf("%s 没换位完成:%+v", e.Path, jr)
		}
	}

	// 新文件已经就位:后端不再是旧内容,且旧文件留了 .old
	got, err := os.ReadFile(filepath.Join(root, "bin/openbox-backend"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "OLD-BACKEND" {
		t.Fatal("提交后 bin/openbox-backend 还是旧内容,换位没生效")
	}
	// 换位是按目录整块来的:整个 bin 被改成 bin.old,回滚时整块换回去
	if _, err := os.Stat(filepath.Join(root, "bin.old")); err != nil {
		t.Fatalf("旧的 bin 应留成 bin.old 以便回滚:%v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "bin.old", "openbox-backend")); string(b) != "OLD-BACKEND" {
		t.Fatal("bin.old 里应是旧的后端二进制")
	}
	// 用户数据不能被碰
	if b, _ := os.ReadFile(filepath.Join(root, "data/state/profile.json")); string(b) != `{"old":true}` {
		t.Fatal("提交不该动 data/ 下的用户数据")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/router-kernel")); string(b) != "mihomo" {
		t.Fatal("提交不该动 etc/ 下的配置")
	}

	// 3. 回滚:装回旧版本
	cur, err := ReadJournal(journal)
	if err != nil {
		t.Fatalf("回滚前读日志失败:%v", err)
	}
	if err := Rollback(context.Background(), journal, cur, Ops{}); err != nil {
		t.Fatalf("回滚失败:%v", err)
	}
	back, err := os.ReadFile(filepath.Join(root, "bin/openbox-backend"))
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != "OLD-BACKEND" {
		t.Fatalf("回滚后应装回旧文件,实际 %q", string(back))
	}
}
