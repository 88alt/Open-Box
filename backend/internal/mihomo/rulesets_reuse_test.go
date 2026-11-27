package mihomo

import (
	"context"
	"errors"
	"testing"
)

// 官方安装(v0.1.493 起)把内置规则集落成 .mrs(文件名带 64 位哈希,是 mihomo 自有的二进制容器),
// 自研后端原先只按 <geoDir>/<tag>.srs 找源文件再反编译,两者对不上 → 部署报「无法读取现有规则集文件」。
// 这组测试盯住新增的「复用已部署 provider」快路径:目录里有现成的就照它拼 provider,不去找 .srs。
func newConvertFS(t *testing.T) *memFS {
	t.Helper()
	return newMemFS()
}

// 有官方 .mrs:直接复用,扩展名决定 format,路径是 ./rules/<实际文件名>。
func TestConverterReusesDeployedMrs(t *testing.T) {
	fsys := newConvertFS(t)
	const tag = "geosite-cn"
	name := tag + "-" + "e7cbf0abb6e8f0a328e5174c30c7db5c485bc67519aa8d32aa02f8143287ff62" + ".mrs"
	if err := fsys.WriteFile("/data/mihomo/rules/"+name, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Converter{
		FS:       fsys,
		RulesDir: "/data/mihomo/rules",
		// 故意让 SrsPath 返回错误:走快路径时不该再去找 .srs
		SrsPath: func(string) (string, error) { return "", errors.New("不该被调用") },
		Decompile: func(context.Context, string, string) error {
			t.Error("复用已部署 provider 时不该反编译")
			return nil
		},
	}

	p, err := c.Convert(context.Background(), []string{tag})
	if err != nil {
		t.Fatalf("有 .mrs 时应复用而不是失败: %v", err)
	}
	got := p.Providers[tag]
	if got.Type != "file" || got.Format != "mrs" || got.Behavior != "domain" {
		t.Fatalf("provider 形状不对: %+v", got)
	}
	if got.Path != "./rules/"+name {
		t.Fatalf("路径应指向实际文件,拿到 %q", got.Path)
	}
}

// 哈希算法不同的文件名(不是 64 位十六进制)不该被当成已部署 provider。
func TestConverterIgnoresNonHashedFile(t *testing.T) {
	fsys := newConvertFS(t)
	const tag = "geosite-cn"
	// 名字看着像,但哈希段不对(只有 8 位)
	if err := fsys.WriteFile("/r/"+tag+"-deadbeef.mrs", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Converter{
		FS:       fsys,
		RulesDir: "/r",
		SrsPath:  func(string) (string, error) { return "/r/" + tag + ".srs", nil },
		Decompile: func(context.Context, string, string) error {
			return errors.New("没有 .srs")
		},
	}
	// 应该走老路 → 反编译失败
	if _, err := c.Convert(context.Background(), []string{tag}); err == nil {
		t.Fatal("哈希段不合法时不该被当成已部署 provider")
	}
}

// txt 扩展名映射成 text,未知扩展名不收。
func TestProviderForFileFormats(t *testing.T) {
	cases := []struct {
		name, wantFormat string
		ok               bool
	}{
		{"geosite-cn-<h>.mrs", "mrs", true},
		{"geosite-cn-<h>.yaml", "yaml", true},
		{"geosite-cn-<h>.txt", "text", true},
		{"geosite-cn-<h>.json", "", false},
	}
	for _, c := range cases {
		got, ok := providerForFile("geosite-cn", c.name, "domain", "")
		if ok != c.ok {
			t.Fatalf("%s: ok=%v,期望 %v", c.name, ok, c.ok)
		}
		if ok && got.Format != c.wantFormat {
			t.Fatalf("%s: format=%q,期望 %q", c.name, got.Format, c.wantFormat)
		}
	}
}
