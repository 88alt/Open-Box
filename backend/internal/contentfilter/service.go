package contentfilter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	fileName = "content-filter"
	// MaxSets 最多几份规则集。
	MaxSets = 50
	// MaxTextBytes 一份规则文本最大字节数(与前端上传文件的上限一致)。
	MaxTextBytes = 512 << 10
	maxNameRunes = 80
	maxURLLen    = 2048
	maxArgs      = 50
	maxArgName   = 64
	maxArgValue  = 1024
)

// idRe 是规则集 id 的合法形状(也是文本文件名的一部分,防路径穿越)。
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// InputError 是请求的内容有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// ErrNotFound 是没有这份规则集(404)。
var ErrNotFound = errors.New("没有这份内容过滤规则")

// Arguments 是规则集的参数值({{{名字}}} 的替换值)。
type Arguments struct {
	Values map[string]any `json:"values,omitempty"`
}

// Set 是一份规则集的记录(文本另存文件,不在这里)。
type Set struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Dialect     Dialect    `json:"dialect"`
	SourceURL   string     `json:"sourceUrl,omitempty"`
	Enabled     bool       `json:"enabled"`
	RuleCount   int        `json:"ruleCount"`
	ScriptCount int        `json:"scriptCount"`
	Skipped     int        `json:"skipped,omitempty"`
	UpdatedAt   string     `json:"updatedAt"`
	Arguments   *Arguments `json:"arguments,omitempty"`
}

