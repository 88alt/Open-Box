package mihomo

import (
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const testMihomoDir = "/d/mihomo"

// 用 WriteProvider 写一份真的 geoip provider,再读回来。
func writeGeoIP(t *testing.T, fs *memFS, tag string, cidrs ...string) Provider {
	t.Helper()
	src := NewSource(3, Rule{Strs("ip_cidr", cidrs...)})
	p, err := WriteProvider(fs, filepath.Join(testMihomoDir, "rules"), tag, src, "ipcidr")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDeployedReaderRoundTrip(t *testing.T) {
	fs := newMemFS()
	want := []string{"1.0.0.0/24", "2001:db8::/32", "10.0.0.0/8"}
	p := writeGeoIP(t, fs, "geoip-cn", want...)
	d := NewDeployedReader(fs, testMihomoDir)
	got, ok := d.GeoIP("geoip-cn", p)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("GeoIP = %v, %v; want %v", got, ok, want)
	}
	// 返回的是副本:改它不影响缓存里的那份。
	got[0] = "9.9.9.9/32"
	again, ok := d.GeoIP("geoip-cn", p)
	if !ok || !reflect.DeepEqual(again, want) {
		t.Fatalf("second read = %v, %v; want %v", again, ok, want)
	}
	// 缓存只省掉逐行校验,文件仍要读(摘要靠内容现算):文件没了就读不回。
	delete(fs.files, filepath.Join(testMihomoDir, p.Path[2:]))
	if _, ok := d.GeoIP("geoip-cn", p); ok {
		t.Fatal("deleted file still readable")
	}
}

func TestDeployedReaderRejects(t *testing.T) {
	fs := newMemFS()
	p := writeGeoIP(t, fs, "geoip-cn", "1.0.0.0/24")
	d := NewDeployedReader(fs, testMihomoDir)
	full := filepath.Join(testMihomoDir, p.Path[2:])

	bad := func(name string, tag string, q Provider) {
		t.Helper()
		if got, ok := d.GeoIP(tag, q); ok {
			t.Errorf("%s: accepted, got %v", name, got)
		}
	}
	with := func(f func(*Provider)) Provider { q := p; f(&q); return q }

	bad("tag 不是 geoip-", "geosite-cn", p)
	bad("tag 带大写", "geoip-CN", p)
	bad("type 不是 file", "geoip-cn", with(func(q *Provider) { q.Type = "http" }))
	bad("behavior 不是 ipcidr", "geoip-cn", with(func(q *Provider) { q.Behavior = "domain" }))
	bad("format 不是 yaml", "geoip-cn", with(func(q *Provider) { q.Format = "mrs" }))
	bad("path 不在 ./rules/<tag>- 下", "geoip-cn", with(func(q *Provider) { q.Path = strings.Replace(q.Path, "geoip-cn", "geoip-us", 1) }))
	bad("path 摘要不是 64 位", "geoip-cn", with(func(q *Provider) { q.Path = q.Path[:len(q.Path)-6] + ".yaml" }))
	bad("path 摘要有大写", "geoip-cn", with(func(q *Provider) {
		i := strings.LastIndexAny(q.Path, "abcdef")
		q.Path = q.Path[:i] + strings.ToUpper(q.Path[i:i+1]) + q.Path[i+1:]
	}))
	bad("path 不是字符串开头的 ./", "geoip-cn", with(func(q *Provider) { q.Path = "rules/" + q.Path[len("./rules/"):] }))

	// 文件被改过:摘要对不上。
	fs.files[full] = append(append([]byte(nil), fs.files[full]...), []byte("  - 8.8.8.8/32\n")...)
	bad("内容被改", "geoip-cn", p)

	// 文件没了。
	delete(fs.files, full)
	bad("文件不存在", "geoip-cn", p)
}

func TestDeployedReaderPayloadShape(t *testing.T) {
	for name, content := range map[string]string{
		"没有 payload 头":  "- 1.0.0.0/24\n",
		"payload 头后只有空": "payload:\n",
		"行不是网段":         "payload:\n  - 1.0.0.0/24\n  - not-a-cidr\n",
		"缺前缀":           "payload:\n  - 1.0.0.0\n",
		"前缀超 3 位":       "payload:\n  - 1.0.0.0/1234\n",
		"缩进不对":          "payload:\n - 1.0.0.0/24\n",
		"带引号":           "payload:\n  - '1.0.0.0/24'\n",
		"空行夹在中间":        "payload:\n  - 1.0.0.0/24\n\n  - 2.0.0.0/24\n",
	} {
		fs := newMemFS()
		file := ProviderFile("geoip-x", "ipcidr", content) // 文件名里的摘要是对的,只是内容不合格
		fs.files[filepath.Join(testMihomoDir, "rules", file)] = []byte(content)
		p := Provider{Type: "file", Behavior: "ipcidr", Format: "yaml", Path: "./rules/" + file}
		if got, ok := NewDeployedReader(fs, testMihomoDir).GeoIP("geoip-x", p); ok {
			t.Errorf("%s: accepted, got %v", name, got)
		}
	}
	// 文件末尾多余的空白 / 换行不影响(上游先 trimEnd)。
	fs := newMemFS()
	content := "payload:\n  - 1.0.0.0/24\n  - ::1/128\n\n\n"
	file := ProviderFile("geoip-x", "ipcidr", content)
	fs.files[filepath.Join(testMihomoDir, "rules", file)] = []byte(content)
	p := Provider{Type: "file", Behavior: "ipcidr", Format: "yaml", Path: "./rules/" + file}
	got, ok := NewDeployedReader(fs, testMihomoDir).GeoIP("geoip-x", p)
	if !ok || !reflect.DeepEqual(got, []string{"1.0.0.0/24", "::1/128"}) {
		t.Fatalf("trailing whitespace: %v, %v", got, ok)
	}
}

// 缓存最多 128 份,满了丢最旧的(丢了只是下次重新逐行校验,结果不变)。
func TestDeployedReaderCacheBounded(t *testing.T) {
	fs := newMemFS()
	d := NewDeployedReader(fs, testMihomoDir)
	for i := 0; i < deployedCache+20; i++ {
		p := writeGeoIP(t, fs, "geoip-t", "10."+strconv.Itoa(i)+".0.0/16")
		if _, ok := d.GeoIP("geoip-t", p); !ok {
			t.Fatalf("read %d failed", i)
		}
	}
	if n := len(d.cache.m); n != deployedCache {
		t.Fatalf("cache size = %d, want %d", n, deployedCache)
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)))
}
