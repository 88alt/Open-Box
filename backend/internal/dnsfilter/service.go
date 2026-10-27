package dnsfilter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// 「应用」的流程(自己的设计,依据 DESIGN 第四节「变更安全」和前端 applyDnsFilter / DnsFilterStatus 的类型,没有参照上游):
//
//  1. 读设置;内核服务的状态读不到就一点都不动。
//  2. 启用时逐份取名单:要更新的(update=true,或指定了 listId 的那一份)和没有缓存的现拉,拉失败就沿用上次的缓存,
//     连缓存都没有的那份记错误、略过;拉下来的内容解析成功才覆盖缓存。
//  3. 编成产物 → 规则集 .srs 落盘(只写不删)→ 把产物存进状态文件。
//  4. 内核没在运行就到此为止(返回 ErrNotRunning):不会为了应用名单把代理启动起来。
//  5. 走部署(校验 → 原子替换 → 重启 → 健康检查 → 失败回滚)。成功了才清理不再引用的旧 .srs、记「已应用」
//     (回滚时旧配置还要用旧文件,所以清理不能早)。

// StateFile 是状态文件名(存在 store 的目录里)。
const StateFile = "dns-filter"

// ErrBusy:已有一次应用在进行。
var ErrBusy = errors.New("已有一次应用正在进行,请稍后再试")

// ErrNoUsableList:启用了过滤,但一份能用的名单都没有。
var ErrNoUsableList = errors.New("没有可用的名单:请先添加并启用至少一份名单,并确认能下载")

// ErrNotRunning:名单已更新并保存,但内核没在运行,没有应用。
var ErrNotRunning = errors.New("名单已更新并保存,但内核没有在运行,没有应用;启动内核后再点一次应用")

// ApplyError 是部署没成功:系统已按部署流程回滚(或根本没动),Msg 是给人看的原因。
type ApplyError struct{ Msg string }

func (e *ApplyError) Error() string { return e.Msg }

type listState struct {
	URL                 string   `json:"url"`
	Count               int      `json:"count"`
	Unsupported         int      `json:"unsupported"`
	UnsupportedExamples []string `json:"unsupportedExamples,omitempty"`
	UpdatedAt           int64    `json:"updatedAt"` // 毫秒
	Error               string   `json:"error,omitempty"`
}

type state struct {
	Lists    map[string]listState `json:"lists"`
	Artifact map[string]any       `json:"artifact,omitempty"` // 交给 core 的 dnsFilter
	Applied  *Applied             `json:"applied,omitempty"`
}

// ProfileReader 读完整档案(*profile.Service 满足)。
type ProfileReader interface {
	Get(ctx context.Context) (map[string]any, error)
}

// Service 管名单的下载、应用和状态。
type Service struct {
	Profiles ProfileReader
	Store    *store.Store
	CacheDir string  // 名单原文缓存目录
	RulesDir string  // 规则集目录(过滤产物的 .srs 放这里)
	Fetch    Fetcher // 下载名单;nil 时只能用缓存
	// Write 把各组编成 <dir>/<tag>.srs(已有的跳过,不删任何文件)。
	Write func(ctx context.Context, dir string, groups []Group) error
	// Prune 删掉 dir 里不在 groups 里的 dns-filter-*.srs;只在新配置已成功应用后调用,可为 nil。
	Prune func(dir string, groups []Group)
	// Running 内核服务是否在运行;读不到返回 error。
	Running func(ctx context.Context) (bool, error)
	// Deploy 走一次完整部署;失败返回 *ApplyError。
	Deploy func(ctx context.Context) error
	Now    func() time.Time

	busy atomic.Bool
}

func (sv *Service) now() time.Time {
	if sv.Now != nil {
		return sv.Now()
	}
	return time.Now()
}

func (sv *Service) load() (*state, error) {
	st := &state{}
	if sv.Store != nil {
		if _, err := sv.Store.Load(StateFile, st); err != nil {
			return nil, fmt.Errorf("读取名单状态失败: %w", err)
		}
	}
	if st.Lists == nil {
		st.Lists = map[string]listState{}
	}
	return st, nil
}