// List 是整份列表;sha256 / size 对「启用的集合按 id 排序后的规范化内容」算。
type List struct {
	Sets   []Set  `json:"sets"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

type file struct {
	Sets []Set `json:"sets"`
}

// SaveInput 是新建 / 更新的请求。
type SaveInput struct {
	ID        string // 空 = 新建
	Name      string
	Dialect   string
	SourceURL string
	Text      *string        // 没给或全空白:有链接就抓链接,否则(更新时)沿用已存的文本
	Arguments map[string]any // nil = 没传:更新时沿用已存的值;非 nil(含空)= 整个替换
}

// PreviewInput 是预览请求。
type PreviewInput struct {
	Dialect   string
	SourceURL string
	Arguments map[string]any
}

// Service 读写内容过滤。
type Service struct {
	Store *store.Store
	Dir   string // 每份规则文本一个文件 <Dir>/<id>.txt
	// Fetch 抓取链接;nil 用 rulelist.Fetch(只收 http(s),8 MiB 上限,超限报错不截断)。
	Fetch func(ctx context.Context, rawURL string) ([]byte, error)
	// Defaults 随包默认规则;nil 用内嵌的 defaults.json。
	Defaults func() ([]Default, error)
	Now      func() time.Time
	NewID    func() string

	mu    sync.Mutex // 串行化改动;同时保护 cache
	cache *digest
}

type digest struct {
	key  string
	sha  string
	size int
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("读随机数失败: %v", err))
	}
	return "cf" + hex.EncodeToString(b)
}

func (s *Service) fetch(ctx context.Context, u string) ([]byte, error) {
	if s.Fetch != nil {
		return s.Fetch(ctx, u)
	}
	return rulelist.Fetch(ctx, nil, u)
}

func (s *Service) textPath(id string) string { return filepath.Join(s.Dir, id+".txt") }

func (s *Service) load() (*file, error) {
	f := &file{}
	if _, err := s.Store.Load(fileName, f); err != nil {
		return nil, fmt.Errorf("读取内容过滤失败: %w", err)
	}
	return f, nil
}

func (s *Service) readText(id string) (string, error) {
	b, err := os.ReadFile(s.textPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取规则文本失败: %w", err)
	}
	return string(b), nil
}

func (s *Service) writeText(id, text string) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("创建规则文本目录失败: %w", err)
	}
	if err := store.WriteFileAtomic(s.textPath(id), []byte(text), 0o600); err != nil {
		return fmt.Errorf("保存规则文本失败: %w", err)
	}
	return nil
}

// List 返回整份列表(不读全部文本;sha256 / size 算一次缓存,增删改时失效)。
func (s *Service) List(context.Context) (List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Service) listLocked() (List, error) {
	f, err := s.load()
	if err != nil {
		return List{}, err
	}
	sets := f.Sets
	if sets == nil {
		sets = []Set{}
	}
	on := make([]Set, 0, len(sets))
	for _, st := range sets {
		if st.Enabled {
			on = append(on, st)
		}
	}
	slices.SortFunc(on, func(a, b Set) int { return strings.Compare(a.ID, b.ID) })
	var kb strings.Builder
	for _, st := range on {
		fmt.Fprintf(&kb, "%s|%s|%s;", st.ID, st.Dialect, st.UpdatedAt)
	}
	key := kb.String()
	if s.cache == nil || s.cache.key != key { // 外部清数据(恢复出厂)后集合变了,key 也跟着变
		h := sha256.New()
		size := 0
		for _, st := range on {
			text, err := s.readText(st.ID)
			if err != nil {
				return List{}, err
			}
			head := fmt.Sprintf("%d:%s|%s|%d:", len(st.ID), st.ID, st.Dialect, len(text))
			h.Write([]byte(head))
			h.Write([]byte(text))
			size += len(head) + len(text)
		}
		s.cache = &digest{key: key, sha: hex.EncodeToString(h.Sum(nil)), size: size}
	}
	return List{Sets: sets, SHA256: s.cache.sha, Size: s.cache.size}, nil
}

// Source 返回一份规则的文本。
func (s *Service) Source(_ context.Context, id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", ErrNotFound
	}
	f, err := s.load()
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(f.Sets, func(st Set) bool { return st.ID == id }) {
		return "", ErrNotFound
	}
	return s.readText(id)
}

func checkDialect(raw string) (Dialect, error) {
	d, ok := ParseDialect(raw)
	if !ok {
		return "", &InputError{"规则格式不对,可选 surge / quantumultx / loon / shadowrocket / stash"}
	}
	if !Supported(d) {
		return "", ErrUnsupported
	}
	return d, nil
}

func checkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || len(raw) > maxURLLen {
		return "", &InputError{"规则链接必须是 http:// 或 https:// 开头的有效地址(最长 2048 个字符)"}
	}
	return raw, nil
}

func checkArgs(in map[string]any) (map[string]any, error) {
	if len(in) > maxArgs {
		return nil, &InputError{fmt.Sprintf("参数最多 %d 个", maxArgs)}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		name := strings.TrimSpace(k)
		if name == "" || len(name) > maxArgName || strings.ContainsAny(name, "{}") {
			return nil, &InputError{"参数名不能为空、不能含花括号、最长 64 个字符"}
		}
		switch x := v.(type) {
		case string:
			if len(x) > maxArgValue {
				return nil, &InputError{fmt.Sprintf("参数「%s」的值太长(最长 %d 个字符)", name, maxArgValue)}
			}
		case bool, float64:
		default:
			return nil, &InputError{fmt.Sprintf("参数「%s」的值只能是字符串、数字或布尔", name)}
		}
		out[name] = v
	}
	return out, nil
}

// prepare 把一份文本规整、检查大小和编码、数出各类条目。至少要有一条规则或脚本。
func prepare(d Dialect, text string) (string, Counts, error) {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", Counts{}, &InputError{"内容不是文本(必须是 UTF-8)"}
	}
	text = Normalize(text)
	if len(text) > MaxTextBytes {
		return "", Counts{}, &InputError{fmt.Sprintf("规则文本超过 %d KiB", MaxTextBytes>>10)}
	}
	c, err := Count(d, text)
	if err != nil {
		return "", Counts{}, err
	}
	if c.Rules+c.Scripts == 0 {
		return "", Counts{}, &InputError{"没有认得出的规则或脚本,请检查所选格式和内容"}
	}
	return text, c, nil
}

// Preview 抓取链接、代入参数、按格式校验,返回规则文本(不保存)。
func (s *Service) Preview(ctx context.Context, in PreviewInput) (string, error) {
	d, err := checkDialect(in.Dialect)
	if err != nil {
		return "", err
	}
	u, err := checkURL(in.SourceURL)
	if err != nil {
		return "", err
	}
	if u == "" {
		return "", &InputError{"请填写规则链接"}
	}
	args, err := checkArgs(in.Arguments)
	if err != nil {
		return "", err
	}
	body, err := s.fetch(ctx, u)
	if err != nil {
		return "", err
	}
	text, _, err := prepare(d, Substitute(string(body), args))
	return text, err
}

// Save 新建(ID 为空)或更新一份规则集,返回整份最新列表。
func (s *Service) Save(ctx context.Context, in SaveInput) (List, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return List{}, &InputError{fmt.Sprintf("名称不能为空,最长 %d 个字符", maxNameRunes)}
	}
	d, err := checkDialect(in.Dialect)
	if err != nil {
		return List{}, err
	}
	u, err := checkURL(in.SourceURL)
	if err != nil {
		return List{}, err
	}
	var args map[string]any
	if in.Arguments != nil {
		if args, err = checkArgs(in.Arguments); err != nil {
			return List{}, err
		}
	}
	cur, err := s.load()
	if err != nil {
		return List{}, err
	}
	var old *Set
	if in.ID != "" {
		if !idRe.MatchString(in.ID) {
			return List{}, ErrNotFound
		}
		i := slices.IndexFunc(cur.Sets, func(st Set) bool { return st.ID == in.ID })
		if i < 0 {
			return List{}, ErrNotFound
		}
		old = &cur.Sets[i]
		if in.Arguments == nil && old.Arguments != nil {
			args = old.Arguments.Values // 「立即更新」不带参数:沿用已存的
		}
	}

	// 文本来源:填了文本用文本(编辑框里的内容,不再代入);否则抓链接(代入参数);否则更新时沿用旧文本
	var raw string
	switch {
	case in.Text != nil && strings.TrimSpace(*in.Text) != "":
		raw = *in.Text
	case u != "":
		body, err := s.fetch(ctx, u)
		if err != nil {
			return List{}, err
		}
		raw = Substitute(string(body), args)
	case old != nil:
		if raw, err = s.readText(old.ID); err != nil {
			return List{}, err
		}
	default:
		return List{}, &InputError{"请填写规则文本或规则链接"}
	}
	text, counts, err := prepare(d, raw)
	if err != nil {
		return List{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var out List
	var wroteNew string
	err = s.mutate(func(f *file) error {
		set := Set{Name: name, Dialect: d, SourceURL: u, Enabled: true}
		idx := -1
		if in.ID != "" {
			if idx = slices.IndexFunc(f.Sets, func(st Set) bool { return st.ID == in.ID }); idx < 0 {
				return ErrNotFound
			}
			set.ID, set.Enabled = in.ID, f.Sets[idx].Enabled
		} else {
			if len(f.Sets) >= MaxSets {
				return &InputError{fmt.Sprintf("最多 %d 份规则集", MaxSets)}
			}
			set.ID = s.newID()
			for slices.ContainsFunc(f.Sets, func(st Set) bool { return st.ID == set.ID }) {
				set.ID = s.newID()
			}
		}
		set.RuleCount, set.ScriptCount, set.Skipped = counts.Rules, counts.Scripts, counts.Skipped
		set.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		if len(args) > 0 {
			set.Arguments = &Arguments{Values: args}
		}
		if err := s.writeText(set.ID, text); err != nil {
			return err
		}
		if idx < 0 {
			wroteNew = set.ID
			f.Sets = append(f.Sets, set)
		} else {
			f.Sets[idx] = set
		}
		return nil
	}, func() {
		if wroteNew != "" { // 记录没写成:新建的文本文件不留孤儿
			_ = os.Remove(s.textPath(wroteNew))
		}
	})
	if err != nil {
		return List{}, err
	}
	s.cache = nil
	out, err = s.listLocked()
	return out, err
}

// mutate 在 store 的锁里读改写 content-filter.json;写失败时调 onFail 收拾已写的文本文件。
func (s *Service) mutate(fn func(f *file) error, onFail ...func()) error {
	var f file
	err := s.Store.Update(fileName, &f, func() error { return fn(&f) })
	if err != nil {
		for _, h := range onFail {
			h()
		}
	}
	return err
}

// SetEnabled 启用 / 停用一份规则集。
func (s *Service) SetEnabled(_ context.Context, id string, enabled bool) (List, error) {
	if !idRe.MatchString(id) {
		return List{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.mutate(func(f *file) error {
		i := slices.IndexFunc(f.Sets, func(st Set) bool { return st.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		f.Sets[i].Enabled = enabled
		return nil
	})
	if err != nil {
		return List{}, err
	}
	s.cache = nil
	return s.listLocked()
}

// Delete 删除一份规则集和它的文本。
func (s *Service) Delete(_ context.Context, id string) (List, error) {
	if !idRe.MatchString(id) {
		return List{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.mutate(func(f *file) error {
		i := slices.IndexFunc(f.Sets, func(st Set) bool { return st.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		f.Sets = slices.Delete(f.Sets, i, i+1)
		return nil
	})
	if err != nil {
		return List{}, err
	}
	if err := os.Remove(s.textPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return List{}, fmt.Errorf("删除规则文本失败: %w", err)
	}
	s.cache = nil
	return s.listLocked()
}

// RestoreDefaults 恢复默认。随包默认为空时 = 清空列表(DESIGN 第 8 节);
// 不为空时:每份默认规则回到随包内容和启用状态(被删的补回),自己加的保留。
func (s *Service) RestoreDefaults(context.Context) (List, error) {
	defs := s.Defaults
	if defs == nil {
		defs = BuiltinDefaults
	}
	ds, err := defs()
	if err != nil {
		return List{}, err
	}
	type prepared struct {
		d      Default
		text   string
		counts Counts
	}
	var ps []prepared
	for _, d := range ds {
		dl, ok := ParseDialect(string(d.Dialect))
		if !ok || !idRe.MatchString(d.ID) {
			return List{}, fmt.Errorf("内嵌的默认规则「%s」不合法", d.ID)
		}
		text, c, err := prepare(dl, d.Text)
		if err != nil {
			return List{}, fmt.Errorf("内嵌的默认规则「%s」不合法: %w", d.ID, err)
		}
		d.Dialect = dl
		ps = append(ps, prepared{d, text, c})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	err = s.mutate(func(f *file) error {
		if len(ps) == 0 {
			for _, st := range f.Sets {
				gone = append(gone, st.ID)
			}
			f.Sets = []Set{}
			return nil
		}
		for _, p := range ps {
			if err := s.writeText(p.d.ID, p.text); err != nil {
				return err
			}
			set := Set{
				ID: p.d.ID, Name: p.d.Name, Dialect: p.d.Dialect, SourceURL: p.d.SourceURL, Enabled: p.d.Enabled,
				RuleCount: p.counts.Rules, ScriptCount: p.counts.Scripts, Skipped: p.counts.Skipped,
				UpdatedAt: s.now().UTC().Format(time.RFC3339),
			}
			if i := slices.IndexFunc(f.Sets, func(st Set) bool { return st.ID == p.d.ID }); i >= 0 {
				f.Sets[i] = set
			} else {
				f.Sets = append(f.Sets, set)
			}
		}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	for _, id := range gone {
		_ = os.Remove(s.textPath(id)) // 孤儿文本留着也无害,删不掉不算失败
	}
	s.cache = nil
	return s.listLocked()
}
