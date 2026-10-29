package dnsdecide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxSetFile    = 64 << 20 // 规则集文件(.srs 或 .json)的大小上限
	maxSetJSON    = 128 << 20
	decodeTimeout = 60 * time.Second
	cacheSize     = 6
)

// FileSets 按配置里 route.rule_set 登记的本地规则集(local,source / binary 两种格式)和内联规则集(inline)
// 回答 Sets。binary 格式靠 `sing-box rule-set decompile` 转成源码再读;remote 类型不联网取,按判断不了处理。
//
// 一个规则集只有在「所有规则都只含域名类条件」时才能完整判断;含有 IP、端口等条件的规则(例如 geoip-cn)
// 对一个只有域名的查询没法判断,没有别的规则命中时返回 ErrUnknownSet,不猜。
type FileSets struct {
	Defs    map[string]SetDef
	SingBox string // sing-box 可执行文件;空 = 从 PATH 找
	TmpDir  string

	mu    sync.Mutex
	cache []cacheItem // 最近用到的在末尾
}

// SetDef 是 route.rule_set 里的一项。
type SetDef struct {
	Type   string // local / inline / remote
	Format string // source / binary
	Path   string
	Rules  []any // inline 的规则
}

type cacheItem struct {
	path string
	size int64
	mod  int64
	m    *setMatcher
}

// SetDefsFromConfig 取出 route.rule_set。
func SetDefsFromConfig(cfg map[string]any) map[string]SetDef {
	out := map[string]SetDef{}
	route, _ := cfg["route"].(map[string]any)
	list, _ := route["rule_set"].([]any)
	for _, e := range list {
		m, _ := e.(map[string]any)
		tag, _ := m["tag"].(string)
		if tag == "" {
			continue
		}
		d := SetDef{}
		d.Type, _ = m["type"].(string)
		d.Format, _ = m["format"].(string)
		d.Path, _ = m["path"].(string)
		d.Rules, _ = m["rules"].([]any)
		out[tag] = d
	}
	return out
}

// Match 实现 Sets,用 f.Defs 登记的规则集。
func (f *FileSets) Match(ctx context.Context, tag, domain string) (bool, error) {
	return f.match(ctx, f.Defs, tag, domain)
}

// Bind 返回一个用 defs 登记规则集的 Sets,和 f 共用解码缓存。每次预测都重读配置、规则集登记可能变,
// 但缓存按「文件路径 + 大小 + 修改时间」认,所以可以共用,也不用改 f.Defs(并发预测时不会互相踩)。
func (f *FileSets) Bind(defs map[string]SetDef) Sets { return bound{f, defs} }

type bound struct {
	f    *FileSets
	defs map[string]SetDef
}

func (b bound) Match(ctx context.Context, tag, domain string) (bool, error) {
	return b.f.match(ctx, b.defs, tag, domain)
}

func (f *FileSets) match(ctx context.Context, defs map[string]SetDef, tag, domain string) (bool, error) {
	def, ok := defs[tag]
	if !ok {
		return false, fmt.Errorf("%w: 配置里没有规则集 %s", ErrUnknownSet, tag)
	}
	m, err := f.load(ctx, tag, def)
	if err != nil {
		return false, err
	}
	if m.has(domain) {
		return true, nil
	}
	if m.partial {
		return false, fmt.Errorf("%w: 规则集 %s 里有只看域名判断不了的规则", ErrUnknownSet, tag)
	}
	return false, nil
}

func (f *FileSets) load(ctx context.Context, tag string, def SetDef) (*setMatcher, error) {
	switch def.Type {
	case "inline":
		return compile(def.Rules), nil
	case "local", "":
	default:
		return nil, fmt.Errorf("%w: 规则集 %s 是 %s 类型,不联网取", ErrUnknownSet, tag, def.Type)
	}
	if def.Path == "" {
		return nil, fmt.Errorf("%w: 规则集 %s 没有路径", ErrUnknownSet, tag)
	}
	fi, err := os.Stat(def.Path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: 规则集 %s 的文件读不到", ErrUnknownSet, tag)
	}
	if fi.Size() > maxSetFile {
		return nil, fmt.Errorf("%w: 规则集 %s 的文件太大", ErrUnknownSet, tag)
	}
	if m := f.cacheGet(def.Path, fi.Size(), fi.ModTime().UnixNano()); m != nil {
		return m, nil
	}
	var src []byte
	if def.Format == "binary" || (def.Format == "" && strings.HasSuffix(def.Path, ".srs")) {
		src, err = f.decompile(ctx, tag, def.Path)
	} else {
		src, err = readLimited(def.Path, maxSetJSON)
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Rules []any `json:"rules"`
	}
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("%w: 规则集 %s 不是有效的规则集 JSON", ErrUnknownSet, tag)
	}
	m := compile(doc.Rules)
	f.cachePut(def.Path, fi.Size(), fi.ModTime().UnixNano(), m)
	return m, nil
}

