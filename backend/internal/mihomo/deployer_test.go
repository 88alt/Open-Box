package mihomo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// ---------- 假件 ----------

type fakeSvc struct {
	mu      sync.Mutex
	running bool
	calls   []string
	// failAction 指定的动作返回失败
	failAction string
	// afterStart 在 start/restart 成功后被调用(用来模拟内核起来 / 起不来)
	afterStart func()
}

func (f *fakeSvc) Status(context.Context, string) (system.ServiceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return system.ServiceInfo{Running: f.running}, nil
}

func (f *fakeSvc) Do(_ context.Context, _, action string) (system.ActionResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, action)
	fail := action == f.failAction
	after := f.afterStart
	if !fail && (action == "start" || action == "restart") {
		f.running = true
	} else if !fail && action == "stop" {
		f.running = false
	}
	f.mu.Unlock()
	if fail {
		return system.ActionResult{Code: 1, Stderr: "boom"}, nil
	}
	if after != nil && (action == "start" || action == "restart") {
		after()
	}
	return system.ActionResult{OK: true, Code: 0}, nil
}

func (f *fakeSvc) callList() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

type fakeBuild struct {
	res *BuildResult
	err error
}

func (b fakeBuild) BuildMihomo(context.Context) (*BuildResult, error) { return b.res, b.err }

func testResult(addr, secret string) *BuildResult {
	m := NewOrdered()
	m.Set("external-controller", addr)
	m.Set("secret", secret)
	m.Set("mode", "rule")
	return &BuildResult{Config: m}
}

// newDep 造一个部署器:配置目录用临时目录,mihomo 可执行文件用假脚本(永远 `-t` 通过)。
// fakeExe 造一个假的可执行文件:按 exit 退出。Windows 上只有 .bat 能被 exec 执行,
// 所以按 runtime.GOOS 选形态(CI 是 Linux、本机是 Windows,两边都要能跑)。
func fakeExe(t *testing.T, dir string, exit int, stderr string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "mihomo.bat")
		body := "@echo off\r\n"
		if stderr != "" {
			body += "echo " + stderr + " 1>&2\r\n"
		}
		body += "exit /b " + strconv.Itoa(exit) + "\r\n"
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := filepath.Join(dir, "mihomo")
	body := "#!/bin/sh\n"
	if stderr != "" {
		body += "echo '" + stderr + "' >&2\n"
	}
	body += "exit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// newDep 造一个部署器:配置目录用临时目录,mihomo 可执行文件用假件(永远 `-t` 通过)。
func newDep(t *testing.T, svc *fakeSvc, res *BuildResult) *Deployer {
	t.Helper()
	dir := t.TempDir()
	return &Deployer{
		Build:         fakeBuild{res: res},
		Paths:         ArtifactPaths{Config: filepath.Join(dir, "mihomo.yaml")},
		Mihomo:        fakeExe(t, dir, 0, ""),
		Service:       svc,
		ServiceName:   "openbox",
		HealthTimeout: 80 * time.Millisecond,
		StableFor:     5 * time.Millisecond,
		Poll:          5 * time.Millisecond,
	}
}

// fakeController 起一个假的 mihomo 控制接口,带 secret 校验。
func fakeController(t *testing.T, secret string) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret != "" && r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":true,"version":"v1.19.34-openbox.22"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

// ---------- 流程 ----------

// 服务没在跑:启动 → 控制接口 200 → 成功;配置写成 mihomo.yaml 且权限 0600。
func TestMihomoDeployFirstTime(t *testing.T) {
	srv, addr := fakeController(t, "s3cret")
	_ = srv
	svc := &fakeSvc{}
	d := newDep(t, svc, testResult(addr, "s3cret"))
	res := d.Deploy(context.Background())
	if !res.OK || res.Stage != MihomoStageRunning || res.BadTags == nil {
		t.Fatalf("%+v", res)
	}
	if svc.callList() != "start" {
		t.Fatalf("调用:%s", svc.callList())
	}
	b, err := os.ReadFile(d.Paths.Config)
	if err != nil || !strings.Contains(string(b), "external-controller") {
		t.Fatalf("配置没写对:%v %s", err, b)
	}
	// Windows 上没有 0600 权限位(文件权限是另一套机制),这条只在类 Unix 上判;
	// CI(Linux)会走到,本机(Windows)跳过。与仓库里其它权限类断言同一口径。
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(d.Paths.Config); fi.Mode().Perm() != 0o600 {
			t.Fatalf("含 secret 的配置必须 0600:%v", fi.Mode())
		}
	}
	if _, err := os.Stat(d.PrevPath()); !os.IsNotExist(err) {
		t.Fatalf("第一次部署没有旧配置可备份:%v", err)
	}
	if _, err := os.Stat(d.Paths.Config + ".candidate"); !os.IsNotExist(err) {
		t.Fatalf("临时校验文件要删掉:%v", err)
	}
}

