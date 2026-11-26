package routetest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ReadDeployedConfig 必须按 router-kernel 选文件:sing-box 读 config.json,mihomo 明确拒绝(不退回旧文件)。
func TestReadDeployedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"route":{"rules":[{"action":"route"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// router-kernel 不存在:按 sing-box,与 system.ReadRouterKernel 的默认一致
	cfg, err := ReadDeployedConfig(dir, nil)
	if err != nil {
		t.Fatalf("router-kernel 不存在时应按 sing-box 读 config.json,拿到错:%v", err)
	}
	if cfg["route"] == nil {
		t.Fatalf("应当读到 config.json 的内容,拿到 %v", cfg)
	}

	// 显式 sing-box:同样读 config.json
	if _, err := ReadDeployedConfig(dir, func() (string, error) { return "sing-box", nil }); err != nil {
		t.Fatalf("sing-box 下应读 config.json,拿到错:%v", err)
	}

	// mihomo:必须报不支持,**不能**退回读 config.json
	_, err = ReadDeployedConfig(dir, func() (string, error) { return "mihomo", nil })
	if !errors.Is(err, ErrKernelConfigUnsupported) {
		t.Fatalf("mihomo 下应返回 ErrKernelConfigUnsupported,拿到 %v", err)
	}

	// router-kernel 文件写着 mihomo:同样拒绝
	if err := os.WriteFile(filepath.Join(dir, "router-kernel"), []byte("mihomo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDeployedConfig(dir, nil); !errors.Is(err, ErrKernelConfigUnsupported) {
		t.Fatalf("router-kernel=mihomo 时应返回 ErrKernelConfigUnsupported,拿到 %v", err)
	}

	// kernel 回调出错:按 sing-box 处理(与 controllerproxy.KernelResolver 同口径)
	if _, err := ReadDeployedConfig(dir, func() (string, error) { return "", errors.New("boom") }); err != nil {
		t.Fatalf("内核读不出来时应退回 sing-box 读 config.json,拿到错:%v", err)
	}

	// config.json 真的不存在:报「还没部署过」,不是 mihomo 那条
	empty := t.TempDir()
	if _, err := ReadDeployedConfig(empty, nil); err == nil {
		t.Fatal("config.json 不存在时应报错")
	}
}
