package singbox

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

// 用户在站点集 / 前置自定义分流里填的「规则集链接」(文本名单),core 会把每个链接折成规则集名 list-<hash>
// (算法见 policyentries.ListTagForURL,与 core 一致)。sing-box 只认 .srs,所以部署前要自己拉名单、编成:
//
//	<规则集目录>/list-<hash>.srs      域名那一份(domain / domain_suffix / domain_keyword / domain_regex)
//	<规则集目录>/list-<hash>-ip.srs   IP 那一份(ip_cidr)
//
// 两份必须分开:规则集内部「域名」和「IP」是「与」的关系,混在一份里一条名单永远匹配不到东西。
// 哪份存在要告诉 core(configDetailed 的 ruleLists,形状 {tag: {domain, ip}}),它才知道该引用哪几份。

var listFileRe = regexp.MustCompile(`^list-[0-9a-f]{8}(-ip)?\.srs$`)

// ListShape 是一个规则集链接编出了哪几份。JSON 形状与 core 的 ruleLists 一致。
type ListShape struct {
	Domain bool `json:"domain"`
	IP     bool `json:"ip"`
}

// ListLoader 拉取并解析一个规则集链接(*rulelist.Service 满足)。
type ListLoader interface {
	Load(ctx context.Context, rawURL string) (*rulelist.Result, error)
}

// ListIssue 是某个链接没处理好的情况。UsedOld 为真表示磁盘上有上次编好的文件,继续沿用了,不影响部署。
type ListIssue struct {
	Tag     string `json:"tag"`
	Reason  string `json:"reason"`
	UsedOld bool   `json:"usedOld"`
}

// ListReport 是一次同步的结果。
type ListReport struct {
	Shapes  map[string]ListShape // 每个链接(按 tag)实际有哪几份
	Written []string             // 新写或改写的文件
	Kept    []string             // 内容没变、没动的文件
	Pruned  []string             // 不再被引用而删掉的 list-*.srs
	Dropped map[string]int       // 每个链接里被丢掉的非法正则条数(tag → 条数);sing-box 编译不查正则,留着会让 check 整体失败
	Issues  []ListIssue
}

// ListFiles 把规则集链接编成 .srs。
type ListFiles struct {
	Rules   ListLoader
	SingBox string // sing-box 可执行文件;空 = "sing-box"
	Dir     string // 规则集目录(Options.RulesetDir)
}

// CollectRuleURLs 从档案的 routing 里取出全部规则集链接:站点集的 ruleUrls 和前置自定义分流里 type=ruleUrl 的行。
// 只收 http(s) 开头的(与 core 的规范化一致),去掉首尾空白,按 tag 去重并保持出现顺序。
func CollectRuleURLs(routing any) []string {
	r, _ := routing.(map[string]any)
	out := []string{}
	seen := map[string]bool{}
	add := func(v any) {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		low := strings.ToLower(s)
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			return
		}
		tag := policyentries.ListTagForURL(s)
		if seen[tag] {
			return
		}
		seen[tag] = true
		out = append(out, s)
	}
	pols, _ := r["policies"].([]any)
	for _, p := range pols {
		pm, _ := p.(map[string]any)
		urls, _ := pm["ruleUrls"].([]any)
		for _, u := range urls {
			add(u)
		}
	}
	custom, _ := r["custom"].(map[string]any)
	rules, _ := custom["rules"].([]any)
	for _, x := range rules {
		m, _ := x.(map[string]any)
		if t, _ := m["type"].(string); t == "ruleUrl" {
			add(m["value"])
		}
	}
	return out
}

// Sync 拉取每个链接并编成 .srs,返回形状表给配置生成用。
//   - 拉取 / 解析 / 编译失败:磁盘上有上次的文件就沿用(UsedOld),没有就记成问题、没有形状,
//     配置里引用的 list-<hash> 会因文件不在而被 WriteLocalSets 如实报缺,部署据此拒绝;
//   - 名单里某一类(域名 / IP)为空:不写那一份,删掉旧的;两类都空:两份都不要(形状全假);
//   - 编完后删掉不再被任何链接引用的 list-*.srs。单个链接失败不影响其它;只有 ctx 取消才返回 error。
func (l ListFiles) Sync(ctx context.Context, urls []string) (*ListReport, error) {
	rep := newListReport()
	keep := map[string]bool{}
	done := map[string]bool{}
	for _, u := range urls {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		tag := policyentries.ListTagForURL(u)
		if tag == "" || done[tag] {
			continue
		}
		done[tag] = true
		if err := l.syncOne(ctx, u, tag, rep, keep); err != nil {
			return rep, err
		}
	}
	l.prune(keep, rep)
	return rep, nil
}

func newListReport() *ListReport {
	return &ListReport{Shapes: map[string]ListShape{}, Written: []string{}, Kept: []string{}, Pruned: []string{}, Dropped: map[string]int{}, Issues: []ListIssue{}}
}

