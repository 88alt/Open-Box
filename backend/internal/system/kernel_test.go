package system

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRouterKernel(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "router-kernel")

	// 文件不存在、路径为空:都是 sing-box
	for _, path := range []string{p, ""} {
		if k, err := ReadRouterKernel(path); err != nil || k != KernelSingBox {
			t.Fatalf("%q: %q %v", path, k, err)
		}
	}
	// 合法内容(含结尾换行和空白)
	for in, want := range map[string]string{"sing-box": KernelSingBox, "mihomo\n": KernelMihomo, "  mihomo \r\n": KernelMihomo} {
		if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		if k, err := ReadRouterKernel(p); err != nil || k != want {
			t.Fatalf("%q: %q %v", in, k, err)
		}
	}
	// 无效内容(包括空文件)
	for _, in := range []string{"", "clash", "Mihomo", "sing-box mihomo"} {
		if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		if k, err := ReadRouterKernel(p); !errors.Is(err, ErrInvalidRouterKernel) || k != "" {
			t.Fatalf("%q 应无效: %q %v", in, k, err)
		}
	}
	// 其它读错误(路径是目录)不能当成「没有选择」
	if _, err := ReadRouterKernel(dir); err == nil || errors.Is(err, ErrInvalidRouterKernel) {
		t.Fatalf("目录应报读取错误: %v", err)
	}
}

func TestKernelPaths(t *testing.T) {
	p := Paths{ConfigDir: filepath.Join("etc", "ob"), DataDir: filepath.Join("var", "ob")}
	for got, want := range map[string]string{
		p.RouterKernelFile():      filepath.Join("etc", "ob", "router-kernel"),
		p.MihomoConfigFile():      filepath.Join("etc", "ob", "mihomo.yaml"),
		p.MihomoEntryBypassFile(): filepath.Join("etc", "ob", "mihomo-entry-bypass.nft"),
		p.MihomoDnsmasqFile():     filepath.Join("etc", "ob", "mihomo-dnsmasq"),
		p.MihomoInputsFile():      filepath.Join("etc", "ob", "mihomo.inputs"),
		p.MihomoHome():            filepath.Join("var", "ob", "mihomo"),
	} {
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestLoadConfigMihomo(t *testing.T) {
	cfg, err := LoadConfig(nil, noEnv, io.Discard)
	if err != nil || cfg.Mihomo != "mihomo" {
		t.Fatalf("默认值: %q %v", cfg.Mihomo, err)
	}
	cfg, err = LoadConfig(nil, func(k string) string {
		if k == EnvMihomo {
			return "/env/mihomo"
		}
		return ""
	}, io.Discard)
	if err != nil || cfg.Mihomo != "/env/mihomo" {
		t.Fatalf("环境变量: %q %v", cfg.Mihomo, err)
	}
	cfg, err = LoadConfig([]string{"-mihomo", "/flag/mihomo"}, noEnv, io.Discard)
	if err != nil || cfg.Mihomo != "/flag/mihomo" {
		t.Fatalf("命令行: %q %v", cfg.Mihomo, err)
	}
	if _, err = LoadConfig([]string{"-mihomo", " "}, noEnv, io.Discard); err == nil {
		t.Fatal("空的 -mihomo 应报错")
	}
}
