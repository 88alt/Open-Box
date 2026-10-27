// Package geodata 读取随包规则库:打包时冻结在一个目录里的 geosite-*.srs / geoip-*.srs。
//
// 后端自己不解析 .srs 的二进制格式,而是交给已安装的 sing-box
// (`sing-box rule-set decompile`),所以看到的就是内核实际会匹配的那份名单。
// 规则库是只读目录:这里不下载、不写入、不改名;分类不在目录里就如实报「没有」。
//
// 目录布局(自己的约定,M6 打包时按它放):<目录>/<分类名>.srs,分类名形如 geosite-cn、geoip-cn。
package geodata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

const (
	// MaxFileBytes 是单个 .srs 的大小上限(现有分类都远小于它;防止目录里被放进异常大的文件)。
	MaxFileBytes = 32 << 20
	// MaxJSONBytes 是解码后 JSON 的大小上限。
	MaxJSONBytes = 64 << 20
	// MaxEntries 是单个分类解出的条目上限,超过就截断并标 Truncated。
	MaxEntries = 300000

	maxTagLen     = 128
	decodeTimeout = 60 * time.Second
	maxDiagBytes  = 4096

	// 内存缓存:搜索框边打边搜时每次按键都会带同一个分类名,不能每次都起一次 sing-box。
	cacheMaxItems   = 2
	cacheMaxEntries = 200000
)

// 业务错误。
var (
	ErrInvalidTag = errors.New("分类名不合法,应形如 geosite-cn 或 geoip-cn")
	ErrNoLibrary  = errors.New("随包规则库目录不存在")
	ErrNotFound   = errors.New("规则库里没有这个分类")
	ErrNoSingBox  = errors.New("找不到 sing-box,无法解码规则库")
)

// DecodeError 是文件在、sing-box 也在,但解码没成功(500)。
type DecodeError struct{ Msg string }

func (e *DecodeError) Error() string { return e.Msg }

func decodeErr(f string, a ...any) error { return &DecodeError{Msg: fmt.Sprintf(f, a...)} }

// Result 是一个分类解码后的内容。缓存里的 Result 是共享的,调用方只读,不要改 Entries。
type Result struct {
	Entries []rulelist.Entry
	// Skipped 是认不得而忽略的规则个数(逻辑规则、只含端口 / 进程名等条件的规则)。
	Skipped int
	// Truncated 为真表示条目超过 MaxEntries,只留了前面的。
	Truncated bool
}

// ValidTag 判断分类名是否合法:geosite- 或 geoip- 开头,后面只含字母、数字和 . _ ! @ -
// (公开规则库的分类名里确有 `@` 和 `!`,例如 geosite-geolocation-!cn),且不含 `..`。
// 它同时保证拼出来的文件路径不会跳出规则库目录。
func ValidTag(tag string) bool {
	if len(tag) > maxTagLen || strings.Contains(tag, "..") {
		return false
	}
	var rest string
	switch {
	case strings.HasPrefix(tag, "geosite-"):
		rest = tag[len("geosite-"):]
	case strings.HasPrefix(tag, "geoip-"):
		rest = tag[len("geoip-"):]
	default:
		return false
	}
	if rest == "" {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '!', c == '@', c == '-':
		default:
			return false
		}
	}
	return true
}

type stamp struct {
	size int64
	mod  int64
}

type cacheItem struct {
	tag string
	st  stamp
	res *Result
}

// Library 是一个规则库目录加用来解码的 sing-box。
type Library struct {
	Dir     string // 规则库目录(只读)
	SingBox string // sing-box 可执行文件(可以只写名字,从 PATH 找)
	TmpDir  string // 解码临时目录;空则用系统临时目录

	sem   chan struct{} // 同一时刻只解一个:路由器内存小,也避免同一分类被并发重复解
	mu    sync.Mutex
	cache []cacheItem // 最近用到的在末尾
}

// New 创建 Library。
func New(dir, singBox, tmpDir string) *Library {
	return &Library{Dir: dir, SingBox: singBox, TmpDir: tmpDir, sem: make(chan struct{}, 1)}
}

// Has 报告规则库里是否有这个分类(只看文件在不在,不解码)。
func (l *Library) Has(tag string) bool {
	if !ValidTag(tag) {
		return false
	}
	fi, err := os.Stat(filepath.Join(l.Dir, tag+".srs"))
	return err == nil && fi.Mode().IsRegular()
}

// Entries 返回分类里的全部条目。分类名不合法 ErrInvalidTag;目录不存在 ErrNoLibrary;
// 分类不在目录里 ErrNotFound;没有 sing-box ErrNoSingBox;解码失败 *DecodeError;
// 请求被取消返回 ctx 的错误。条目类型与规则集链接预览一致(domain / domain_suffix /
// domain_keyword / domain_regex / ip_cidr),同类型同内容去重,保持规则库里的顺序。
func (l *Library) Entries(ctx context.Context, tag string) (*Result, error) {
	if !ValidTag(tag) {
		return nil, ErrInvalidTag
	}
	if fi, err := os.Stat(l.Dir); err != nil || !fi.IsDir() {
		return nil, ErrNoLibrary
	}
	path := filepath.Join(l.Dir, tag+".srs")
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, decodeErr("读取规则库文件 %s.srs 失败: %v", tag, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	if fi.Size() > MaxFileBytes {
		return nil, decodeErr("规则库文件 %s.srs 太大(%d 字节,上限 %d)", tag, fi.Size(), MaxFileBytes)
	}
	st := stamp{size: fi.Size(), mod: fi.ModTime().UnixNano()}
	if r := l.cacheGet(tag, st); r != nil {
		return r, nil
	}

	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-l.sem }()
	// 排队期间别的请求可能已经解好了
	if r := l.cacheGet(tag, st); r != nil {
		return r, nil
	}

	bin := l.SingBox
	if bin == "" {
		bin = "sing-box"
	}
	exe, err := exec.LookPath(bin)
	if err != nil {
		return nil, ErrNoSingBox
	}
	res, err := l.decompile(ctx, exe, path, tag)
	if err != nil {
		return nil, err
	}
	l.cachePut(tag, st, res)
	return res, nil
}