// syncOne 处理一个链接(拉取 → 拆分 → 编译),结果记进 rep,要保留的文件名记进 keep。
// 只有 ctx 取消才返回 error;其余失败记成 Issue。不做清理(清理是 Sync 的事)。
func (l ListFiles) syncOne(ctx context.Context, u, tag string, rep *ListReport, keep map[string]bool) error {
	domPath := filepath.Join(l.Dir, tag+".srs")
	ipPath := filepath.Join(l.Dir, tag+"-ip.srs")
	fallback := func(reason string) {
		d, i := hasSRS(domPath), hasSRS(ipPath)
		if d || i {
			rep.Shapes[tag] = ListShape{Domain: d, IP: i}
			keep[tag+".srs"], keep[tag+"-ip.srs"] = d, i
			rep.Issues = append(rep.Issues, ListIssue{Tag: tag, Reason: reason + "(沿用上次编好的文件)", UsedOld: true})
			return
		}
		rep.Issues = append(rep.Issues, ListIssue{Tag: tag, Reason: reason})
	}
	if l.Rules == nil {
		fallback("没有配置规则集链接的拉取服务")
		return nil
	}
	res, err := l.Rules.Load(ctx, u)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fallback("拉取或解析规则集链接失败:" + err.Error())
		return nil
	}
	dom, ip, dropped := splitEntries(res.Entries)
	if dropped > 0 {
		rep.Dropped[tag] = dropped
	}
	shape := ListShape{Domain: len(dom) > 0, IP: len(ip) > 0}
	failed := ""
	for _, part := range []struct {
		path string
		rule map[string]any
		has  bool
		name string
	}{
		{domPath, dom, shape.Domain, tag + ".srs"},
		{ipPath, ip, shape.IP, tag + "-ip.srs"},
	} {
		if !part.has {
			if err := os.Remove(part.path); err == nil {
				rep.Pruned = append(rep.Pruned, part.path)
			}
			continue
		}
		src, err := marshalSource(part.rule)
		if err == nil {
			var changed bool
			changed, err = compileSRS(ctx, l.SingBox, part.path, src, part.name)
			if err == nil {
				if changed {
					rep.Written = append(rep.Written, part.path)
				} else {
					rep.Kept = append(rep.Kept, part.path)
				}
				keep[part.name] = true
				continue
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failed = err.Error()
		break
	}
	if failed != "" {
		fallback("编译规则集失败:" + failed)
		return nil
	}
	rep.Shapes[tag] = shape
	return nil
}

// splitEntries 把名单拆成域名一份、IP 一份(源码规则的字段);空的一份返回 nil。
// 非法的正则(sing-box 用的也是 Go 的 regexp 语法)丢掉并计数。
func splitEntries(entries []rulelist.Entry) (dom, ip map[string]any, dropped int) {
	d := map[string][]string{}
	var cidrs []string
	for _, e := range entries {
		switch e.Type {
		case rulelist.TypeDomainRegex:
			if _, err := regexp.Compile(e.Value); err != nil {
				dropped++
				continue
			}
			d[e.Type] = append(d[e.Type], e.Value)
		case rulelist.TypeDomain, rulelist.TypeDomainSuffix, rulelist.TypeDomainKeyword:
			d[e.Type] = append(d[e.Type], e.Value)
		case rulelist.TypeIPCIDR:
			cidrs = append(cidrs, e.Value)
		}
	}
	if len(d) > 0 {
		dom = map[string]any{}
		for k, v := range d {
			dom[k] = v
		}
	}
	if len(cidrs) > 0 {
		ip = map[string]any{"ip_cidr": cidrs}
	}
	return dom, ip, dropped
}

// prune 删掉目录里不在 keep 里的 list-<8 位十六进制>[-ip].srs;别的文件一概不碰。
func (l ListFiles) prune(keep map[string]bool, rep *ListReport) {
	ents, err := os.ReadDir(l.Dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !listFileRe.MatchString(name) || keep[name] {
			continue
		}
		p := filepath.Join(l.Dir, name)
		if os.Remove(p) == nil {
			rep.Pruned = append(rep.Pruned, p)
		}
	}
}

// RuleListURLs 读档案里的 routing,返回全部规则集链接。
func (g *Generator) RuleListURLs(ctx context.Context) ([]string, error) {
	prof, err := g.profiles.Get(ctx)
	if err != nil {
		return nil, err
	}
	return CollectRuleURLs(prof["routing"]), nil
}

// ListedBuilder 是带「规则集链接准备」的 Builder:先拉名单编 .srs,再把形状交给 core 生成配置。
// 部署用它(会联网、会写文件);config/preview 和测速不用它,仍然用 Generator,不联网不落盘。
type ListedBuilder struct {
	Gen   *Generator
	Lists ListFiles
}

// Build 实现 Builder。规则集链接没处理好的(没有旧文件可沿用),原因记在 Built.ListIssues,部署据此报缺。
func (b ListedBuilder) Build(ctx context.Context) (*Built, error) {
	urls, err := b.Gen.RuleListURLs(ctx)
	if err != nil {
		return nil, err
	}
	rep, err := b.Lists.Sync(ctx, urls)
	if err != nil {
		return nil, err
	}
	built, err := b.Gen.BuildWith(ctx, &Override{RuleLists: rep.Shapes})
	if err != nil {
		return nil, err
	}
	built.ListIssues = map[string]string{}
	for _, is := range rep.Issues {
		if !is.UsedOld {
			built.ListIssues[is.Tag] = is.Reason
		}
	}
	return built, nil
}
