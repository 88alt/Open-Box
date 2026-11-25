package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/timeflysoon/Open-Box/backend/internal/release"
)

// 默认值。
const (
	DefaultRepo    = "timeflysoon/Open-Box"
	DefaultAPIBase = "https://api.github.com"
	maxBody        = 4 << 20 // 发布列表响应的读取上限
	maxNoteRunes   = 20000   // 更新说明截断到这么多字
	httpTimeout    = 15 * time.Second
)

// Checker 读仓库的公开发布信息(GitHub REST API:GET /repos/{repo}/releases、/releases/tags/{tag}),只读、不带任何凭据。
type Checker struct {
	Repo    string // owner/name,空 = DefaultRepo
	APIBase string // 空 = DefaultAPIBase;测试里指向假服务器
	// MirrorPrefix 非空时是镜像加速前缀:镜像通道请求 <前缀><原地址>(例如 https://mirror.example/https://api.github.com/...)。
	// 前缀由安装时给出,后端不内置任何镜像地址。
	MirrorPrefix string
	// Arch 是本机架构(amd64 / arm64),空则取 runtime.GOARCH。只认带这个架构安装包的发布。
	Arch   string
	Client *http.Client
}

func (c *Checker) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Checker) base() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return DefaultAPIBase
}

func (c *Checker) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: httpTimeout}
}

// ReleasesPage 是前端「看其它版本」用的发布列表页。
func (c *Checker) ReleasesPage() string { return "https://github.com/" + c.repo() + "/releases" }

type ghRelease struct {
	Tag       string  `json:"tag_name"`
	Name      string  `json:"name"`
	Body      string  `json:"body"`
	Draft     bool    `json:"draft"`
	Published string  `json:"published_at"`
	Assets    []Asset `json:"assets"`
}

