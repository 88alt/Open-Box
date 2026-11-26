// Package kernelgen 把「档案 + 节点 + 订阅 + 规则集」装配成目标内核的一份配置,并在不碰系统的情况下校验它。
//
// 双内核第 4 步(切换预检)与第 5 步(切换内核)共用这一层:
//   - sing-box 一侧复用 singbox.Generator 生成,再核对配置引用的本地规则集是否就位,最后 `sing-box check`;
//   - Mihomo 一侧先把用户规则集链接同步成 .srs(与 sing-box 共用规则集目录),再装配 mihomo.yaml,
//     最后 `mihomo -t`。
//
// 这里不做落盘部署、不动服务、不改 router-kernel——那些在第 5 步的切换流程里做。
package kernelgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/mihomo"
	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// Profiles / Groups / Catalog 与 singbox.Generator 用的是同一组接口(实现直接复用现有服务)。
type (
	Profiles interface {
		Get(ctx context.Context) (map[string]any, error)
	}
	Groups interface {
		Groups(ctx context.Context) ([]any, error)
	}
	Catalog interface {
		Catalog(ctx context.Context) ([]subs.Record, []subs.CatalogNode, error)
	}
	// ListLoader 拉取一个规则集链接(*rulelist.Service 满足)。
	ListLoader interface {
		Load(ctx context.Context, rawURL string) (*rulelist.Result, error)
	}
)

// Deps 是装配要用到的服务与路径。
type Deps struct {
	Profiles Profiles
	Groups   Groups
	Catalog  Catalog
	Lists    ListLoader
	// Secret 取内核控制密钥(没有就生成并保存);singbox.Generator.ClashSecret 满足。
	Secret func() (string, error)
	// Compiler 调 core 算组 / 规则(两边都用)。
	Compiler *mihomo.Compiler

	Platform string // openwrt / systemd / linux,给 Mihomo 装配判断 dnsmasq
	DataDir  string
	GeoDir   string // 随包规则库目录
	// RulesetDir 是 .srs 目录(<DataDir>/rules),sing-box 与 Mihomo 共用。
	RulesetDir string
	SingBox    string
	Mihomo     string
	// ConfigDir 放证书(<ConfigDir>/certs/server.{crt,key} 给共享网络入口用)。
	ConfigDir string

	SystemDNS func() []string
	LANIfaces func() ([]string, error)
	// Selections 是选中项快照({组名: 节点名});nil = 没有。
	Selections func(ctx context.Context) map[string]any
	// LiveSelections 问正在运行的内核要各组此刻的选择(Mihomo 的 GET /proxies);读不到返回 error,退到快照。
	LiveSelections func(ctx context.Context) (map[string]string, error)
	FS             mihomo.FS
}

// selections 取装配用的选中项(对齐上游:实时选择优先,读不到用上次切换存下的快照,都没有就是空)。
// Mihomo 的实时选择里内置出站叫 DIRECT / REJECT,要换成我们自己配置里的内置组名字。
func (g *Generator) selections(ctx context.Context, groups []any) map[string]any {
	if g.d.LiveSelections != nil {
		if now, err := g.d.LiveSelections(ctx); err == nil && len(now) > 0 {
			builtin := mihomo.BuiltinNames{}
			if g.d.Compiler != nil {
				if b, err := g.d.Compiler.BuiltinTags(ctx, groups); err == nil {
					builtin = b
				}
			}
			return mihomo.SelectionsToTags(now, builtin)
		}
	}
	if g.d.Selections != nil {
		if sel := g.d.Selections(ctx); len(sel) > 0 {
			return sel
		}
	}
	return map[string]any{}
}

// Generator 按需生成某一侧内核的配置。并发安全靠下层(singbox.Generator / mihomo.Compiler)。
type Generator struct {
	d Deps

	// Mihomo 一侧的常驻对象:规则集转换、已部署 provider 读回、入口形状、直连 IP 段缓存。
	converter  *mihomo.Converter
	deployed   *mihomo.DeployedReader
	shapes     *mihomo.ShapeReader
	entryCache *mihomo.EntryDirectCache
}