func (l *Library) decompile(ctx context.Context, exe, path, tag string) (*Result, error) {
	dir, err := os.MkdirTemp(l.TmpDir, "geodata-*")
	if err != nil {
		return nil, decodeErr("创建解码临时目录失败: %v", err)
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "out.json")

	cctx, cancel := context.WithTimeout(ctx, decodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, exe, "rule-set", "decompile", "--output", out, path)
	lw := &limitedWriter{max: maxDiagBytes}
	cmd.Stdout, cmd.Stderr = lw, lw
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cctx.Err() != nil {
			return nil, decodeErr("解码 %s.srs 超时(%s)", tag, decodeTimeout)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg := strings.TrimSpace(lw.buf.String())
			if msg == "" {
				msg = ee.Error()
			}
			msg = strings.NewReplacer(path, tag+".srs", out, "out.json").Replace(msg)
			return nil, decodeErr("sing-box 解码 %s.srs 失败: %s", tag, msg)
		}
		return nil, decodeErr("运行 sing-box 失败: %v", err)
	}

	f, err := os.Open(out)
	if err != nil {
		return nil, decodeErr("读取解码结果失败: %v", err)
	}
	defer f.Close()
	var src source
	if err := json.NewDecoder(io.LimitReader(f, MaxJSONBytes+1)).Decode(&src); err != nil {
		return nil, decodeErr("%s.srs 的解码结果不是有效的规则集 JSON(或超过 %d 字节): %v", tag, MaxJSONBytes, err)
	}
	return convert(&src), nil
}

// source 是 sing-box 规则集源码格式里这里用得上的部分;不认识的字段一律忽略。
type source struct {
	Version int    `json:"version"`
	Rules   []rule `json:"rules"`
}

type rule struct {
	Type          string   `json:"type"` // "logical" 是逻辑规则;普通规则为空或 "default"
	Domain        listable `json:"domain"`
	DomainSuffix  listable `json:"domain_suffix"`
	DomainKeyword listable `json:"domain_keyword"`
	DomainRegex   listable `json:"domain_regex"`
	IPCIDR        listable `json:"ip_cidr"`
}

// listable 既收单个字符串也收字符串数组(sing-box 的 Listable 两种写法都合法),null 当作空。
type listable []string

func (l *listable) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*l = nil
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = listable{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

func convert(src *source) *Result {
	res := &Result{Entries: []rulelist.Entry{}}
	seen := map[rulelist.Entry]struct{}{}
	add := func(typ string, vals listable) {
		for _, v := range vals {
			if v == "" || res.Truncated {
				continue
			}
			e := rulelist.Entry{Type: typ, Value: v}
			if _, dup := seen[e]; dup {
				continue
			}
			if len(res.Entries) >= MaxEntries {
				res.Truncated = true
				continue
			}
			seen[e] = struct{}{}
			res.Entries = append(res.Entries, e)
		}
	}
	for _, r := range src.Rules {
		if r.Type == "logical" {
			res.Skipped++
			continue
		}
		if len(r.Domain)+len(r.DomainSuffix)+len(r.DomainKeyword)+len(r.DomainRegex)+len(r.IPCIDR) == 0 {
			res.Skipped++
			continue
		}
		add(rulelist.TypeDomain, r.Domain)
		add(rulelist.TypeDomainSuffix, r.DomainSuffix)
		add(rulelist.TypeDomainKeyword, r.DomainKeyword)
		add(rulelist.TypeDomainRegex, r.DomainRegex)
		add(rulelist.TypeIPCIDR, r.IPCIDR)
	}
	return res
}

func (l *Library) cacheGet(tag string, st stamp) *Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, it := range l.cache {
		if it.tag == tag && it.st == st {
			l.cache = append(append(l.cache[:i:i], l.cache[i+1:]...), it) // 挪到末尾
			return it.res
		}
	}
	return nil
}

func (l *Library) cachePut(tag string, st stamp, res *Result) {
	if len(res.Entries) > cacheMaxEntries {
		return // 单个就超了:不缓存,免得常驻大块内存
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.cache[:0:0]
	for _, it := range l.cache {
		if it.tag != tag {
			kept = append(kept, it)
		}
	}
	l.cache = append(kept, cacheItem{tag: tag, st: st, res: res})
	for len(l.cache) > cacheMaxItems || l.cachedEntries() > cacheMaxEntries {
		l.cache = l.cache[1:]
	}
}

func (l *Library) cachedEntries() int {
	n := 0
	for _, it := range l.cache {
		n += len(it.res.Entries)
	}
	return n
}

type limitedWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}
