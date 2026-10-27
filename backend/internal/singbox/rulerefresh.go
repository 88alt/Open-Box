package singbox

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

// 规则集链接「立即更新」(/api/openbox/rulesets/refresh):名单平时随部署才重拉,自己维护名单的人改完想立刻生效,
// 就对这一个链接重拉重编。自己的设计,依据前端 refreshRuleList / OpenboxRulesetRefreshResult 的注释,没有参照上游。
//
// 与部署时的 ListFiles.Sync 的区别:
//   - 只处理这一个链接,不清理别的 list-*.srs(Sync 会把「没被引用」的都删掉,这里的链接可能还没保存进档案);
//   - 一定重新联网拉取(跳过 60 秒缓存);
//   - 拉取 / 编译失败直接报错,不像部署那样悄悄沿用旧文件——用户点了按钮,就要知道没成;失败时旧文件原样保留。
//
// 不重启也不部署:只改规则集文件。是否需要重启只看「构成」有没有变——档案里正在用的这个链接,
// 编出来的文件从有 / 无 IP 份、域名份变成了另一种组合,配置里引用的规则集名就变了,要重新生成配置并重启;
// 构成没变,只是条目变了,只有文件内容更新。内核是否会热读更新后的本地规则集文件,要真机确认(见 STATUS)。

// URLSource 给出档案里正在用的规则集链接(*Generator 满足)。
type URLSource interface {
	RuleListURLs(ctx context.Context) ([]string, error)
}

// RefreshInputError 是链接本身不合格(400)。
type RefreshInputError struct{ Msg string }

func (e *RefreshInputError) Error() string { return e.Msg }

// RefreshBuildError 是名单拉到了,但编译落盘没成(500)。旧文件保持不动。
type RefreshBuildError struct{ Msg string }

func (e *RefreshBuildError) Error() string { return e.Msg }

// RefreshResult 的 JSON 形状对应前端 OpenboxRulesetRefreshResult(url / tag / total / counts / needsRestart),
// 多出的 changed / dropped / referenced 前端用不到,方便排查。
type RefreshResult struct {
	URL          string         `json:"url"`
	Tag          string         `json:"tag"`
	Total        int            `json:"total"`  // 名单总条数(与 rulesets/preview 的 total 同口径,含被丢掉的非法正则)
	Counts       map[string]int `json:"counts"` // 按条目类型的条数:domain / domain_suffix / domain_keyword / domain_regex / ip_cidr
	NeedsRestart bool           `json:"needsRestart"`
	Changed      bool           `json:"changed"`    // 有规则集文件被改写或删除
	Dropped      int            `json:"dropped"`    // 被丢掉的非法正则条数
	Referenced   bool           `json:"referenced"` // 这个链接目前在档案里被站点集 / 前置自定义分流引用
}

// Reloader 重新拉取名单,不走缓存(*rulelist.Service 满足)。
type Reloader interface {
	Reload(ctx context.Context, rawURL string) (*rulelist.Result, error)
}

// Refresher 对单个规则集链接做重拉重编。
type Refresher struct {
	Lists ListFiles // 落盘用;Lists.Rules 在这里不用,拉取走 Fetch
	Fetch Reloader
	URLs  URLSource // 为 nil 时一律当作「没被引用」
	mu    sync.Mutex
}

// fixedLoader 把已经拉到的结果交给 syncOne,免得再拉一次。
type fixedLoader struct{ res *rulelist.Result }

func (f fixedLoader) Load(context.Context, string) (*rulelist.Result, error) { return f.res, nil }

// Refresh 重拉重编一个链接。错误:*RefreshInputError(链接不合格)、拉取 / 解析的错误原样交回
// (*rulelist.FetchError / *rulelist.InputError / rulelist.ErrBinary / rulelist.ErrEmpty,调用方按类型映射)、
// ErrNoSingBox、*RefreshBuildError、ctx 的取消错误。同一时刻只做一次(规则集目录里的文件不并发改)。
func (r *Refresher) Refresh(ctx context.Context, rawURL string) (*RefreshResult, error) {
	u := strings.TrimSpace(rawURL)
	low := strings.ToLower(u)
	if u == "" {
		return nil, &RefreshInputError{Msg: "缺少规则集链接"}
	}
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return nil, &RefreshInputError{Msg: "规则集链接只支持 http / https"}
	}
	if r.Fetch == nil {
		return nil, &RefreshBuildError{Msg: "没有配置规则集链接的拉取服务"}
	}
	tag := policyentries.ListTagForURL(u)
	if tag == "" {
		return nil, &RefreshInputError{Msg: "规则集链接不合格"}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	before := ListShape{Domain: hasSRS(filepath.Join(r.Lists.Dir, tag+".srs")), IP: hasSRS(filepath.Join(r.Lists.Dir, tag+"-ip.srs"))}

	res, err := r.Fetch.Reload(ctx, u)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, e := range res.Entries {
		counts[e.Type]++
	}

	l := r.Lists
	l.Rules = fixedLoader{res: res}
	rep := newListReport()
	if err := l.syncOne(ctx, u, tag, rep, map[string]bool{}); err != nil {
		return nil, err
	}
	// 失败:syncOne 在「编译失败」时会沿用旧文件并记 Issue,这里不当作成功。
	for _, is := range rep.Issues {
		if is.Tag != tag {
			continue
		}
		if strings.Contains(is.Reason, ErrNoSingBox.Error()) {
			return nil, ErrNoSingBox
		}
		return nil, &RefreshBuildError{Msg: strings.TrimSuffix(is.Reason, "(沿用上次编好的文件)") + "(原有的规则集文件没有改动)"}
	}
	after := rep.Shapes[tag]
	if !after.Domain && !after.IP {
		msg := "名单里没有可用的条目(正则都不合法?),没有生成规则集文件"
		if len(rep.Pruned) > 0 {
			msg += "(旧的规则集文件已移除)"
		}
		return nil, &RefreshBuildError{Msg: msg}
	}

	referenced := false
	if r.URLs != nil {
		urls, err := r.URLs.RuleListURLs(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("读取档案失败: " + err.Error())
		}
		for _, x := range urls {
			if policyentries.ListTagForURL(x) == tag {
				referenced = true
				break
			}
		}
	}
	return &RefreshResult{
		URL: u, Tag: tag, Total: len(res.Entries), Counts: counts,
		NeedsRestart: referenced && before != after,
		Changed:      len(rep.Written) > 0 || len(rep.Pruned) > 0,
		Dropped:      rep.Dropped[tag],
		Referenced:   referenced,
	}, nil
}
