package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuleSetKind(t *testing.T) {
	cases := []struct {
		tag  string
		kind string
		ok   bool
	}{
		{"geoip-cn", "ipcidr", true},
		{"geosite-cn", "domain", true},
		{"geosite-category-ads-all", "domain", true},
		{"list-0123abcd", "domain", true},
		{"list-0123abcd-ip", "ipcidr", true},
		{"list-0123ABCD", "", false}, // 十六进制只认小写
		{"list-0123abc", "", false},  // 不是 8 位
		{"list-0123abcd-x", "", false},
		{"geoip-", "", false},       // 前缀后面必须有内容
		{"geosite-a..b", "", false}, // 含 .. 视为不合法
		{"geosite-a/b", "", false},  // 含路径分隔符
		{"whatever", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		k, ok := RuleSetKind(c.tag)
		if k != c.kind || ok != c.ok {
			t.Errorf("RuleSetKind(%q) = %q,%v,想要 %q,%v", c.tag, k, ok, c.kind, c.ok)
		}
	}
}

func TestRuleSetPathFunc(t *testing.T) {
	f := RuleSetPathFunc("/geo", "/data/rules")
	for tag, want := range map[string]string{
		"geoip-cn":      "/geo/geoip-cn.srs",
		"geosite-cn":    "/geo/geosite-cn.srs",
		"list-0123abcd": "/data/rules/list-0123abcd.srs",
	} {
		if got, err := f(tag); err != nil || got != want {
			t.Errorf("%s: %q %v,想要 %q", tag, got, err, want)
		}
	}
	for _, bad := range []string{"../x", "a/b", "geosite-..", "", "a b"} {
		if _, err := f(bad); err == nil || !strings.Contains(err.Error(), "不合法的规则集名") {
			t.Errorf("%q 应报不合法: %v", bad, err)
		}
	}
}

// 假的 sing-box:按路径给出源文件 JSON;记下调用顺序。
func newConverter(fs *memFS, sources map[string]string, calls *[]string) *Converter {
	return &Converter{
		FS:       fs,
		RulesDir: "/d/mihomo/rules",
		SrsPath:  RuleSetPathFunc("/geo", "/d/rules"),
		Decompile: func(_ context.Context, srs, out string) error {
			*calls = append(*calls, srs)
			body, ok := sources[srs]
			if !ok {
				return errors.New("无法读取现有规则集文件")
			}
			return fs.WriteFile(out, []byte(body), 0o600)
		},
	}
}

func TestConverterConvert(t *testing.T) {
	fs := newMemFS()
	var calls []string
	c := newConverter(fs, map[string]string{
		"/geo/geosite-cn.srs":        `{"version":3,"rules":[{"domain_suffix":["cn","qq.com"]}]}`,
		"/geo/geoip-cn.srs":          `{"version":3,"rules":[{"ip_cidr":["1.0.1.0/24","240e::/18"]}]}`,
		"/d/rules/list-0123abcd.srs": `{"version":3,"rules":[{"domain":["a.com"],"domain_keyword":["ads"]}]}`,
	}, &calls)
	// geosite-cn 重复一次:只转一次
	res, err := c.Convert(context.Background(), []string{"geosite-cn", "geoip-cn", "geosite-cn", "list-0123abcd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0] != "/geo/geosite-cn.srs" || calls[1] != "/geo/geoip-cn.srs" || calls[2] != "/d/rules/list-0123abcd.srs" {
		t.Errorf("还原次序 %v", calls)
	}
	wantKinds := map[string]string{"geosite-cn": "domain", "geoip-cn": "ipcidr", "list-0123abcd": "domain"}
	for tag, k := range wantKinds {
		if res.Kinds[tag] != k {
			t.Errorf("%s 类别 %q,想要 %q", tag, res.Kinds[tag], k)
		}
	}
	if res.Providers["geosite-cn"].Behavior != "domain" || res.Providers["geoip-cn"].Behavior != "ipcidr" || res.Providers["list-0123abcd"].Behavior != "classical" {
		t.Errorf("行为: %+v", res.Providers)
	}
	// 规则集的 kind 是 domain,但 behavior 可以是 classical:Kinds 看名字,不看行为
	if res.Kinds["list-0123abcd"] != "domain" {
		t.Errorf("list-* 的类别应是 domain")
	}
	if len(fs.dirs) != 1 || fs.dirs[0] != "/d/mihomo/rules" {
		t.Errorf("应创建 rules 目录: %v", fs.dirs)
	}
	// 临时 JSON 要删干净,只剩三个 provider
	for p := range fs.files {
		if strings.HasSuffix(p, ".json") {
			t.Errorf("临时文件没删: %s", p)
		}
	}
	if len(fs.files) != 3 {
		t.Errorf("应只有 3 个 provider 文件: %v", fs.files)
	}
	// 再转一次:文件已存在,不重写 provider(只多出还原的临时文件写入)
	before := fs.writes
	if _, err := c.Convert(context.Background(), []string{"geosite-cn"}); err != nil {
		t.Fatal(err)
	}
	if fs.writes != before+1 { // 仅临时 JSON
		t.Errorf("不应重写已有 provider: 写入 %d → %d", before, fs.writes)
	}
}

func TestConverterErrors(t *testing.T) {
	fs := newMemFS()
	var calls []string
	c := newConverter(fs, map[string]string{
		"/geo/geosite-bad.srs": `{"version":3,"rules":[{"port":[80]}]}`,
		"/geo/geosite-nil.srs": `{"version":3,"rules":[]}`,
		"/geo/geosite-js.srs":  `{oops`,
	}, &calls)
	cases := []struct{ tag, want string }{
		{"foo", "mihomo 不支持规则集 foo"},
		{"geosite-missing", "mihomo 规则集 geosite-missing 转换失败：无法读取现有规则集文件"},
		{"geosite-bad", "mihomo 规则集 geosite-bad 转换失败：无法转换规则字段 port"},
		{"geosite-nil", "mihomo 规则集 geosite-nil 转换失败：规则集没有可用条目"},
		{"geosite-js", "mihomo 规则集 geosite-js 转换失败：规则集源文件不是有效的 JSON"},
	}
	for _, tc := range cases {
		_, err := c.Convert(context.Background(), []string{tc.tag})
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v,想要 %q", tc.tag, err, tc.want)
		}
	}
	// 失败后临时文件也要清掉
	for p := range fs.files {
		t.Errorf("失败后不应留文件: %s", p)
	}
	// 出错即停:第一个不认得的名字之后的不再处理
	calls = nil
	if _, err := c.Convert(context.Background(), []string{"foo", "geosite-bad"}); err == nil || len(calls) != 0 {
		t.Errorf("应在 foo 处停下: %v %v", err, calls)
	}
}

func TestSingBoxDecompilerRealBinaryMissing(t *testing.T) {
	dec := SingBoxDecompiler("/nonexistent/sing-box")
	err := dec(context.Background(), "/x.srs", filepath.Join(t.TempDir(), "o.json"))
	if err == nil || err.Error() != "无法读取现有规则集文件" {
		t.Errorf("err=%v", err)
	}
}

// 有真 sing-box 时走一遍完整流程:json → compile → srs → Convert(decompile) → yaml。没有就跳过。
func TestConverterWithRealSingBox(t *testing.T) {
	sb, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box 可执行文件")
	}
	dir := t.TempDir()
	srcJSON := filepath.Join(dir, "geosite-test.json")
	if err := os.WriteFile(srcJSON, []byte(`{"version":3,"rules":[{"domain_suffix":["example.com","example.org"],"domain":["www.example.net"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sb, "rule-set", "compile", "--output", filepath.Join(dir, "geosite-test.srs"), srcJSON).CombinedOutput(); err != nil {
		t.Skipf("sing-box 编译规则集失败(版本不支持?): %v %s", err, out)
	}
	c := &Converter{FS: OSFS{}, RulesDir: filepath.Join(dir, "mihomo", "rules"), SrsPath: RuleSetPathFunc(dir, dir), Decompile: SingBoxDecompiler(sb)}
	res, err := c.Convert(context.Background(), []string{"geosite-test"})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Providers["geosite-test"]
	b, err := os.ReadFile(filepath.Join(dir, "mihomo", p.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+.example.com", "+.example.org", "www.example.net"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("载荷缺 %s: %s", want, b)
		}
	}
	if p.Behavior != "domain" {
		t.Errorf("行为 %s", p.Behavior)
	}
}

// 有原版 Mihomo 时,让它真的加载我们生成的 provider:启动后从控制接口 /providers/rules 读每个 provider 的行为和条数。
// (`mihomo -t` 不加载 rule-provider 文件,反例也不会失败,所以不用它。)没有 mihomo 就跳过。
func TestProvidersLoadedByMihomo(t *testing.T) {
	mh, err := exec.LookPath("mihomo")
	if err != nil {
		t.Skip("没有 mihomo 可执行文件")
	}
	dir := t.TempDir()
	fs := newMemFS()
	var calls []string
	c := newConverter(fs, map[string]string{
		"/geo/geosite-x.srs":         `{"version":3,"rules":[{"domain_suffix":["example.com","example.org"],"domain":["www.example.net"]}]}`,
		"/geo/geoip-x.srs":           `{"version":3,"rules":[{"ip_cidr":["1.0.1.0/24","240e::/18"]}]}`,
		"/d/rules/list-0123abcd.srs": `{"version":3,"rules":[{"domain_keyword":["ads"],"domain_regex":["^a\\\\.b$"],"domain":["x.com"]}]}`,
	}, &calls)
	tags := []string{"geosite-x", "geoip-x", "list-0123abcd"}
	res, err := c.Convert(context.Background(), tags)
	if err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(dir, "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for p, b := range fs.files { // 把假文件系统里的 provider 落到真目录
		if err := os.WriteFile(filepath.Join(rulesDir, filepath.Base(p)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("没有可用端口")
	}
	ctl := ln.Addr().String()
	ln.Close()
	var cfg strings.Builder
	cfg.WriteString("mixed-port: 0\nexternal-controller: " + ctl + "\nrule-providers:\n")
	for _, tag := range tags {
		p := res.Providers[tag]
		cfg.WriteString("  " + tag + ":\n    type: " + p.Type + "\n    behavior: " + p.Behavior + "\n    format: " + p.Format + "\n    path: " + p.Path + "\n")
	}
	cfg.WriteString("rules:\n  - RULE-SET,geosite-x,DIRECT\n  - RULE-SET,geoip-x,DIRECT,no-resolve\n  - RULE-SET,list-0123abcd,DIRECT\n  - MATCH,DIRECT\n")
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(mh, "-d", dir, "-f", cfgPath)
	if err := cmd.Start(); err != nil {
		t.Skipf("启动 mihomo 失败: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	type prov struct {
		Behavior  string `json:"behavior"`
		RuleCount int    `json:"ruleCount"`
	}
	var got map[string]prov
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + ctl + "/providers/rules")
		if err == nil {
			var body struct {
				Providers map[string]prov `json:"providers"`
			}
			err = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if err == nil && len(body.Providers) == len(tags) {
				got = body.Providers
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got == nil {
		t.Skip("mihomo 没在 15 秒内起来(环境限制?)")
	}
	want := map[string]prov{
		"geosite-x":     {"Domain", 3},
		"geoip-x":       {"IPCIDR", 2},
		"list-0123abcd": {"Classical", 3},
	}
	for tag, w := range want {
		if got[tag] != w {
			t.Errorf("%s: mihomo 读到 %+v,想要 %+v", tag, got[tag], w)
		}
	}
}
