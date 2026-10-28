// Package subshare 管理「订阅分享」:把若干订阅合成一条对外链接(/sub/<token>)给别的设备用。
// 本包只管分享记录的增删改查和 token;链接背后下发什么内容是另一件事,不在这里。
// 字段取自前端 api/openbox.ts 的 OpenboxSubscriptionShare 和 SubscriptionShareCard.vue 的用法,没有参照上游:
//   - token 是链接里的口令:新建时前端会带一个草稿 token,合格且没被占用才用,否则换一个(前端会提示以列表里的为准);
//   - 编辑时 regenerate 为真才换 token,不传就保持原链接;启停只改 enabled;
//   - host / protocol 只用来拼链接给人看(host 为空 = 面板当前的地址),这里只做格式检查。
package subshare

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	fileName = "subscription-shares"
	// MaxShares 最多几条分享。
	MaxShares = 50
	// MaxSubscriptions 一条分享最多选几份订阅。
	MaxSubscriptions = 200
	maxNameLen       = 80
	maxHostLen       = 255
	minTokenLen      = 32
	maxTokenLen      = 128
)

// Share 是一条订阅分享。
type Share struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Host            string   `json:"host"`
	Protocol        string   `json:"protocol"` // "http" | "https" | ""
	Enabled         bool     `json:"enabled"`
	Token           string   `json:"token"`
	SubscriptionIDs []string `json:"subscriptionIds"`
	CreatedAt       int64    `json:"createdAt"`
	UpdatedAt       int64    `json:"updatedAt"`
}

// Input 是创建 / 修改的请求。指针为 nil = 没传(修改时保持原样,创建时按默认)。
type Input struct {
	Name            *string
	Host            *string
	Protocol        *string
	SubscriptionIDs *[]string
	Token           *string // 只在创建时看
	Enabled         *bool   // 只在修改时看;创建时恒为开
	Regenerate      bool    // 只在修改时看
}

// InputError 是请求的内容有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// ErrNotFound 是没有这条分享(404)。
var ErrNotFound = errors.New("没有这条订阅分享")

type file struct {
	Shares []Share `json:"shares"`
}

// Service 读写订阅分享。
type Service struct {
	Store *store.Store
	// SubscriptionIDs 返回当前存在的订阅 id,用来检查新选的订阅是否存在;nil 时不检查。
	SubscriptionIDs func(ctx context.Context) ([]string, error)
	Now             func() time.Time
	// NewToken 生成 token,测试里换成固定的;nil 用 24 字节随机数的十六进制(48 个字符,与前端草稿同样的长度)。
	NewToken func() string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) token() string {
	if s.NewToken != nil {
		return s.NewToken()
	}
	return randHex(24)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("读随机数失败: %v", err)) // 读不到系统随机数时不能退而求其次地造 token
	}
	return hex.EncodeToString(b)
}

// List 返回全部分享(没有是空切片)。
func (s *Service) List(context.Context) ([]Share, error) {
	f := &file{}
	if _, err := s.Store.Load(fileName, f); err != nil {
		return nil, fmt.Errorf("读取订阅分享失败: %w", err)
	}
	if f.Shares == nil {
		f.Shares = []Share{}
	}
	return f.Shares, nil
}

// Create 新建一条分享。name 和 subscriptionIds 必填。
func (s *Service) Create(ctx context.Context, in Input) (Share, error) {
	if in.Name == nil || in.SubscriptionIDs == nil {
		return Share{}, &InputError{"名称和订阅都要填"}
	}
	name, err := checkName(*in.Name)
	if err != nil {
		return Share{}, err
	}
	sh := Share{Name: name, Enabled: true}
	if in.Host != nil {
		if sh.Host, err = checkHost(*in.Host); err != nil {
			return Share{}, err
		}
	}
	if in.Protocol != nil {
		if sh.Protocol, err = checkProtocol(*in.Protocol); err != nil {
			return Share{}, err
		}
	}
	if sh.SubscriptionIDs, err = s.checkSubs(ctx, *in.SubscriptionIDs, nil); err != nil {
		return Share{}, err
	}
	var out Share
	err = s.mutate(func(f *file) (bool, error) {
		if len(f.Shares) >= MaxShares {
			return false, &InputError{fmt.Sprintf("最多 %d 条分享", MaxShares)}
		}
		sh.ID = uniqueID(f)
		sh.Token = pickToken(f, in.Token, s.token)
		sh.CreatedAt = s.now().UnixMilli()
		sh.UpdatedAt = sh.CreatedAt
		f.Shares = append(f.Shares, sh)
		out = sh
		return true, nil
	})
	return out, err
}