func (f *FileSets) decompile(ctx context.Context, tag, path string) ([]byte, error) {
	bin := f.SingBox
	if bin == "" {
		bin = "sing-box"
	}
	exe, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%w: 没有 sing-box,解不开二进制规则集 %s", ErrUnknownSet, tag)
	}
	dir, err := os.MkdirTemp(f.TmpDir, "dnsdecide-*")
	if err != nil {
		return nil, fmt.Errorf("%w: 创建临时目录失败", ErrUnknownSet)
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "out.json")
	cctx, cancel := context.WithTimeout(ctx, decodeTimeout)
	defer cancel()
	var diag bytes.Buffer
	cmd := exec.CommandContext(cctx, exe, "rule-set", "decompile", "--output", out, path)
	cmd.Stdout, cmd.Stderr = &limited{b: &diag, max: 2048}, &limited{b: &diag, max: 2048}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: 解码规则集 %s 失败", ErrUnknownSet, tag)
	}
	return readLimited(out, maxSetJSON)
}

type limited struct {
	b   *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: 读不到 %s", ErrUnknownSet, filepath.Base(path))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, fmt.Errorf("%w: %s 读不全或太大", ErrUnknownSet, filepath.Base(path))
	}
	return b, nil
}

func (f *FileSets) cacheGet(path string, size, mod int64) *setMatcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.cache {
		if c.path == path && c.size == size && c.mod == mod {
			f.cache = append(append(f.cache[:i:i], f.cache[i+1:]...), c) // 挪到末尾
			return c.m
		}
	}
	return nil
}

func (f *FileSets) cachePut(path string, size, mod int64, m *setMatcher) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.cache {
		if c.path == path {
			f.cache = append(f.cache[:i:i], f.cache[i+1:]...)
			break
		}
	}
	f.cache = append(f.cache, cacheItem{path, size, mod, m})
	if len(f.cache) > cacheSize {
		f.cache = f.cache[len(f.cache)-cacheSize:]
	}
}

// setMatcher 是一个规则集里「只含域名类条件」的规则合在一起的匹配器;partial 为真表示还有判断不了的规则。
type setMatcher struct {
	exact   map[string]struct{}
	suffix  []string
	keyword []string
	regex   []*regexp.Regexp
	partial bool
}

// 规则集规则里属于「域名这一组」的键;其余键(ip_cidr、port、process_name 等)出现就判断不了。
var addrKeys = map[string]bool{"domain": true, "domain_suffix": true, "domain_keyword": true, "domain_regex": true}

func compile(rules []any) *setMatcher {
	m := &setMatcher{exact: map[string]struct{}{}}
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		if rule == nil {
			continue
		}
		if t, _ := rule["type"].(string); t == "logical" {
			m.partial = true
			continue
		}
		if inv, _ := rule["invert"].(bool); inv {
			m.partial = true
			continue
		}
		pure, hasAddr := true, false
		for k, v := range rule {
			if k == "type" || empty(v) {
				continue
			}
			if addrKeys[k] {
				hasAddr = true
			} else {
				pure = false
			}
		}
		if !pure {
			m.partial = true // 同时带 IP 等条件(或只有 IP 条件):对只有域名的查询判断不了
			continue
		}
		if !hasAddr {
			continue
		}
		for _, s := range strs(rule["domain"]) {
			m.exact[strings.ToLower(strings.TrimSuffix(s, "."))] = struct{}{}
		}
		for _, s := range strs(rule["domain_suffix"]) {
			if s = strings.ToLower(s); s != "" {
				m.suffix = append(m.suffix, s)
			}
		}
		for _, s := range strs(rule["domain_keyword"]) {
			if s = strings.ToLower(s); s != "" {
				m.keyword = append(m.keyword, s)
			}
		}
		for _, s := range strs(rule["domain_regex"]) {
			if re, err := regexp.Compile(s); err == nil {
				m.regex = append(m.regex, re)
			} else {
				m.partial = true // 编不过的正则:不知道它会不会命中
			}
		}
	}
	return m
}

func (m *setMatcher) has(d string) bool {
	if _, ok := m.exact[d]; ok {
		return true
	}
	for _, s := range m.suffix {
		if SuffixMatch(s, d) {
			return true
		}
	}
	for _, k := range m.keyword {
		if strings.Contains(d, k) {
			return true
		}
	}
	for _, re := range m.regex {
		if re.MatchString(d) {
			return true
		}
	}
	return false
}
