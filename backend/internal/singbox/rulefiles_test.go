package singbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cfgWith(sets ...map[string]any) map[string]any {
	list := make([]any, 0, len(sets))
	for _, s := range sets {
		list = append(list, s)
	}
	return map[string]any{"route": map[string]any{"rule_set": list}}
}

func local(tag, format, path string) map[string]any {
	return map[string]any{"type": "local", "tag": tag, "format": format, "path": path}
}

func TestLocalSetsKeepsOrderAndSkipsRemote(t *testing.T) {
	cfg := cfgWith(
		local("a", "binary", "/x/a.srs"),
		map[string]any{"type": "remote", "tag": "r", "url": "https://e.com/r.srs"},
		local("a", "binary", "/y/a.srs"), // 重复 tag 只留第一条
		local("b", "source", "/x/b.json"),
		map[string]any{"type": "local", "tag": "", "path": "/x/none"}, // 没 tag 的丢掉
	)
	got := LocalSets(cfg)
	if len(got) != 2 || got[0].Tag != "a" || got[0].Path != "/x/a.srs" || got[1].Tag != "b" {
		t.Fatalf("got %+v", got)
	}
	if len(LocalSets(map[string]any{})) != 0 {
		t.Fatal("没有 route 应返回空")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Kind{
		"obnode-direct":          KindNodeDirect,
		"obnode-direct-ip":       KindNodeDirect,
		"obflip-ff2082aa78ae":    KindFlipFlag,
		"obflip-fallback":        KindFlipFlag,
		"obflip-byp-geoip-cn":    KindFlipDynamic,
		"obflip-need-all":        KindFlipDynamic,
		"obflip-need-geoip-cn":   KindFlipDynamic,
		"geosite-cn":             KindRuleset,
		"geoip-cn":               KindRuleset,
		"user-ruleset-abcdef012": KindRuleset,
	}
	for tag, want := range cases {
		if got := Classify(LocalSet{Tag: tag}); got != want {
			t.Errorf("%s: got %s want %s", tag, got, want)
		}
	}
}

func TestNodeDirectSource(t *testing.T) {
	b, err := nodeDirectSource(nodeDirectDomainTag, []string{"hk.example.com", "us.example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"version":3,"rules":[{"domain":["hk.example.com","us.example.com"]}]}` {
		t.Fatalf("domain: %s", b)
	}
	b, _ = nodeDirectSource(nodeDirectIPTag, nil, []string{"203.0.113.0/24"})
	if string(b) != `{"version":3,"rules":[{"ip_cidr":["203.0.113.0/24"]}]}` {
		t.Fatalf("ip: %s", b)
	}
	// 空名单用占位
	b, _ = nodeDirectSource(nodeDirectDomainTag, nil, nil)
	if !strings.Contains(string(b), placeholderDomain) {
		t.Fatalf("空域名应有占位: %s", b)
	}
	b, _ = nodeDirectSource(nodeDirectIPTag, nil, nil)
	if !strings.Contains(string(b), placeholderCIDR4) {
		t.Fatalf("空地址应有占位: %s", b)
	}
	if _, err := nodeDirectSource("other", nil, nil); err == nil {
		t.Fatal("不认识的 tag 应报错")
	}
}

func TestDirectHostLists(t *testing.T) {
	d, c := directHostLists(nil)
	if len(d) != 0 || len(c) != 0 {
		t.Fatal("nil 应得到空名单")
	}
	d, c = directHostLists(map[string]any{
		"domains": []any{"a.com", "", 7, "b.com"},
		"cidrs":   []any{"10.0.0.0/8"},
	})
	if len(d) != 2 || d[0] != "a.com" || d[1] != "b.com" || len(c) != 1 {
		t.Fatalf("d=%v c=%v", d, c)
	}
}

// 不需要 sing-box:node-direct 能写,其余如实报缺。
func TestWriteLocalSetsNodeDirectAndMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := cfgWith(
		local("geosite-cn", "binary", filepath.Join(dir, "rules", "geosite-cn.srs")),
		local(nodeDirectDomainTag, "source", filepath.Join(dir, "rules", "obnode-direct.json")),
		local(nodeDirectIPTag, "source", filepath.Join(dir, "rules", "obnode-direct-ip.json")),
		local("obflip-abc123abc123", "source", filepath.Join(dir, "flip", "obflip-abc123abc123.json")),
	)
	direct := map[string]any{"domains": []any{"hk.example.com"}, "cidrs": []any{}}
	rep, err := Files{SingBox: "no-such-sing-box"}.WriteLocalSets(context.Background(), cfg, direct)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Written) != 2 {
		t.Fatalf("written=%v", rep.Written)
	}
	b, err := os.ReadFile(filepath.Join(dir, "rules", "obnode-direct.json"))
	if err != nil || !strings.Contains(string(b), "hk.example.com") {
		t.Fatalf("内容 %s err=%v", b, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	ip, _ := os.ReadFile(filepath.Join(dir, "rules", "obnode-direct-ip.json"))
	if !strings.Contains(string(ip), placeholderCIDR4) {
		t.Fatalf("空 cidrs 应写占位: %s", ip)
	}
	if st, err := os.Stat(filepath.Join(dir, "rules", "obnode-direct.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("权限应为 0600: %v %v", st, err)
	}
	if rep.OK() || len(rep.Missing) != 2 {
		t.Fatalf("missing=%+v", rep.Missing)
	}
	kinds := map[Kind]bool{}
	for _, m := range rep.Missing {
		kinds[m.Kind] = true
		if m.Reason == "" || m.Path == "" || m.Tag == "" {
			t.Fatalf("缺原因 / 路径: %+v", m)
		}
	}
	if !kinds[KindRuleset] || !kinds[KindFlipFlag] {
		t.Fatalf("kinds=%v", kinds)
	}
	// 重复执行:node-direct 按最新名单改写
	rep2, _ := Files{}.WriteLocalSets(context.Background(), cfg, map[string]any{"domains": []any{"new.example.com"}})
	_ = rep2
	b, _ = os.ReadFile(filepath.Join(dir, "rules", "obnode-direct.json"))
	if !strings.Contains(string(b), "new.example.com") || strings.Contains(string(b), "hk.example.com") {
		t.Fatalf("应改写成新名单: %s", b)
	}
}

func TestWriteLocalSetsFormatMismatch(t *testing.T) {
	dir := t.TempDir()
	cfg := cfgWith(
		local(nodeDirectDomainTag, "binary", filepath.Join(dir, "a.json")),
		local("obflip-need-all", "source", filepath.Join(dir, "b.srs")),
	)
	rep, _ := Files{}.WriteLocalSets(context.Background(), cfg, nil)
	if len(rep.Written) != 0 || len(rep.Missing) != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestWriteLocalSetsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Files{}.WriteLocalSets(ctx, cfgWith(local(nodeDirectDomainTag, "source", filepath.Join(t.TempDir(), "x.json"))), nil)
	if err == nil {
		t.Fatal("ctx 取消应返回 error")
	}
}

func TestFlipDynamicNoSingBox(t *testing.T) {
	dir := t.TempDir()
	cfg := cfgWith(local("obflip-need-all", "binary", filepath.Join(dir, "flip", "obflip-need-all.srs")))
	rep, err := Files{SingBox: "no-such-sing-box"}.WriteLocalSets(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Missing) != 1 || !strings.Contains(rep.Missing[0].Reason, ErrNoSingBox.Error()) {
		t.Fatalf("%+v", rep.Missing)
	}
}

// 需要真实 sing-box:占位 .srs 能编出来、已存在时不覆盖、sing-box check 认得整份配置。
func TestFlipDynamicCompilePlaceholderWithRealSingBox(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flip", "obflip-need-all.srs")
	cfg := cfgWith(local("obflip-need-all", "binary", path))
	rep, err := Files{SingBox: bin}.WriteLocalSets(context.Background(), cfg, nil)
	if err != nil || !rep.OK() || len(rep.Written) != 1 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 8 || string(data[:3]) != "SRS" {
		t.Fatalf("应是 .srs(以 SRS 开头): %q err=%v", data, err)
	}
	// 已存在 → 保留(热切换写进去的内容不能被覆盖)
	if err := os.WriteFile(path, []byte("SRS-custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _ = Files{SingBox: bin}.WriteLocalSets(context.Background(), cfg, nil)
	if len(rep.Kept) != 1 || len(rep.Written) != 0 {
		t.Fatalf("rep=%+v", rep)
	}
	if b, _ := os.ReadFile(path); string(b) != "SRS-custom" {
		t.Fatalf("被覆盖了: %q", b)
	}
	// 临时文件不留
	left, _ := filepath.Glob(filepath.Join(dir, "flip", ".compile-*"))
	if len(left) != 0 {
		t.Fatalf("残留临时文件: %v", left)
	}
}

// 真实 core 生成的配置 + 真实 sing-box:落盘后,配置里引用的本地规则集只剩「生成不了的」那几类。
func TestWriteLocalSetsFromRealBuild(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	e := newEnv(t, System{})
	e.addSub(t, "A", twoNodes)
	built, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 把配置里的 /data 前缀换到临时目录
	root := t.TempDir()
	raw, _ := built.JSON()
	moved := strings.ReplaceAll(string(raw), `"/data/`, `"`+root+`/`)
	var cfg map[string]any
	if err := json.Unmarshal([]byte(moved), &cfg); err != nil {
		t.Fatal(err)
	}
	rep, err := Files{SingBox: bin}.WriteLocalSets(context.Background(), cfg, built.DirectHosts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Written) < 5 { // 2 个 node-direct + 至少 3 个动态集占位
		t.Fatalf("written=%v", rep.Written)
	}
	for _, m := range rep.Missing {
		if m.Kind != KindFlipFlag && m.Kind != KindRuleset {
			t.Fatalf("不该缺的: %+v", m)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "rules", "obnode-direct.json"))
	if err != nil || !strings.Contains(string(b), "hk.example.com") || !strings.Contains(string(b), "us.example.com") {
		t.Fatalf("node-direct 内容: %s err=%v", b, err)
	}
}