// Update 修改一条分享:传了的字段才改;regenerate 为真换一个新 token(旧链接立即失效)。没有实际变化不写盘。
// 新选的订阅必须存在;这条分享里已经有的订阅 id 即使订阅后来被删了也照旧保留(否则启停会被卡住)。
func (s *Service) Update(ctx context.Context, id string, in Input) (Share, error) {
	var out Share
	cur, err := s.find(ctx, id)
	if err != nil {
		return Share{}, err
	}
	var subs []string
	if in.SubscriptionIDs != nil {
		if subs, err = s.checkSubs(ctx, *in.SubscriptionIDs, cur.SubscriptionIDs); err != nil {
			return Share{}, err
		}
	}
	err = s.mutate(func(f *file) (bool, error) {
		i := indexOf(f, id)
		if i < 0 {
			return false, ErrNotFound
		}
		sh := f.Shares[i]
		old := f.Shares[i]
		var err error
		if in.Name != nil {
			if sh.Name, err = checkName(*in.Name); err != nil {
				return false, err
			}
		}
		if in.Host != nil {
			if sh.Host, err = checkHost(*in.Host); err != nil {
				return false, err
			}
		}
		if in.Protocol != nil {
			if sh.Protocol, err = checkProtocol(*in.Protocol); err != nil {
				return false, err
			}
		}
		if in.SubscriptionIDs != nil {
			sh.SubscriptionIDs = subs
		}
		if in.Enabled != nil {
			sh.Enabled = *in.Enabled
		}
		if in.Regenerate {
			sh.Token = pickToken(f, nil, s.token)
		}
		out = sh
		if reflect.DeepEqual(sh, old) {
			return false, nil
		}
		sh.UpdatedAt = s.now().UnixMilli()
		f.Shares[i] = sh
		out = sh
		return true, nil
	})
	return out, err
}

// Regenerate 给一条分享换新 token。
func (s *Service) Regenerate(ctx context.Context, id string) (Share, error) {
	return s.Update(ctx, id, Input{Regenerate: true})
}

// Delete 删除一条分享。没有这条返回 ErrNotFound。
func (s *Service) Delete(_ context.Context, id string) error {
	return s.mutate(func(f *file) (bool, error) {
		i := indexOf(f, id)
		if i < 0 {
			return false, ErrNotFound
		}
		f.Shares = slices.Delete(f.Shares, i, i+1)
		return true, nil
	})
}

func (s *Service) find(ctx context.Context, id string) (Share, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Share{}, err
	}
	if i := slices.IndexFunc(all, func(x Share) bool { return x.ID == id }); i >= 0 {
		return all[i], nil
	}
	return Share{}, ErrNotFound
}

var errNoChange = errors.New("no change")

// mutate 在锁里读改写;fn 返回 false(没变化)不写盘——路由器的闪存经不起无谓的写。
func (s *Service) mutate(fn func(f *file) (bool, error)) error {
	f := &file{}
	err := s.Store.Update(fileName, f, func() error {
		changed, err := fn(f)
		if err != nil {
			return err
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

func indexOf(f *file, id string) int {
	return slices.IndexFunc(f.Shares, func(x Share) bool { return x.ID == id })
}

func uniqueID(f *file) string {
	for {
		id := randHex(8)
		if indexOf(f, id) < 0 {
			return id
		}
	}
}

func tokenUsed(f *file, t string) bool {
	return slices.ContainsFunc(f.Shares, func(x Share) bool { return x.Token == t })
}

// pickToken 优先用客户端给的 token(长度和字符都合格、且没被占用),否则自己生成一个没被占用的。
// 太短的口令谁都猜得到,所以不合格的不收,而是悄悄换一个(前端会提示以列表里的链接为准)。
func pickToken(f *file, want *string, gen func() string) string {
	if want != nil && validToken(*want) && !tokenUsed(f, *want) {
		return *want
	}
	for {
		if t := gen(); !tokenUsed(f, t) {
			return t
		}
	}
}

func validToken(t string) bool {
	if len(t) < minTokenLen || len(t) > maxTokenLen {
		return false
	}
	for _, c := range t {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func checkName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" {
		return "", &InputError{"名称不能为空"}
	}
	if len([]rune(n)) > maxNameLen {
		return "", &InputError{fmt.Sprintf("名称最长 %d 个字", maxNameLen)}
	}
	return n, nil
}

func checkProtocol(p string) (string, error) {
	switch p {
	case "http", "https", "":
		return p, nil
	}
	return "", &InputError{"协议只能是 http 或 https"}
}

// checkHost 只收「主机」或「主机:端口」(IPv6 用方括号),不收协议、路径、账号、空白。空串合法 = 用面板当前的地址。
func checkHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", nil
	}
	bad := &InputError{"主机地址不对,只能写「域名或 IP」或「域名或 IP:端口」"}
	if len(h) > maxHostLen || strings.ContainsAny(h, "/?#@\\ \t\r\n") {
		return "", bad
	}
	u, err := url.Parse("http://" + h)
	if err != nil || u.Host != h || u.Hostname() == "" || u.User != nil || u.Path != "" {
		return "", bad
	}
	return h, nil
}

// checkSubs 规整订阅 id:去空白、去重、保序;不能为空;不在 keep 里的 id 必须是现存的订阅。
func (s *Service) checkSubs(ctx context.Context, ids []string, keep []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, &InputError{"至少选一份订阅"}
	}
	if len(out) > MaxSubscriptions {
		return nil, &InputError{fmt.Sprintf("一条分享最多选 %d 份订阅", MaxSubscriptions)}
	}
	if s.SubscriptionIDs == nil {
		return out, nil
	}
	exist, err := s.SubscriptionIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取订阅列表失败: %w", err)
	}
	for _, id := range out {
		if !slices.Contains(exist, id) && !slices.Contains(keep, id) {
			return nil, &InputError{"选择的订阅里有不存在的"}
		}
	}
	return out, nil
}
