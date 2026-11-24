package mihomo

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	geoipTag       = regexp.MustCompile(`^geoip-[a-z0-9_-]+$`)
	providerHashYA = regexp.MustCompile(`^[a-f0-9]{64}\.yaml$`)
	cidrPayloadRow = regexp.MustCompile(`^  - [0-9a-fA-F:.]+/\d{1,3}$`)
)

// deployedCache 对应上游 dC:读回的网段表按内容摘要缓存,最多 128 份。
const deployedCache = 128

// DeployedReader 读回「已经部署的 geoip provider」里的网段(上游 OE)。
// 入口旁路要的是 geoip 规则集里的网段,而这些已经被转成 provider 写在 <DataDir>/mihomo/rules 里了,
// 直接读回来比再调一次 `sing-box rule-set decompile` 便宜。读不回(文件没了、内容被改过、格式不对)一律返回 false,
// 调用方再走转换。
type DeployedReader struct {
	FS        FS
	MihomoDir string // Mihomo 的 -d 目录(<DataDir>/mihomo);provider 的 path 相对它
	cache     *lru[[]string]
}

// NewDeployedReader 建一个读取器;缓存只在这个读取器里。
func NewDeployedReader(fs FS, mihomoDir string) *DeployedReader {
	return &DeployedReader{FS: fs, MihomoDir: mihomoDir, cache: newLRU[[]string](deployedCache)}
}

// GeoIP 返回规则集 tag 的 provider p 里的网段(顺序同文件)。条件(都不满足就返回 false):
// tag 是 geoip-*;p 是 file / ipcidr / yaml;p.path 恰好是 `./rules/<tag>-<64 位十六进制>.yaml`;
// 文件以 `payload:` 换行开头;**文件名里的摘要等于文件内容现算的摘要**(防文件被改、被截断);
// 载荷每行都是 `  - <网段>`。返回的切片是副本,调用方可以随便改。
func (d *DeployedReader) GeoIP(tag string, p Provider) ([]string, bool) {
	if !geoipTag.MatchString(tag) || p.Type != "file" || p.Behavior != "ipcidr" || p.Format != "yaml" {
		return nil, false
	}
	prefix := "./rules/" + tag + "-"
	if !strings.HasPrefix(p.Path, prefix) || !providerHashYA.MatchString(p.Path[len(prefix):]) {
		return nil, false
	}
	raw, err := d.FS.ReadFile(filepath.Join(d.MihomoDir, p.Path[2:]))
	if err != nil {
		return nil, false
	}
	content := string(raw)
	if !strings.HasPrefix(content, "payload:\n") {
		return nil, false
	}
	name := ProviderFile("", "ipcidr", content) // "-<摘要>.yaml"
	if !strings.HasSuffix(p.Path, name) {
		return nil, false
	}
	hash := name[1 : len(name)-len(".yaml")]
	if hit, ok := d.cache.get(hash); ok {
		return append([]string(nil), hit...), true
	}
	body := strings.TrimRightFunc(content, jsSpace)
	if len(body) > 9 {
		body = body[9:]
	} else {
		body = ""
	}
	rows := strings.Split(body, "\n")
	out := make([]string, len(rows))
	for i, row := range rows {
		if !cidrPayloadRow.MatchString(row) {
			return nil, false
		}
		out[i] = row[4:]
	}
	d.cache.set(hash, out)
	return append([]string(nil), out...), true
}