// Asset 是发布里的一个附件。Sums 只有 SHA256SUMS 用得上,安装包用 URL / Size。
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// find 按资产名找附件。
func (r ghRelease) find(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Archive 返回这一版给本架构的安装包;没有就 ok = false(M6 第一节第 4 条:只认带本架构安装包的发布)。
func (r ghRelease) Archive(arch string) (Asset, bool) {
	return r.find(release.ArchiveName(r.Tag, arch))
}

// Sums 返回这一版的 SHA256SUMS 附件。
func (r ghRelease) Sums() (Asset, bool) { return r.find(release.SumsName) }

// Arch 是本次要装的架构(amd64 / arm64),空则取 runtime.GOARCH。
func (c *Checker) arch() string {
	if c.Arch != "" {
		return c.Arch
	}
	return runtime.GOARCH
}

// get 经指定通道取一个 API 地址的 JSON。via 取 "api"(直连 GitHub API)或镜像前缀。
func (c *Checker) get(ctx context.Context, path, via string, out any) error {
	target := c.base() + path
	if via != "api" {
		target = via + target
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "openbox-backend")
	resp, err := c.client().Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return errors.New("连不上发布服务器")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return errors.New("发布服务器限流了,请稍后再试")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("发布服务器回了状态码 %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return errors.New("读取发布服务器的响应失败")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("发布服务器的响应不是预期的格式")
	}
	return nil
}

var errNotFound = errors.New("没有找到这个版本")

// route 是一条可以走的通道:via 是返回给前端的来源标记("api" 或镜像前缀)。
func (c *Checker) routes() []string {
	r := []string{"api"}
	if p := strings.TrimSpace(c.MirrorPrefix); p != "" {
		r = append(r, p)
	}
	return r
}

// Latest 找仓库里最新的发布:跳过草稿,只认带本架构安装包的,取版本号能认出来的里面最大的。直连失败再试镜像(配了前缀的话)。
// 返回 tag(带 v)和来源标记。
func (c *Checker) Latest(ctx context.Context) (tag, via string, err error) {
	var last error
	for _, v := range c.routes() {
		r, err := c.latestOn(ctx, v)
		if err != nil {
			last = err
			continue
		}
		return r.Tag, v, nil
	}
	return "", "", last
}

// LatestTagOnly 只取「仓库里最新的、版本号能认出来的发布 tag」,不要求它带本架构安装包。
//
// 为什么单独一个方法:`Latest` 的「必须带本架构安装包」是给**自动升级**用的(装了也装不上就不该催人升级);
// 但概览顶栏只是**显示版本号**,不该因为这个仓库的资产命名和自打包规则不一致就显示不出来。
// 实测:main 自己的 release 用 `openbox-<版本>-linux-<架构>.tar.gz`,而当前仓库里只有上游命名的
// `open-box-linux-x64.tar.gz`,于是 `Latest` 恒为空、顶栏只能退回构建时注入的版本(2026-11-25 真机发现)。
// 草稿一律跳过(草稿对 `check` 也不可见)。直连失败再试镜像。
func (c *Checker) LatestTagOnly(ctx context.Context) (tag, via string, err error) {
	var last error
	for _, v := range c.routes() {
		var list []ghRelease
		if err := c.get(ctx, "/repos/"+c.repo()+"/releases?per_page=30", v, &list); err != nil {
			last = err
			continue
		}
		var best string
		var bestV Version
		for _, r := range list {
			if r.Draft {
				continue
			}
			pv, ok := ParseVersion(r.Tag)
			if !ok {
				continue
			}
			if best == "" || pv.Compare(bestV) > 0 {
				best, bestV = r.Tag, pv
			}
		}
		if best == "" {
			last = errors.New("仓库里没有版本号能认出来的发布")
			continue
		}
		return best, v, nil
	}
	if last == nil {
		last = errors.New("没有可用的通道")
	}
	return "", "", last
}

// latestOn 在指定通道上找最新一版(只看带本架构安装包的)。
func (c *Checker) latestOn(ctx context.Context, via string) (ghRelease, error) {
	arch := c.arch()
	if !release.ValidArch(arch) {
		return ghRelease{}, fmt.Errorf("本机架构 %q 没有安装包(只出 amd64 / arm64)", arch)
	}
	var list []ghRelease
	if err := c.get(ctx, "/repos/"+c.repo()+"/releases?per_page=30", via, &list); err != nil {
		return ghRelease{}, err
	}
	var best ghRelease
	var bestV Version
	for _, r := range list {
		if r.Draft {
			continue
		}
		// 没有本架构的安装包就不算:装了也装不上,不该催人升级(M6 第一节第 4 条)
		if _, ok := r.Archive(arch); !ok {
			continue
		}
		pv, ok := ParseVersion(r.Tag)
		if !ok {
			continue
		}
		if best.Tag == "" || pv.Compare(bestV) > 0 {
			best, bestV = r, pv
		}
	}
	if best.Tag == "" {
		return ghRelease{}, fmt.Errorf("仓库里没有找到带 %s 安装包的发布", arch)
	}
	return best, nil
}

// CheckResult 对应前端 checkUpdate 的响应。
type CheckResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Via       string `json:"via"`
	HasUpdate bool   `json:"hasUpdate"`
}

// Check 比较当前版本和最新发布。当前版本认不出来(开发构建 "dev")时 hasUpdate 一律为假:没法比,不拿它去催人升级。
func (c *Checker) Check(ctx context.Context, current string) (*CheckResult, error) {
	tag, via, err := c.Latest(ctx)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{Current: current, Latest: tag, Via: via}
	cv, ok1 := ParseVersion(current)
	lv, ok2 := ParseVersion(tag)
	res.HasUpdate = ok1 && ok2 && lv.Compare(cv) > 0
	return res, nil
}

// Plan 是一次升级要下载的东西:安装包与 SHA256SUMS 走同一个通道。
type Plan struct {
	Tag     string `json:"tag"`
	Arch    string `json:"arch"`
	Via     string `json:"via"`
	Archive Asset  `json:"archive"`
	Sums    Asset  `json:"sums"`
}