// New 创建生成器。
func New(d Deps) *Generator {
	if d.FS == nil {
		d.FS = mihomo.OSFS{}
	}
	if d.Platform == "" {
		d.Platform = "openwrt"
	}
	g := &Generator{d: d}
	if d.Compiler != nil && d.DataDir != "" {
		mihomoDir := filepath.Join(d.DataDir, "mihomo")
		srs := mihomo.RuleSetPathFunc(d.GeoDir, d.RulesetDir)
		g.converter = &mihomo.Converter{
			FS:        d.FS,
			RulesDir:  filepath.Join(mihomoDir, "rules"),
			SrsPath:   srs,
			Decompile: mihomo.SingBoxDecompiler(d.SingBox),
		}
		g.deployed = mihomo.NewDeployedReader(d.FS, mihomoDir)
		g.shapes = &mihomo.ShapeReader{
			FS:        d.FS,
			GeoDir:    d.GeoDir,
			TmpDir:    filepath.Join(d.DataDir, "tmp"),
			SrsPath:   srs,
			Decompile: mihomo.SingBoxDecompiler(d.SingBox),
		}
		g.entryCache = mihomo.NewEntryDirectCache(16)
	}
	return g
}

// ---- sing-box 一侧 ----

// SingboxBuild 生成一份 sing-box 配置(不落盘)。
func (g *Generator) SingboxBuild(ctx context.Context, gen *singbox.Generator) (*singbox.Built, error) {
	if gen == nil {
		return nil, fmt.Errorf("没有 sing-box 配置生成器")
	}
	return gen.Build(ctx)
}

// SingboxRulesets 核对配置引用的本地规则集是否就位。不写文件(部署时才写):
// node-direct / flip-* 这类部署时生成的文件按「稍后会写」算通过,其余不存在就报缺。
func (g *Generator) SingboxRulesets(built *singbox.Built) ([]string, error) {
	if built == nil {
		return nil, fmt.Errorf("没有配置可核对")
	}
	details := []string{}
	var missing []string
	for _, s := range singbox.LocalSets(built.Config) {
		switch singbox.Classify(s) {
		case singbox.KindNodeDirect, singbox.KindFlipDynamic, singbox.KindFlipFlag:
			continue // 部署那一步会写
		}
		if _, err := os.Stat(s.Path); err != nil {
			missing = append(missing, s.Tag)
			details = append(details, fmt.Sprintf("规则集 %s 不可用(%s)", s.Tag, s.Path))
		}
	}
	if len(missing) > 0 {
		return details, fmt.Errorf("配置引用的规则集还没准备好:%s", strings.Join(missing, "、"))
	}
	return details, nil
}

// SingboxValidate 跑 `sing-box check`。
func (g *Generator) SingboxValidate(ctx context.Context, built *singbox.Built) ([]string, error) {
	if built == nil {
		return nil, fmt.Errorf("没有配置可校验")
	}
	data, err := built.JSON()
	if err != nil {
		return nil, fmt.Errorf("序列化配置失败: %w", err)
	}
	dir := g.d.DataDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := singbox.Check(ctx, g.d.SingBox, dir, data); err != nil {
		return nil, err
	}
	return []string{"sing-box 配置已通过自检"}, nil
}

// ---- Mihomo 一侧 ----

// MihomoBuild 装配一份 mihomo.yaml。规则集链接先同步成 .srs(与 sing-box 共用目录);
// 已部署的 provider 不在这里复用(每次全量转换),转换结果写进 <DataDir>/mihomo/rules。
func (g *Generator) MihomoBuild(ctx context.Context) (*mihomo.BuildResult, error) {
	if g.d.Compiler == nil || g.converter == nil {
		return nil, fmt.Errorf("mihomo 装配没有配置完整(缺少 core 编译或数据目录)")
	}
	prof, err := g.d.Profiles.Get(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := g.d.Groups.Groups(ctx)
	if err != nil {
		return nil, err
	}
	recs, cnodes, err := g.d.Catalog.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := g.d.Secret()
	if err != nil {
		return nil, fmt.Errorf("读取内核控制密钥失败: %w", err)
	}

	nodes := make([]map[string]any, 0, len(cnodes))
	for _, cn := range cnodes {
		m, err := toMap(cn.Node)
		if err != nil {
			return nil, err
		}
		m["subscriptionId"] = cn.SubscriptionID // 节点 DNS 靠它找所属订阅
		nodes = append(nodes, m)
	}
	subsList := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		if !r.Enabled {
			continue
		}
		m, err := toMap(r)
		if err != nil {
			return nil, err
		}
		subsList = append(subsList, m)
	}

	rep, err := g.syncLists(ctx, prof)
	if err != nil {
		return nil, err
	}
	ruleLists := map[string]any{}
	for _, u := range singbox.CollectRuleURLs(prof["routing"]) {
		tag := listTag(u)
		if tag == "" {
			continue
		}
		shape := rep.Shapes[tag]
		ruleLists[tag] = map[string]any{"url": u, "domain": shape.Domain, "ip": shape.IP}
	}

	var resolvers []any
	if g.d.SystemDNS != nil {
		for _, s := range g.d.SystemDNS() {
			resolvers = append(resolvers, s)
		}
	}
	var lanIfaces []string
	if g.d.LANIfaces != nil {
		lanIfaces, _ = g.d.LANIfaces()
	}
	selections := g.selections(ctx, groups)
	groupMaps := make([]map[string]any, 0, len(groups))
	for _, v := range groups {
		if m, ok := v.(map[string]any); ok {
			groupMaps = append(groupMaps, m)
		}
	}
	mihomoDir := filepath.Join(g.d.DataDir, "mihomo")
	in := mihomo.BuildInput{
		Platform:      g.d.Platform,
		DataDir:       g.d.DataDir,
		Profile:       prof,
		Nodes:         nodes,
		Groups:        groupMaps,
		Subscriptions: subsList,
		RuleLists:     ruleLists,
		Selections:    selections,
		SystemDNS:     resolvers,
		Secret:        secret,
		ChainNodes:    chainNodes(prof),
		LanIfaces:     lanIfaces,
	}
	deps := mihomo.BuildDeps{
		FS:         g.d.FS,
		Compiler:   g.d.Compiler,
		Converter:  g.converter,
		Deployed:   g.deployed,
		EntryCache: g.entryCache,
		RulesDir:   filepath.Join(mihomoDir, "rules"),
		MihomoDir:  mihomoDir,
		ReadFile:   func(path string) (string, error) { b, err := os.ReadFile(path); return string(b), err },
		TLSCert:    filepath.Join(g.d.ConfigDir, "certs", "server.crt"),
		TLSKey:     filepath.Join(g.d.ConfigDir, "certs", "server.key"),
		Bypass: mihomo.EntryBypassDeps{
			Shape:     g.shapes.Shape,
			UserLists: func() (map[string]mihomo.UserList, error) { return userLists(ruleLists), nil },
			SrsPath:   mihomo.RuleSetPathFunc(g.d.GeoDir, g.d.RulesetDir),
			FS:        g.d.FS,
		},
	}
	return mihomo.BuildConfig(ctx, in, deps)
}

