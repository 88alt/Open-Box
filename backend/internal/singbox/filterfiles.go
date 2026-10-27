package singbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
)

// 域名过滤的规则集(自己的设计,没有参照上游):每组一个 <tag>.srs,tag 里带内容哈希,
// 所以文件名相同就是内容相同,已经有的不用重编。编用已安装的 sing-box(rule-set compile),
// 得到的二进制格式版本和内核一致。编译(Sync)和清理(Prune)分开:清理只在新配置应用成功之后做。

var filterFileRe = regexp.MustCompile(`^` + regexp.QuoteMeta(dnsfilter.TagPrefix) + `.+\.srs$`)

// FilterFiles 把过滤产物编成 .srs。
type FilterFiles struct {
	SingBox string // sing-box 可执行文件;空 = "sing-box"
}

// Sync 把 groups 编成 <dir>/<tag>.srs,已存在的跳过。只写不删:部署失败回滚时旧配置还要用旧文件。
func (f FilterFiles) Sync(ctx context.Context, dir string, groups []dnsfilter.Group) error {
	for _, g := range groups {
		name := g.Tag + ".srs"
		path := filepath.Join(dir, name)
		if hasSRS(path) {
			continue
		}
		src, err := marshalSource(g.Rule)
		if err != nil {
			return fmt.Errorf("%s: %w", g.Tag, err)
		}
		if _, err := compileSRS(ctx, f.SingBox, path, src, name); err != nil {
			return fmt.Errorf("%s: %w", g.Tag, err)
		}
	}
	return nil
}

// Prune 删掉 dir 里不在 groups 里的 dns-filter-*.srs,别的文件一概不碰;
// 只在新配置已经成功应用之后调用。返回删掉的文件名。
func (FilterFiles) Prune(dir string, groups []dnsfilter.Group) []string {
	keep := map[string]bool{}
	for _, g := range groups {
		keep[g.Tag+".srs"] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var gone []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || keep[name] || !filterFileRe.MatchString(name) {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			gone = append(gone, name)
		}
	}
	return gone
}