// routesFor 按通道给出可走的通道顺序。direct 只直连、mirror 只走镜像、auto 先直连再镜像。
func (c *Checker) routesFor(channel string) ([]string, error) {
	switch channel {
	case "direct":
		return []string{"api"}, nil
	case "mirror":
		p := strings.TrimSpace(c.MirrorPrefix)
		if p == "" {
			return nil, errors.New("没有配置镜像前缀,不能用镜像通道")
		}
		return []string{p}, nil
	case "auto":
		return c.routes(), nil
	}
	return nil, errors.New("通道只能是 auto、direct 或 mirror")
}

// byTag 在指定通道上取一版。tag 空 = 取最新版。
func (c *Checker) byTag(ctx context.Context, via, tag string) (ghRelease, error) {
	if tag == "" {
		return c.latestOn(ctx, via)
	}
	var r ghRelease
	if err := c.get(ctx, "/repos/"+c.repo()+"/releases/tags/"+url.PathEscape(tag), via, &r); err != nil {
		return ghRelease{}, err
	}
	if r.Draft {
		return ghRelease{}, errNotFound
	}
	return r, nil
}

// Plan 按通道定下这次要装哪一版、从哪下。tag 空 = 最新版;tag 非空 = 装指定那一版(仍要求它带本架构的安装包)。
// 校验和清单也必须有:没有它就没法核对下载下来的东西。
func (c *Checker) Plan(ctx context.Context, channel, tag string) (*Plan, error) {
	routes, err := c.routesFor(channel)
	if err != nil {
		return nil, err
	}
	arch := c.arch()
	if !release.ValidArch(arch) {
		return nil, fmt.Errorf("本机架构 %q 没有安装包(只出 amd64 / arm64)", arch)
	}
	var last error
	for _, via := range routes {
		r, err := c.byTag(ctx, via, tag)
		if err != nil {
			last = err
			continue
		}
		archive, ok := r.Archive(arch)
		if !ok {
			last = fmt.Errorf("%s 没有 %s 的安装包", r.Tag, arch)
			continue
		}
		sums, ok := r.Sums()
		if !ok {
			last = fmt.Errorf("%s 没有 %s,无法校验下载内容", r.Tag, release.SumsName)
			continue
		}
		return &Plan{Tag: r.Tag, Arch: arch, Via: via, Archive: archive, Sums: sums}, nil
	}
	if last == nil {
		last = errors.New("没有可用的通道")
	}
	return nil, last
}

// Note / Notes 对应前端 OpenboxUpdateNote / OpenboxUpdateNotes。
type Note struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	Body    string `json:"body"`
}

type Notes struct {
	URL   string `json:"url"`
	Note  *Note  `json:"note"`
	Via   string `json:"via"`
	Error string `json:"error,omitempty"`
}

var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-main\.\d+)?$`)

// ValidTag 报告 s 是不是能直接拿来取说明的版本号写法(别的写法后端忽略、自己再探一次最新版)。
func ValidTag(s string) bool { return tagRe.MatchString(s) }

// Notes 取一版的更新说明。永远不返回错误:取不到时 Note 为 nil、Error 写原因(前端靠这个字段显示,接口始终 200)。
// latest 为空或写法不对时先探最新版。
func (c *Checker) Notes(ctx context.Context, latest string) Notes {
	out := Notes{URL: c.ReleasesPage()}
	tag := latest
	if !ValidTag(tag) {
		t, _, err := c.Latest(ctx)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		tag = t
	}
	var last error
	for _, v := range c.routes() {
		var r ghRelease
		if err := c.get(ctx, "/repos/"+c.repo()+"/releases/tags/"+url.PathEscape(tag), v, &r); err != nil {
			last = err
			continue
		}
		body := r.Body
		if utf8.RuneCountInString(body) > maxNoteRunes {
			body = string([]rune(body)[:maxNoteRunes]) + "\n\n…(说明太长,已截断,完整内容见发布页)"
		}
		out.Note = &Note{Version: r.Tag, Date: r.Published, Body: body}
		out.Via = v
		return out
	}
	if last != nil {
		out.Error = last.Error()
	}
	return out
}