// MihomoCompat 问 core:Mihomo 表达不了的配置有哪些(空 = 没有)。
// profile 里没登记的规则集由 rulesets 一项单独检查,这里不重复。
func (g *Generator) MihomoCompat(ctx context.Context) ([]string, error) {
	if g.d.Compiler == nil {
		return nil, fmt.Errorf("mihomo 兼容性检查没有 core 编译")
	}
	prof, err := g.d.Profiles.Get(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := g.d.Groups.Groups(ctx)
	if err != nil {
		return nil, err
	}
	recs, cnodes, err := g.d.Catalog.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	nodes := make([]map[string]any, 0, len(cnodes))
	for _, cn := range cnodes {
		m, err := toMap(cn.Node)
		if err != nil {
			return nil, err
		}
		m["subscriptionId"] = cn.SubscriptionID
		nodes = append(nodes, m)
	}
	_ = recs
	selections := g.selections(ctx, groups)
	return g.d.Compiler.Compatibility(ctx, mihomo.CompatInput{
		Profile: prof, Nodes: nodes, Groups: groups, ChainNodes: chainNodes(prof), Selections: selections,
	})
}

// MihomoRulesets 同步规则集链接(.srs)并报告没处理好的链接(有旧文件可沿用的不算失败)。
func (g *Generator) MihomoRulesets(ctx context.Context) ([]string, error) {
	prof, err := g.d.Profiles.Get(ctx)
	if err != nil {
		return nil, err
	}
	rep, err := g.syncLists(ctx, prof)
	if err != nil {
		return nil, err
	}
	details := []string{}
	for _, is := range rep.Issues {
		if is.UsedOld {
			details = append(details, fmt.Sprintf("%s 沿用了上次的文件(%s)", is.Tag, is.Reason))
		}
	}
	details = append(details, fmt.Sprintf("%d 个规则集链接已就绪", len(rep.Shapes)))
	return details, nil
}

// MihomoValidate 把装配结果写成临时配置并跑 `mihomo -t`。
func (g *Generator) MihomoValidate(ctx context.Context, res *mihomo.BuildResult) ([]string, error) {
	if res == nil {
		return nil, fmt.Errorf("没有装配结果可校验")
	}
	text, err := mihomo.MarshalYAML(res.Config)
	if err != nil {
		return nil, fmt.Errorf("写出 mihomo.yaml 失败: %w", err)
	}
	dir := filepath.Join(g.d.DataDir, "mihomo")
	if err := g.d.FS.MkdirAll(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "preflight.yaml")
	if err := g.d.FS.WriteFile(path, text, 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(path)
	if err := mihomo.Check(ctx, g.d.Mihomo, path, dir, 20*time.Second); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("mihomo 配置已通过自检(%d 字节)", len(text))}, nil
}

// preflightNftTable 是校验入口规则时套的外层表名。只用于 `nft -c` 语法检查(不加载),
// 所以不必与服务脚本实际建的那张表同名,取一个不会撞的名字。
const preflightNftTable = "openbox-preflight"

// MihomoFirewall 校验入口防火墙规则(`nft -c -f`)。没有入口 nft 就不适用。
// 返回 applicable=false 表示这次切换不涉及入口防火墙。
func (g *Generator) MihomoFirewall(ctx context.Context, res *mihomo.BuildResult) ([]string, bool, error) {
	if res == nil || strings.TrimSpace(res.Entry) == "" {
		return nil, false, nil
	}
	if _, err := exec.LookPath("nft"); err != nil {
		return nil, false, nil // 没有 nft 的环境(容器 / 桌面)不适用,与上游一致
	}
	dir := filepath.Join(g.d.DataDir, "mihomo")
	if err := g.d.FS.MkdirAll(dir); err != nil {
		return nil, true, err
	}
	path := filepath.Join(dir, "preflight-entry.nft")
	// res.Entry 是**链正文片段,不含 table 外壳**(服务脚本 openbox_apply_nft 会自己
	// `echo "table $NFT_TABLE {"` 包住再加载,见 openwrt/initd/openbox 的注释)。
	// 所以这里也要照那个形状包一层再交给 `nft -c`,否则裸片段必然报
	// `syntax error, unexpected '{', expecting string or last` —— 那是校验方式错了,不是规则错了
	// (2026-11-26 真机:切回 mihomo 被 firewall 这一项拦住,原因就是拿裸片段去 -c)。
	wrapped := "table inet " + preflightNftTable + " {\n" + res.Entry + "\n}\n"
	if err := g.d.FS.WriteFile(path, []byte(wrapped), 0o600); err != nil {
		return nil, true, err
	}
	defer os.Remove(path)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "nft", "-c", "-f", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, true, fmt.Errorf("入口防火墙规则没通过校验:%s", msg)
	}
	return []string{"入口防火墙规则已通过 nft 校验"}, true, nil
}

