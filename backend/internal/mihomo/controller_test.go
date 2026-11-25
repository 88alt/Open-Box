package mihomo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTmp(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mihomo.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// 两种产出形态都要认:自研 MarshalYAML 的缩进 YAML,和上游面板写的单行 JSON。
func TestControllerFromFileBothShapes(t *testing.T) {
	cases := []struct {
		name, text, addr, secret string
	}{
		{"JSON(上游面板)", `{"allow-lan":false,"external-controller":"127.0.0.1:9095","secret":"ac780fff","tun":{"enable":true}}`, "127.0.0.1:9095", "ac780fff"},
		{"YAML(自研)", "log-level: \"warning\"\nexternal-controller: \"127.0.0.1:9095\"\nsecret: \"0e1f2a\"\n", "127.0.0.1:9095", "0e1f2a"},
		{"YAML 无引号", "external-controller: 127.0.0.1:9095\nsecret: abc\n", "127.0.0.1:9095", "abc"},
		{"YAML 没有 secret", "external-controller: 127.0.0.1:9095\n", "127.0.0.1:9095", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, secret, err := ControllerFromFile(writeTmp(t, c.text))
			if err != nil || addr != c.addr || secret != c.secret {
				t.Fatalf("得到 %q %q %v", addr, secret, err)
			}
		})
	}
}

// 嵌套段里的同名键不能当成顶层:proxies / hosts 里出现 external-controller 这种名字时不能误读。
func TestControllerFromFileIgnoresNestedKeys(t *testing.T) {
	text := "proxies:\n  - name: x\n    external-controller: 10.0.0.1:1\nhosts:\n  \"external-controller\": \"10.0.0.2:2\"\n"
	if addr, _, err := ControllerFromFile(writeTmp(t, text)); err == nil {
		t.Fatalf("嵌套的键不该被当顶层:%q", addr)
	}
}

func TestControllerFromFileErrors(t *testing.T) {
	if _, _, err := ControllerFromFile(filepath.Join(t.TempDir(), "nope.yaml")); err == nil || !strings.Contains(err.Error(), "读不到") {
		t.Fatalf("没有文件应报错: %v", err)
	}
	if _, _, err := ControllerFromFile(writeTmp(t, "tun:\n  enable: true\n")); err == nil || !strings.Contains(err.Error(), "external-controller") {
		t.Fatalf("没开控制接口应报错: %v", err)
	}
}