// update 在 store 的锁里读改写状态文件(应用期间别的请求读到的总是完整的一份)。
func (sv *Service) update(fn func(st *state)) error {
	if sv.Store == nil {
		return nil
	}
	st := &state{}
	return sv.Store.Update(StateFile, st, func() error {
		if st.Lists == nil {
			st.Lists = map[string]listState{}
		}
		fn(st)
		return nil
	})
}

func pending(s Settings, ap *Applied) bool {
	if ap == nil {
		return s.Enabled
	}
	if ap.Enabled != s.Enabled {
		return true
	}
	return s.Enabled && ap.Key != SettingsKey(s)
}

// Status 拼出前端要的状态。lists 只含设置里现有的名单;connected(查询日志)还没接。
func (sv *Service) Status(ctx context.Context) (Status, error) {
	prof, err := sv.Profiles.Get(ctx)
	if err != nil {
		return Status{}, err
	}
	s := FromProfile(prof)
	st, err := sv.load()
	if err != nil {
		return Status{}, err
	}
	out := BuildStatus(s)
	for _, l := range s.Lists {
		if ls, ok := st.Lists[l.ID]; ok && ls.URL == l.URL {
			out.Lists[l.ID] = ListStat{Count: ls.Count, Unsupported: ls.Unsupported, UnsupportedExamples: ls.UnsupportedExamples, UpdatedAt: ls.UpdatedAt, Error: ls.Error}
		}
	}
	out.Pending = pending(s, st.Applied)
	out.Applied = st.Applied
	out.Busy = sv.busy.Load()
	return out, nil
}

// CoreArtifact 给配置生成用:启用过滤时返回要传给 core 的 dnsFilter 和期望的 key。
// ready 为假表示还没有按当前设置应用过(没有产物,或产物是按别的设置编的):调用方应当当作「过滤还没生效」,
// 不要把半截的东西交给 core(core 在这种情况下会拒绝生成配置)。没启用过滤时 enabled 为假,其余返回值无意义。
func (sv *Service) CoreArtifact(ctx context.Context, prof map[string]any) (artifact map[string]any, key string, enabled, ready bool, err error) {
	s := FromProfile(prof)
	if !s.Enabled {
		return nil, "", false, true, nil
	}
	key = SettingsKey(s)
	st, err := sv.load()
	if err != nil {
		return nil, "", true, false, err
	}
	if st.Artifact == nil || st.Artifact["key"] != key {
		return nil, key, true, false, nil
	}
	return st.Artifact, key, true, true, nil
}

func (sv *Service) cachePath(id string) string {
	return filepath.Join(sv.CacheDir, "list-"+id+".txt")
}

// Apply 对应 POST /api/openbox/dns-filter/apply。update 为真是重新下载全部启用的名单,listID 非空只重新下载那一份。
func (sv *Service) Apply(ctx context.Context, update bool, listID string) (Status, error) {
	if !sv.busy.CompareAndSwap(false, true) {
		return Status{}, ErrBusy
	}
	err := sv.apply(ctx, update, listID)
	sv.busy.Store(false) // 先放开再读状态:返回的状态里 busy 是假
	if err != nil {
		return Status{}, err
	}
	return sv.Status(ctx)
}