// syncLists 把档案里引用的规则集链接同步成 .srs,并给出 BuildInput.RuleLists 要的形状。
// 链接没处理好但没有旧文件可沿用时报错(原因与 sing-box 一侧同一句文案)。
func (g *Generator) syncLists(ctx context.Context, prof map[string]any) (*singbox.ListReport, error) {
	urls := singbox.CollectRuleURLs(prof["routing"])
	files := singbox.ListFiles{Rules: g.d.Lists, SingBox: g.d.SingBox, Dir: g.d.RulesetDir}
	rep, err := files.Sync(ctx, urls)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, is := range rep.Issues {
		if !is.UsedOld {
			bad = append(bad, is.Tag+"("+is.Reason+")")
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("规则集链接还没准备好:%s", strings.Join(bad, "、"))
	}
	return rep, nil
}

// listTag 与 core / sing-box 用的名字一致:list-<fnv1a 8 位>。
func listTag(rawURL string) string {
	return policyentries.ListTagForURL(strings.TrimSpace(rawURL))
}

// userLists 把规则集链接的形状摊成入口旁路要的 UserLists。
func userLists(ruleLists map[string]any) map[string]mihomo.UserList {
	out := make(map[string]mihomo.UserList, len(ruleLists))
	for tag, v := range ruleLists {
		m, _ := v.(map[string]any)
		ip, _ := m["ip"].(bool)
		out[tag] = mihomo.UserList{IP: ip}
	}
	return out
}

// chainNodes 把档案里启用的链式代理解析成候选节点:tag = 名字,detour = 上游,chain = true。
// (与 singbox 一侧同一口径;解析不了的跳过,core 还会再核对上游是否存在 / 成环。)
func chainNodes(profile map[string]any) []any {
	list, _ := profile["chainProxies"].([]any)
	out := []any{}
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if en, has := m["enabled"].(bool); has && !en {
			continue
		}
		name, _ := m["name"].(string)
		link, _ := m["link"].(string)
		up, _ := m["upstream"].(string)
		name = strings.TrimSpace(name)
		if name == "" || strings.TrimSpace(link) == "" || up == "" {
			continue
		}
		res, err := sub.Parse([]byte(link))
		if err != nil || len(res.Nodes) == 0 {
			continue
		}
		n, err := toMap(res.Nodes[0])
		if err != nil {
			continue
		}
		n["tag"], n["detour"], n["chain"] = name, up, true
		delete(n, "originalTag")
		delete(n, "regionCode")
		out = append(out, n)
	}
	return out
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}
