package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// 配置里的 route.rule_set 会引用一批本地文件(type=local)。sing-box 启动和 `sing-box check` 都要求它们存在,
// 所以部署之前要先把能生成的文件落盘。这里按 tag 把它们分成几类:
//
//	node-direct  obnode-direct / obnode-direct-ip:订阅节点的服务器域名 / 地址,走直连(源码 JSON,内容来自 Built.DirectHosts)
//	flip-flag    obflip-<id> / obflip-fallback:站点集「此刻走直连还是走代理」的开关(源码 JSON,内容取决于路由状态)
//	flip-dynamic obflip-byp-* / obflip-need-*:热切换的动态集(二进制 .srs,初始是占位,之后由热切换功能改写)
//	ruleset      其余(geoip-* / geosite-* / 用户规则集链接编成的 .srs):内容来自规则库或用户的名单
//
// 本文件负责前两类里内容完全确定的部分:node-direct 和 flip-dynamic 的占位。其余的在 Missing 里如实报告。
const (
	KindNodeDirect  Kind = "node-direct"
	KindFlipFlag    Kind = "flip-flag"
	KindFlipDynamic Kind = "flip-dynamic"
	KindRuleset     Kind = "ruleset"

	nodeDirectDomainTag = "obnode-direct"
	nodeDirectIPTag     = "obnode-direct-ip"
	flipPrefix          = "obflip-"
	flipBypassPrefix    = "obflip-byp-"
	flipNeedPrefix      = "obflip-need-"

	// 空名单的占位(文档保留的域名 / 地址,真实流量碰不到):规则集不能真空,内核不建集合,之后再填就报错。
	placeholderDomain = "obnode-direct.invalid"
	placeholderCIDR4  = "192.0.2.255/32"
	placeholderCIDR6  = "2001:db8:ffff::ffff/128"

	compileTimeout = 30 * time.Second
	// 规则集源码里的 version:和内核 1.13 / 1.14 都认的 3 一致。
	ruleSetSourceVersion = 3
)

// Kind 是本地规则集文件的类别。
type Kind string

// LocalSet 是配置里一条本地规则集。
type LocalSet struct {
	Tag    string
	Format string // source / binary
	Path   string
}

// LocalSets 按出现顺序列出 cfg.route.rule_set 里的本地条目(同 tag 只留第一条)。
func LocalSets(cfg map[string]any) []LocalSet {
	route, _ := cfg["route"].(map[string]any)
	list, _ := route["rule_set"].([]any)
	seen := map[string]bool{}
	out := []LocalSet{}
	for _, o := range list {
		m, _ := o.(map[string]any)
		if m == nil || m["type"] != "local" {
			continue
		}
		tag, _ := m["tag"].(string)
		path, _ := m["path"].(string)
		format, _ := m["format"].(string)
		if tag == "" || path == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, LocalSet{Tag: tag, Format: format, Path: path})
	}
	return out
}

// Classify 按 tag 判断类别。
func Classify(s LocalSet) Kind {
	switch {
	case s.Tag == nodeDirectDomainTag || s.Tag == nodeDirectIPTag:
		return KindNodeDirect
	case strings.HasPrefix(s.Tag, flipBypassPrefix) || strings.HasPrefix(s.Tag, flipNeedPrefix):
		return KindFlipDynamic
	case strings.HasPrefix(s.Tag, flipPrefix):
		return KindFlipFlag
	}
	return KindRuleset
}