func (sv *Service) apply(ctx context.Context, update bool, listID string) error {
	prof, err := sv.Profiles.Get(ctx)
	if err != nil {
		return err
	}
	s := FromProfile(prof)
	if listID != "" {
		found := false
		for _, l := range s.Lists {
			found = found || l.ID == listID
		}
		if !found {
			return &InputError{Msg: "没有这份名单: " + listID}
		}
	}
	if sv.Running == nil || sv.Deploy == nil || sv.Write == nil {
		return errors.New("名单应用没有配置完整(缺少服务状态、部署或规则集落盘)")
	}
	// 先摸清现状:读不到服务状态就什么都不动
	running, err := sv.Running(ctx)
	if err != nil {
		return &ApplyError{Msg: "读不到内核服务的状态,为安全起见没有改动:" + err.Error()}
	}

	art := &Artifact{Key: SettingsKey(s), Groups: []Group{}}
	stats := map[string]listState{}
	if s.Enabled {
		parsed := map[string]*Parsed{}
		for _, l := range s.Lists {
			if !l.Enabled {
				continue
			}
			force := update && (listID == "" || listID == l.ID)
			p, ls, err := sv.loadList(ctx, l, force)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_ = err // 失败原因已记在 ls.Error 里
			stats[l.ID] = ls
			if p != nil {
				parsed[l.ID] = p
			}
		}
		art = Compile(s, parsed)
		if len(art.Blocks) == 0 {
			// 一份能拦东西的名单都没有:不要部署一个空过滤,也不要覆盖上次的产物
			_ = sv.update(func(st *state) { mergeStats(st, stats) })
			return ErrNoUsableList
		}
	}
	if err := sv.Write(ctx, sv.RulesDir, art.Groups); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("编译名单规则集失败: %w", err)
	}
	var core map[string]any
	if s.Enabled {
		core = art.ForCore(sv.RulesDir)
	}
	if err := sv.update(func(st *state) { mergeStats(st, stats); st.Artifact = core }); err != nil {
		return fmt.Errorf("保存名单状态失败: %w", err)
	}
	if !running {
		return ErrNotRunning
	}
	if err := sv.Deploy(ctx); err != nil {
		return err
	}
	if sv.Prune != nil {
		sv.Prune(sv.RulesDir, art.Groups)
	}
	if err := sv.update(func(st *state) { st.Applied = &Applied{Enabled: s.Enabled, Key: art.Key} }); err != nil {
		return fmt.Errorf("已应用,但记录状态失败: %w", err)
	}
	return nil
}

func mergeStats(st *state, stats map[string]listState) {
	for id, ls := range stats {
		st.Lists[id] = ls
	}
}

// loadList 取一份名单的解析结果和统计。返回的 error 只是失败原因(已记在统计的 Error 里);
// 返回的 *Parsed 为 nil 表示这份名单没有可用数据。
func (sv *Service) loadList(ctx context.Context, l List, force bool) (*Parsed, listState, error) {
	prev := listState{}
	if st, err := sv.load(); err == nil {
		if ls, ok := st.Lists[l.ID]; ok && ls.URL == l.URL {
			prev = ls
		}
	}
	var cached *Parsed
	if prev.URL == l.URL {
		if b, err := os.ReadFile(sv.cachePath(l.ID)); err == nil {
			if p, err := ParseList(b); err == nil {
				cached = p
			}
		}
	}
	if cached != nil && !force {
		prev.Error = ""
		return cached, prev, nil
	}
	fail := func(msg string) (*Parsed, listState, error) {
		if cached != nil {
			prev.Error = msg + "(沿用上次下载的内容)"
			return cached, prev, errors.New(msg)
		}
		return nil, listState{URL: l.URL, Error: msg}, errors.New(msg)
	}
	if sv.Fetch == nil {
		return fail("没有配置名单下载服务")
	}
	body, err := sv.Fetch(ctx, l.URL)
	if err != nil {
		return fail("下载失败:" + errText(err))
	}
	p, err := ParseList(body)
	if err != nil {
		return fail("名单解析失败:" + err.Error())
	}
	if len(p.Entries) == 0 {
		// 内容不是名单(比如返回了网页)或者全是不支持的写法:不能拿它覆盖缓存、也不能当成一份空名单
		return fail(fmt.Sprintf("名单里没有一条能用的规则(%d 条写法不支持)", p.Unsupported))
	}
	if old, err := os.ReadFile(sv.cachePath(l.ID)); err != nil || !bytes.Equal(old, body) {
		if err := os.MkdirAll(sv.CacheDir, 0o700); err != nil {
			return fail("缓存名单失败:" + err.Error())
		}
		if err := store.WriteFileAtomic(sv.cachePath(l.ID), body, 0o600); err != nil {
			return fail("缓存名单失败:" + err.Error())
		}
	}
	return p, listState{
		URL: l.URL, Count: len(p.Entries), Unsupported: p.Unsupported,
		UnsupportedExamples: p.UnsupportedExamples, UpdatedAt: sv.now().UnixMilli(),
	}, nil
}

// errText 取错误里给人看的那部分:下载错误(rulelist.FetchError / InputError)的文字本身就是中文说明,且不带地址里的 token。
func errText(err error) string { return strings.TrimSpace(err.Error()) }

// InputError 是请求本身有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }
