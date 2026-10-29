package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// ---------- 假的服务管理与配置生成 ----------

type fakeSvc struct {
	mu        sync.Mutex
	running   bool
	calls     []string
	statusErr error
	// doFn 非空时先问它:返回 handled=true 就用它的结果,不改 running
	doFn func(action string, n int) (res system.ActionResult, err error, handled bool)
	// alive 非空时覆盖 Status 的 Running(参数是第几次 Status)
	alive   func(n int) bool
	statusN int
}

func (f *fakeSvc) Status(ctx context.Context, name string) (system.ServiceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusN++
	if f.statusErr != nil {
		return system.ServiceInfo{}, f.statusErr
	}
	r := f.running
	if f.alive != nil {
		r = f.alive(f.statusN)
	}
	return system.ServiceInfo{Running: r}, nil
}

func (f *fakeSvc) Do(ctx context.Context, name, action string) (system.ActionResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, action)
	n := len(f.calls)
	do := f.doFn
	f.mu.Unlock()
	if do != nil {
		if res, err, handled := do(action, n); handled {
			return res, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = action != "stop"
	return system.ActionResult{OK: true}, nil
}

func (f *fakeSvc) callList() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

type fakeBuild struct {
	built *Built
	err   error
	gate  chan struct{} // 非空时 Build 等它关闭
}

func (b *fakeBuild) Build(ctx context.Context) (*Built, error) {
	if b.gate != nil {
		<-b.gate
	}
	return b.built, b.err
}

func markerHealth(ctx context.Context, cfg map[string]any) error {
	if cfg["marker"] == "bad" {
		return errors.New("控制接口还连不上")
	}
	return nil
}

func newDP(t *testing.T, cfg map[string]any, svc *fakeSvc) *Deployer {
	t.Helper()
	return &Deployer{
		Build:         &fakeBuild{built: &Built{Config: cfg}},
		SingBox:       fakeSingBox(t),
		ConfigDir:     t.TempDir(),
		Service:       svc,
		ServiceName:   "sing-box",
		Health:        markerHealth,
		HealthTimeout: 60 * time.Millisecond, StableFor: 5 * time.Millisecond, Poll: 2 * time.Millisecond,
	}
}

func writeCfg(t *testing.T, d *Deployer, marker string) []byte {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"marker": marker})
	if err := os.WriteFile(d.configPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func readCfg(t *testing.T, d *Deployer) []byte {
	t.Helper()
	b, err := os.ReadFile(d.configPath())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func marker(b []byte) string {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	s, _ := m["marker"].(string)
	return s
}

// ---------- 流程 ----------

func TestDeployFirstTime(t *testing.T) {
	svc := &fakeSvc{}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	res := d.Deploy(context.Background())
	if !res.OK || res.Stage != StageRunning || res.BadTags == nil {
		t.Fatalf("%+v", res)
	}
	if marker(readCfg(t, d)) != "new" || svc.callList() != "start" {
		t.Fatalf("配置 / 调用不对: %s %s", readCfg(t, d), svc.callList())
	}
	if fi, _ := os.Stat(d.configPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("配置里有密钥,权限必须 0600: %v", fi.Mode())
	}
	if _, err := os.Stat(d.PrevPath()); !os.IsNotExist(err) {
		t.Fatalf("第一次部署没有旧配置可备份: %v", err)
	}
}

func TestDeployReplacesAndKeepsBackup(t *testing.T) {
	svc := &fakeSvc{running: true}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if !res.OK || svc.callList() != "restart" || marker(readCfg(t, d)) != "new" {
		t.Fatalf("%+v %s", res, svc.callList())
	}
	prev, err := os.ReadFile(d.PrevPath())
	if err != nil || !bytes.Equal(prev, old) {
		t.Fatalf("旧配置应备份: %s %v", prev, err)
	}
	if !strings.HasSuffix(d.PrevPath(), "config.prev.json") {
		t.Fatal(d.PrevPath())
	}
}

func TestDeployValidateFailureTouchesNothing(t *testing.T) {
	svc := &fakeSvc{running: true}
	d := newDP(t, map[string]any{"marker": "BAD"}, svc) // 假 sing-box 见到 BAD 就校验失败
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageValidate || !strings.Contains(res.Message, "校验未通过") || res.RolledBack {
		t.Fatalf("%+v", res)
	}
	if !bytes.Equal(readCfg(t, d), old) || svc.callList() != "" {
		t.Fatalf("校验没过不能碰正式配置和服务: %s %q", readCfg(t, d), svc.callList())
	}
	if _, err := os.Stat(d.PrevPath()); !os.IsNotExist(err) {
		t.Fatal("没到替换那一步,不该产生备份")
	}
}

func TestDeployMissingRuleSets(t *testing.T) {
	svc := &fakeSvc{running: true}
	dir := t.TempDir()
	cfg := map[string]any{"marker": "new", "route": map[string]any{"rule_set": []any{
		map[string]any{"type": "local", "tag": "geosite-cn", "format": "binary", "path": filepath.Join(dir, "geosite-cn.srs")},
	}}}
	d := newDP(t, cfg, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageValidate || len(res.BadTags) != 1 || res.BadTags[0] != "geosite-cn" || !strings.Contains(res.Message, "规则集") {
		t.Fatalf("%+v", res)
	}
	if !bytes.Equal(readCfg(t, d), old) || svc.callList() != "" {
		t.Fatal("规则集没就位不能应用")
	}
}

func TestDeployBuildFailure(t *testing.T) {
	d := newDP(t, nil, &fakeSvc{})
	d.Build = &fakeBuild{err: ErrCoreUnavailable}
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageValidate || !strings.Contains(res.Message, "openbox-core") {
		t.Fatalf("%+v", res)
	}
}

func TestDeployNoSingBox(t *testing.T) {
	d := newDP(t, map[string]any{"marker": "new"}, &fakeSvc{})
	d.SingBox = "/nonexistent/sing-box"
	if res := d.Deploy(context.Background()); res.OK || res.Stage != StageValidate || !strings.Contains(res.Message, "sing-box") {
		t.Fatalf("%+v", res)
	}
}

func TestDeployStatusUnknownDoesNothing(t *testing.T) {
	svc := &fakeSvc{statusErr: system.ErrNoInit}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageError || !bytes.Equal(readCfg(t, d), old) || svc.callList() != "" {
		t.Fatalf("读不到服务状态就不动: %+v", res)
	}
}

func TestDeployStartFailureRollsBack(t *testing.T) {
	svc := &fakeSvc{running: true}
	svc.doFn = func(action string, n int) (system.ActionResult, error, bool) {
		if n == 1 { // 第一次(用新配置)重启失败,回滚时的重启正常
			return system.ActionResult{OK: false, Code: 1, Stderr: "Command failed"}, nil, true
		}
		return system.ActionResult{}, nil, false
	}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageStart || !res.RolledBack || !strings.Contains(res.Message, "Command failed") || !strings.Contains(res.Message, "已回滚") {
		t.Fatalf("%+v", res)
	}
	if !bytes.Equal(readCfg(t, d), old) || svc.callList() != "restart,restart" {
		t.Fatalf("%s %s", readCfg(t, d), svc.callList())
	}
}

func TestDeployVerifyFailureRollsBack(t *testing.T) {
	svc := &fakeSvc{running: true}
	d := newDP(t, map[string]any{"marker": "bad"}, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageVerify || !res.RolledBack || !strings.Contains(res.Message, "健康检查没通过") || !strings.Contains(res.Message, "内核已恢复运行") {
		t.Fatalf("%+v", res)
	}
	if !bytes.Equal(readCfg(t, d), old) || svc.callList() != "restart,restart" {
		t.Fatalf("%s %s", readCfg(t, d), svc.callList())
	}
}

func TestDeployVerifyFailureFirstTimeRemovesConfig(t *testing.T) {
	svc := &fakeSvc{}
	d := newDP(t, map[string]any{"marker": "bad"}, svc)
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageVerify || !res.RolledBack || !strings.Contains(res.Message, "撤销") || !strings.Contains(res.Message, "保持停止") {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(d.configPath()); !os.IsNotExist(err) {
		t.Fatal("第一次部署失败应撤销新配置")
	}
	if svc.callList() != "start,stop" || svc.running {
		t.Fatalf("应停回去: %s", svc.callList())
	}
}

func TestDeployStoppedBeforeStaysStoppedAfterRollback(t *testing.T) {
	svc := &fakeSvc{running: false}
	d := newDP(t, map[string]any{"marker": "bad"}, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if res.OK || !res.RolledBack || !strings.Contains(res.Message, "保持停止") {
		t.Fatalf("%+v", res)
	}
	if !bytes.Equal(readCfg(t, d), old) || svc.callList() != "start,stop" || svc.running {
		t.Fatalf("部署前没在跑,回滚后也不能留着在跑: %s", svc.callList())
	}
}

func TestDeployRollbackRestartAlsoFails(t *testing.T) {
	svc := &fakeSvc{running: true}
	svc.doFn = func(action string, n int) (system.ActionResult, error, bool) {
		if n == 2 {
			return system.ActionResult{OK: false, Code: 3, Stderr: "boom"}, nil, true
		}
		return system.ActionResult{}, nil, false
	}
	d := newDP(t, map[string]any{"marker": "bad"}, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(context.Background())
	if res.OK || !res.RolledBack || !strings.Contains(res.Message, "旧配置重启失败") || !strings.Contains(res.Message, "boom") || !strings.Contains(res.Message, "请检查内核服务") {
		t.Fatalf("回滚也失败要明说: %+v", res)
	}
	if !bytes.Equal(readCfg(t, d), old) {
		t.Fatal("配置仍应换回旧的")
	}
}

func TestDeployRollbackWriteFailureLeavesServiceAlone(t *testing.T) {
	svc := &fakeSvc{running: true}
	d := newDP(t, map[string]any{"marker": "bad"}, svc)
	writeCfg(t, d, "old")
	// 把「写回旧配置」搞失败:健康检查时让配置路径变成目录,原子 rename 盖不过去(换成只读目录在 root 下无效)
	d.Health = func(ctx context.Context, cfg map[string]any) error {
		_ = os.Remove(d.configPath())
		_ = os.Mkdir(d.configPath(), 0o700) // 路径被目录占了,原子 rename 会失败
		return errors.New("不通")
	}
	res := d.Deploy(context.Background())
	if res.OK || res.RolledBack || !strings.Contains(res.Message, "回滚失败") || !strings.Contains(res.Message, d.PrevPath()) {
		t.Fatalf("%+v", res)
	}
	if svc.callList() != "restart" {
		t.Fatalf("配置没换回去就不要再动服务: %s", svc.callList())
	}
}

func TestDeployCrashDuringStableWindow(t *testing.T) {
	svc := &fakeSvc{running: true}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	writeCfg(t, d, "old")
	// 健康检查通过的同时内核「崩了」:稳定期结束再查时 Status 报没在跑
	d.Health = func(ctx context.Context, cfg map[string]any) error {
		svc.mu.Lock()
		svc.alive = func(int) bool { return false }
		svc.mu.Unlock()
		return nil
	}
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageVerify || !strings.Contains(res.Message, "后又出问题") {
		t.Fatalf("稳定期内崩溃要被抓住: %+v", res)
	}
}

func TestDeployConcurrentRejected(t *testing.T) {
	svc := &fakeSvc{}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	gate := make(chan struct{})
	d.Build = &fakeBuild{built: &Built{Config: map[string]any{"marker": "new"}}, gate: gate}
	done := make(chan DeployResult)
	go func() { done <- d.Deploy(context.Background()) }()
	time.Sleep(30 * time.Millisecond)
	if res := d.Deploy(context.Background()); res.OK || res.Stage != StageError || !res.Busy || !strings.Contains(res.Message, "正在进行") {
		t.Fatalf("%+v", res)
	}
	close(gate)
	if res := <-done; !res.OK {
		t.Fatalf("%+v", res)
	}
}

// 客户端断开(ctx 取消)发生在重启失败的同时:回滚仍要做完。
func TestDeployRollbackSurvivesCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	svc := &fakeSvc{running: true}
	svc.doFn = func(action string, n int) (system.ActionResult, error, bool) {
		if n == 1 {
			cancel()
			return system.ActionResult{OK: false, Code: 1, Stderr: "x"}, nil, true
		}
		return system.ActionResult{}, nil, false
	}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	old := writeCfg(t, d, "old")
	res := d.Deploy(ctx)
	if !res.RolledBack || !bytes.Equal(readCfg(t, d), old) || svc.callList() != "restart,restart" {
		t.Fatalf("%+v %s", res, svc.callList())
	}
}

func TestDeployNotConfigured(t *testing.T) {
	if res := (&Deployer{}).Deploy(context.Background()); res.OK || res.Stage != StageError {
		t.Fatalf("%+v", res)
	}
}

// ---------- 默认健康检查 ----------

func TestDialAddr(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:9090": "127.0.0.1:9090", "0.0.0.0:9090": "127.0.0.1:9090", ":9090": "127.0.0.1:9090",
		"[::]:9090": "127.0.0.1:9090", "192.168.1.1:80": "192.168.1.1:80", "": "", "nonsense": "", "host:": "",
	} {
		if got := dialAddr(in); got != want {
			t.Errorf("dialAddr(%q) = %q, 想要 %q", in, got, want)
		}
	}
}

func TestDefaultHealth(t *testing.T) {
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/version" || gotAuth != "Bearer s3" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"version":"x"}`))
	}))
	defer api.Close()
	addr := api.Listener.Addr().String()
	cfg := func(secret string) map[string]any {
		return map[string]any{"experimental": map[string]any{"clash_api": map[string]any{"external_controller": addr, "secret": secret}}}
	}
	ctx := context.Background()
	if err := DefaultHealth(ctx, cfg("s3")); err != nil || gotAuth != "Bearer s3" {
		t.Fatalf("%v %q", err, gotAuth)
	}
	if err := DefaultHealth(ctx, cfg("wrong")); err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("鉴权不过应报错且不带密钥: %v", err)
	}
	// 控制接口关了
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().String()
	l.Close()
	c := cfg("s3")
	c["experimental"].(map[string]any)["clash_api"].(map[string]any)["external_controller"] = dead
	if err := DefaultHealth(ctx, c); err == nil || !strings.Contains(err.Error(), "控制接口") {
		t.Fatalf("%v", err)
	}
	// 没有控制接口也没有入站:没什么可查,通过
	if err := DefaultHealth(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	// 入站:监听着的通过,没监听的报错;tun 之类没端口的跳过
	in, _ := net.Listen("tcp", "127.0.0.1:0")
	defer in.Close()
	port := float64(in.Addr().(*net.TCPAddr).Port)
	ok := map[string]any{"inbounds": []any{
		map[string]any{"type": "mixed", "tag": "m", "listen": "127.0.0.1", "listen_port": port},
		map[string]any{"type": "tun", "tag": "t"},
		map[string]any{"type": "direct", "tag": "d", "listen_port": float64(1)}, // 不在检查的类型里
	}}
	if err := DefaultHealth(ctx, ok); err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	bad := map[string]any{"inbounds": []any{map[string]any{"type": "mixed", "tag": "m2", "listen": "127.0.0.1", "listen_port": float64(deadPort)}}}
	if err := DefaultHealth(ctx, bad); err == nil || !strings.Contains(err.Error(), "m2") {
		t.Fatalf("%v", err)
	}
}

// ---------- 真实 sing-box ----------

// procSvc 用真实的 sing-box 进程充当服务管理:start / restart 起进程,stop 杀掉。
type procSvc struct {
	bin, cfg string
	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
}

func (p *procSvc) kill() {
	if p.cmd != nil {
		_ = p.cmd.Process.Kill()
		<-p.done
		p.cmd = nil
	}
}

func (p *procSvc) Status(ctx context.Context, name string) (system.ServiceInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		return system.ServiceInfo{}, nil
	}
	select {
	case <-p.done:
		return system.ServiceInfo{}, nil
	default:
		return system.ServiceInfo{Running: true}, nil
	}
}

func (p *procSvc) Do(ctx context.Context, name, action string) (system.ActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kill()
	if action == "stop" {
		return system.ActionResult{OK: true}, nil
	}
	cmd := exec.Command(p.bin, "run", "-c", p.cfg)
	if err := cmd.Start(); err != nil {
		return system.ActionResult{OK: false, Code: -1, Stderr: err.Error()}, nil
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	p.cmd, p.done = cmd, done
	return system.ActionResult{OK: true}, nil
}

func freeTCP(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func realCfg(inPort, apiPort int) map[string]any {
	return map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []any{map[string]any{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": inPort}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"experimental": map[string]any{"clash_api": map[string]any{
			"external_controller": "127.0.0.1:" + strconv.Itoa(apiPort), "secret": "s3cret",
		}},
	}
}

// 真实内核:第一次部署成功;第二次的配置能通过 check、但入站端口被占,内核起不来 → 自动回滚到第一份并恢复运行。
func TestDeployRealSingBox(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过(云端 Actions 里装好后跑)")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	svc := &procSvc{bin: bin, cfg: cfgPath}
	t.Cleanup(func() { svc.mu.Lock(); svc.kill(); svc.mu.Unlock() })
	build := &fakeBuild{}
	d := &Deployer{
		Build: build, SingBox: bin, ConfigDir: dir, Service: svc, ServiceName: "sing-box",
		HealthTimeout: 4 * time.Second, StableFor: 300 * time.Millisecond, Poll: 100 * time.Millisecond,
	}

	good := realCfg(freeTCP(t), freeTCP(t))
	build.built = &Built{Config: good}
	res := d.Deploy(context.Background())
	if !res.OK {
		t.Fatalf("第一次部署应成功: %+v", res)
	}
	first := readCfg(t, d)

	// 第二份:端口被测试占着。sing-box check 不会真去绑定端口,所以校验能过,只有启动时才失败
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	build.built = &Built{Config: realCfg(busy.Addr().(*net.TCPAddr).Port, freeTCP(t))}
	res = d.Deploy(context.Background())
	if res.OK || res.Stage != StageVerify || !res.RolledBack || !strings.Contains(res.Message, "内核已恢复运行") {
		t.Fatalf("端口被占应在 verify 阶段失败并回滚: %+v", res)
	}
	if !bytes.Equal(readCfg(t, d), first) {
		t.Fatal("配置应换回第一份")
	}
	// 旧配置确实又跑起来了
	if err := DefaultHealth(context.Background(), good); err != nil {
		t.Fatalf("回滚后旧配置应健康: %v", err)
	}

	// 第三份:合法,换一组端口,应成功并真正生效
	next := realCfg(freeTCP(t), freeTCP(t))
	build.built = &Built{Config: next}
	if res = d.Deploy(context.Background()); !res.OK {
		t.Fatalf("%+v", res)
	}
	if err := DefaultHealth(context.Background(), next); err != nil {
		t.Fatal(err)
	}
}

// ---------- 冲突 ----------

func TestDeployConflictBlocksStartFromStopped(t *testing.T) {
	svc := &fakeSvc{}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	d.Conflicts = func(context.Context) ([]string, error) { return []string{"OpenClash", "PassWall"}, nil }
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageConflict || res.Busy || res.BadTags == nil ||
		!strings.Contains(res.Message, "OpenClash、PassWall") {
		t.Fatalf("%+v", res)
	}
	if svc.callList() != "" {
		t.Fatalf("冲突时不能碰服务: %s", svc.callList())
	}
	if _, err := os.Stat(d.configPath()); !os.IsNotExist(err) {
		t.Fatalf("冲突时不能写配置: %v", err)
	}
}

func TestDeployConflictDoesNotBlockRestartOfRunning(t *testing.T) {
	svc := &fakeSvc{running: true}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	called := false
	d.Conflicts = func(context.Context) ([]string, error) { called = true; return []string{"OpenClash"}, nil }
	if res := d.Deploy(context.Background()); !res.OK || svc.callList() != "restart" {
		t.Fatalf("内核已在运行时不拦: %+v %s", res, svc.callList())
	}
	if called {
		t.Fatal("内核已在运行时没必要问冲突")
	}
}

func TestDeployConflictCheckFailureOrNoneDoesNotBlock(t *testing.T) {
	for name, fn := range map[string]func(context.Context) ([]string, error){
		"检测失败": func(context.Context) ([]string, error) { return nil, errors.New("读不到") },
		"没有冲突": func(context.Context) ([]string, error) { return nil, nil },
	} {
		svc := &fakeSvc{}
		d := newDP(t, map[string]any{"marker": "new"}, svc)
		d.Conflicts = fn
		if res := d.Deploy(context.Background()); !res.OK || svc.callList() != "start" {
			t.Errorf("%s 不该拦: %+v %s", name, res, svc.callList())
		}
	}
}
