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
	"strings"
	"time"
	"unicode/utf8"
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
	Client       *http.Client
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

type release struct {
	Tag       string `json:"tag_name"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	Draft     bool   `json:"draft"`
	Published string `json:"published_at"`
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

// Latest 找仓库里最新的发布:跳过草稿,取版本号能认出来的里面最大的。直连失败再试镜像(配了前缀的话)。
// 返回 tag(带 v)和来源标记。
func (c *Checker) Latest(ctx context.Context) (tag, via string, err error) {
	var last error
	for _, v := range c.routes() {
		var list []release
		if err := c.get(ctx, "/repos/"+c.repo()+"/releases?per_page=30", v, &list); err != nil {
			last = err
			continue
		}
		best := ""
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
			last = errors.New("仓库里没有找到能识别版本号的发布")
			continue
		}
		return best, v, nil
	}
	return "", "", last
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
		var r release
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