// Missing 是没能落盘的本地规则集,以及原因。
type Missing struct {
	Tag    string `json:"tag"`
	Kind   Kind   `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// FilesReport 是一次落盘的结果。
type FilesReport struct {
	Written []string  `json:"written"` // 本次新写或改写的文件
	Kept    []string  `json:"kept"`    // 已存在、按约定保留不动的文件(热切换的动态集)
	Missing []Missing `json:"missing"` // 没能处理的:后面的部署步骤据此决定能不能继续
}

// OK 表示配置引用的本地规则集都已就位。
func (r *FilesReport) OK() bool { return len(r.Missing) == 0 }

// Files 把配置引用的本地规则集落盘。
type Files struct {
	SingBox string // sing-box 可执行文件,编译占位 .srs 用;空 = "sing-box"
}

// WriteLocalSets 处理 cfg 引用的本地规则集:
//   - node-direct:每次按 direct(Built.DirectHosts)改写,没有地址时写占位;
//   - flip-dynamic:不存在才写占位(用 sing-box 编译),已存在的不动,免得覆盖热切换写进去的内容;
//   - flip-flag、ruleset:这里生成不了,报在 Missing 里(原因写明白)。
//
// 单个文件失败不会中断其它文件,都记在 Missing 里;只有 ctx 取消这类整体性错误才返回 error。
func (f Files) WriteLocalSets(ctx context.Context, cfg map[string]any, direct any) (*FilesReport, error) {
	rep := &FilesReport{Written: []string{}, Kept: []string{}, Missing: []Missing{}}
	domains, cidrs := directHostLists(direct)
	for _, s := range LocalSets(cfg) {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		kind := Classify(s)
		miss := func(reason string) {
			rep.Missing = append(rep.Missing, Missing{Tag: s.Tag, Kind: kind, Path: s.Path, Reason: reason})
		}
		switch kind {
		case KindNodeDirect:
			if s.Format != "source" {
				miss("格式应为 source,配置里是 " + s.Format)
				continue
			}
			data, err := nodeDirectSource(s.Tag, domains, cidrs)
			if err != nil {
				miss(err.Error())
				continue
			}
			if err := writeFile(s.Path, data); err != nil {
				miss(err.Error())
				continue
			}
			rep.Written = append(rep.Written, s.Path)
		case KindFlipDynamic:
			if s.Format != "binary" {
				miss("格式应为 binary,配置里是 " + s.Format)
				continue
			}
			if _, err := os.Stat(s.Path); err == nil {
				rep.Kept = append(rep.Kept, s.Path)
				continue
			}
			if err := f.writePlaceholderSRS(ctx, s.Path); err != nil {
				miss(err.Error())
				continue
			}
			rep.Written = append(rep.Written, s.Path)
		case KindFlipFlag:
			miss("热切换开关的内容取决于每个站点集此刻走直连还是走代理,需要 core 提供开关状态(见 STATUS.md「需要 core 配合」)")
		default:
			miss("规则库 / 用户规则集的 .srs 还没有生成来源(依赖随包规则库和 rulesets entries)")
		}
	}
	return rep, nil
}

// directHostLists 从 core 返回的 directHosts({domains, cidrs} 或 null)取出两份名单。
func directHostLists(v any) (domains, cidrs []string) {
	m, _ := v.(map[string]any)
	pick := func(key string) []string {
		list, _ := m[key].([]any)
		out := []string{}
		for _, x := range list {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return pick("domains"), pick("cidrs")
}

type ruleSetSource struct {
	Version int              `json:"version"`
	Rules   []map[string]any `json:"rules"`
}

func marshalSource(rule map[string]any) ([]byte, error) {
	return json.Marshal(ruleSetSource{Version: ruleSetSourceVersion, Rules: []map[string]any{rule}})
}

// nodeDirectSource 生成 obnode-direct / obnode-direct-ip 的源码 JSON;名单为空时用占位。
func nodeDirectSource(tag string, domains, cidrs []string) ([]byte, error) {
	switch tag {
	case nodeDirectDomainTag:
		if len(domains) == 0 {
			domains = []string{placeholderDomain}
		}
		return marshalSource(map[string]any{"domain": domains})
	case nodeDirectIPTag:
		if len(cidrs) == 0 {
			cidrs = []string{placeholderCIDR4}
		}
		return marshalSource(map[string]any{"ip_cidr": cidrs})
	}
	return nil, fmt.Errorf("不认识的 node-direct 规则集 %q", tag)
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	if err := store.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("写文件失败: %w", err)
	}
	return nil
}

// writePlaceholderSRS 用 `sing-box rule-set compile` 把占位源码编成 .srs 再原子放到 path。
// 用已安装的内核编,得到的二进制格式版本和它一致。
func (f Files) writePlaceholderSRS(ctx context.Context, path string) error {
	bin := f.SingBox
	if bin == "" {
		bin = "sing-box"
	}
	exe, err := exec.LookPath(bin)
	if err != nil {
		return ErrNoSingBox
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	src, err := marshalSource(map[string]any{"ip_cidr": []string{placeholderCIDR4, placeholderCIDR6}})
	if err != nil {
		return err
	}
	in, err := os.CreateTemp(dir, ".compile-*.json")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	inName := in.Name()
	defer os.Remove(inName)
	if _, err := in.Write(src); err != nil {
		in.Close()
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := in.Close(); err != nil {
		return err
	}
	outName := inName + ".srs"
	defer os.Remove(outName)

	cctx, cancel := context.WithTimeout(ctx, compileTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, exe, "rule-set", "compile", "--output", outName, inName)
	lw := &limitedWriter{max: maxCheckOut}
	cmd.Stdout, cmd.Stderr = lw, lw
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return fmt.Errorf("sing-box rule-set compile 超时(%s)", compileTimeout)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg := strings.TrimSpace(lw.buf.String())
			if msg == "" {
				msg = ee.Error()
			}
			return fmt.Errorf("sing-box rule-set compile 失败: %s", strings.ReplaceAll(msg, inName, "placeholder.json"))
		}
		return fmt.Errorf("运行 sing-box 失败: %w", err)
	}
	data, err := os.ReadFile(outName)
	if err != nil {
		return fmt.Errorf("读编译结果失败: %w", err)
	}
	if len(data) == 0 {
		return errors.New("sing-box rule-set compile 没有产出文件")
	}
	return writeFile(path, data)
}