// 校验不过(-t 失败)时什么都不碰。
func TestMihomoDeployCheckFailsTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSvc{}
	d := &Deployer{
		Build:         fakeBuild{res: testResult("127.0.0.1:1", "x")},
		Paths:         ArtifactPaths{Config: filepath.Join(dir, "mihomo.yaml")},
		Mihomo:        fakeExe(t, dir, 1, "bad config"),
		Service:       svc,
		ServiceName:   "openbox",
		HealthTimeout: 50 * time.Millisecond,
		StableFor:     time.Millisecond,
		Poll:          time.Millisecond,
	}
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != MihomoStageValidate || !strings.Contains(res.Message, "bad config") {
		t.Fatalf("%+v", res)
	}
	if svc.callList() != "" {
		t.Fatalf("校验失败不该动服务:%s", svc.callList())
	}
	if _, err := os.Stat(d.Paths.Config); !os.IsNotExist(err) {
		t.Fatalf("不该写正式配置:%v", err)
	}
}

// 健康检查不过(控制接口 401,正是真机那次问题的形状)要回滚,并重启回旧配置。
func TestMihomoDeployVerifyFailsRollsBack(t *testing.T) {
	_, goodAddr := fakeController(t, "old-secret")
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer badSrv.Close()
	badAddr := strings.TrimPrefix(badSrv.URL, "http://")

	svc := &fakeSvc{running: true}
	d := newDep(t, svc, testResult(badAddr, "new-secret"))
	// 先放一份「旧配置」,它的控制接口是好的
	old := testResult(goodAddr, "old-secret")
	oldText, _ := MarshalYAML(old.Config)
	if err := os.WriteFile(d.Paths.Config, oldText, 0o600); err != nil {
		t.Fatal(err)
	}

	res := d.Deploy(context.Background())
	if res.OK || res.Stage != MihomoStageVerify || !res.RolledBack {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Message, "401") {
		t.Fatalf("原因里应有 401:%s", res.Message)
	}
	// 回滚后配置必须是旧的
	got, _ := os.ReadFile(d.Paths.Config)
	if !strings.Contains(string(got), "old-secret") {
		t.Fatalf("应回滚到旧配置:%s", got)
	}
	// 旧配置重启后再健康 → 调用序列是 restart(新)、restart(旧)
	if c := svc.callList(); c != "restart,restart" {
		t.Fatalf("调用序列:%s", c)
	}
	if !strings.Contains(res.Message, "已回滚到上一份配置") {
		t.Fatalf("应说明回滚:%s", res.Message)
	}
}

// 启动失败也要回滚。
func TestMihomoDeployStartFailsRollsBack(t *testing.T) {
	_, addr := fakeController(t, "s")
	svc := &fakeSvc{running: true, failAction: "restart"}
	d := newDep(t, svc, testResult(addr, "s"))
	oldText, _ := MarshalYAML(testResult(addr, "s").Config)
	if err := os.WriteFile(d.Paths.Config, oldText, 0o600); err != nil {
		t.Fatal(err)
	}
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != MihomoStageStart || !res.RolledBack {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Message, "boom") {
		t.Fatalf("要带上原因:%s", res.Message)
	}
}

// 生成失败走 validate,不碰任何东西。
func TestMihomoDeployBuildFails(t *testing.T) {
	svc := &fakeSvc{}
	d := newDep(t, svc, nil)
	d.Build = fakeBuild{err: errors.New("装配坏了")}
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != MihomoStageValidate || !strings.Contains(res.Message, "装配坏了") {
		t.Fatalf("%+v", res)
	}
	if svc.callList() != "" {
		t.Fatalf("不该动服务:%s", svc.callList())
	}
}

// 并发第二次要回 Busy,不进响应体。
func TestMihomoDeployBusy(t *testing.T) {
	_, addr := fakeController(t, "s")
	svc := &fakeSvc{}
	d := newDep(t, svc, testResult(addr, "s"))
	// 占住锁
	d.mu.Lock()
	res := d.Deploy(context.Background())
	d.mu.Unlock()
	if !res.Busy {
		t.Fatalf("第二次应回 Busy:%+v", res)
	}
}
