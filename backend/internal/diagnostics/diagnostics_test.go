package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

type calls struct{ cmds [][]string }

func newSvc(t *testing.T, init system.InitKind, files map[string]string, runOut system.RunResult) (*Service, *calls) {
	t.Helper()
	c := &calls{}
	cfg := filepath.Join(t.TempDir(), "config.json")
	if body, ok := files["CONFIG"]; ok {
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{
		Version: "v1.2.3", CoreService: "sing-box", PanelService: "openbox", ConfigFile: cfg, Init: init,
		KernelVersion: func(context.Context) string { return "1.14.4" },
		ServiceStatus: func(_ context.Context, n string) (system.ServiceInfo, error) {
			return system.ServiceInfo{Running: n == "sing-box", Raw: "running"}, nil
		},
		Profile: func(context.Context) (map[string]any, error) {
			return map[string]any{"testUrl": "https://www.gstatic.com/generate_204",
				"chainProxies": []any{map[string]any{"name": "跳板", "link": "trojan://pw@h.example.com:443#x", "upstream": "HK"}},
				"servers":      []any{map[string]any{"name": "s", "port": 1080.0, "password": "hunter2"}}}, nil
		},
		ReadFile: func(p string) ([]byte, error) {
			if p == cfg {
				return os.ReadFile(cfg)
			}
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
		Run: func(_ context.Context, name string, args ...string) (system.RunResult, error) {
			c.cmds = append(c.cmds, append([]string{name}, args...))
			return runOut, nil
		},
		Now: func() time.Time { return time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC) },
	}, c
}

var openwrtFiles = map[string]string{
	"/etc/openwrt_release": "DISTRIB_ID='OpenWrt'\nDISTRIB_RELEASE='23.05.5'\nDISTRIB_TARGET='mediatek/filogic'\nDISTRIB_DESCRIPTION='OpenWrt 23.05.5 r24106'\n",
	"/proc/version":        "Linux version 5.15.167 (builder) #0 SMP\nsecond line\n",
	"/proc/meminfo":        "MemTotal:        1015936 kB\nMemFree: 1 kB\nMemAvailable:     600000 kB\nSwapTotal: 0 kB\n",
	"CONFIG":               `{"outbounds":[{"type":"vless","tag":"HK","server":"secret.example.com","server_port":443,"uuid":"abc"}],"log":{"level":"warn"}}`,
}

func TestCollectEverythingRedacted(t *testing.T) {
	s, c := newSvc(t, system.InitOpenWrt, openwrtFiles, system.RunResult{Stdout: "Oct 29 sing-box[1]: dial vless://abc@x.com:1#n failed\nOct 29 sing-box[1]: fetched https://sub.example.com/api?token=SECRET ok\n"})
	out := s.Collect(context.Background())
	raw, _ := json.Marshal(out)
	body := string(raw)
	for _, leak := range []string{"secret.example.com", `"uuid":"abc"`, "hunter2", "h.example.com", "token=SECRET", "vless://abc"} {
		if strings.Contains(body, leak) {
			t.Errorf("诊断包泄漏了 %q", leak)
		}
	}
	for _, keep := range []string{"v1.2.3", "OpenWrt 23.05.5 r24106", "mediatek/filogic", "5.15.167", "MemAvailable", "1.14.4", "gstatic.com", `"level":"warn"`, "https://sub.example.com/…"} {
		if !strings.Contains(body, keep) {
			t.Errorf("应保留 %q", keep)
		}
	}
	if out["generatedAt"] != "2026-10-29T08:00:00Z" || out["redacted"] != true {
		t.Errorf("头部: %v %v", out["generatedAt"], out["redacted"])
	}
	fw := out["firmware"].(map[string]any)
	if fw["system"] != "openwrt" || fw["release"] != "23.05.5" || fw["kernel"] != "Linux version 5.15.167 (builder) #0 SMP" {
		t.Errorf("固件: %v", fw)
	}
	sv := out["services"].(map[string]any)
	if sv["core"].(map[string]any)["running"] != true || sv["panel"].(map[string]any)["running"] != false {
		t.Errorf("服务状态: %v", sv)
	}
	want := [][]string{{"logread", "-e", "sing-box"}, {"logread", "-e", "openbox"}}
	if !reflect.DeepEqual(c.cmds, want) {
		t.Errorf("OpenWrt 应用 logread 按服务名过滤: %v", c.cmds)
	}
	cfgOut := out["config"].(map[string]any)
	if cfgOut["sizeBytes"] == nil || cfgOut["content"] == nil || cfgOut["error"] != nil {
		t.Errorf("配置: %v", cfgOut)
	}
}

func TestCollectSystemdUsesJournalctl(t *testing.T) {
	s, c := newSvc(t, system.InitSystemd, map[string]string{"/etc/os-release": "ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_ID=\"12\"\n", "CONFIG": "{}"}, system.RunResult{Stdout: "a\nb\n"})
	out := s.Collect(context.Background())
	fw := out["firmware"].(map[string]any)
	if fw["system"] != "debian" || fw["description"] != "Debian GNU/Linux 12 (bookworm)" || fw["release"] != "12" {
		t.Errorf("os-release: %v", fw)
	}
	if len(c.cmds) != 2 || c.cmds[0][0] != "journalctl" || c.cmds[0][2] != "sing-box" {
		t.Errorf("systemd 应用 journalctl -u: %v", c.cmds)
	}
	if got := out["logs"].(map[string]any)["core"].([]string); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("日志行: %v", got)
	}
}

func TestLogsTrimmedAndScrubbed(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 450; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	sb.WriteString(strings.Repeat("长", 900) + "\n")
	s, _ := newSvc(t, system.InitOpenWrt, openwrtFiles, system.RunResult{Stdout: sb.String()})
	lines := s.Collect(context.Background())["logs"].(map[string]any)["core"].([]string)
	if len(lines) != LogLines {
		t.Fatalf("只留最后 %d 行,得到 %d", LogLines, len(lines))
	}
	if lines[0] != "line 251" && !strings.HasPrefix(lines[0], "line ") {
		t.Errorf("应保留靠后的行: %q", lines[0])
	}
	last := []rune(lines[len(lines)-1])
	if len(last) != maxLineLen+1 || last[len(last)-1] != '…' {
		t.Errorf("过长的行应截断并加省略号: %d", len(last))
	}
}

func TestSectionsFailIndependently(t *testing.T) {
	s, _ := newSvc(t, system.InitNone, map[string]string{"CONFIG": "{坏的"}, system.RunResult{})
	s.ServiceStatus = func(context.Context, string) (system.ServiceInfo, error) {
		return system.ServiceInfo{}, errors.New("init.d 没有")
	}
	s.Profile = func(context.Context) (map[string]any, error) { return nil, errors.New("档案坏了") }
	out := s.Collect(context.Background())
	if e := out["config"].(map[string]any); e["error"] == nil || e["content"] != nil {
		t.Errorf("坏 JSON 的配置不能原样放进去(会漏脱敏): %v", e)
	}
	if out["profile"].(map[string]any)["error"] != "档案坏了" {
		t.Errorf("档案: %v", out["profile"])
	}
	if out["services"].(map[string]any)["core"].(map[string]any)["error"] == nil {
		t.Error("服务状态读不到应只在那一块写原因")
	}
	if out["logs"].(map[string]any)["core"].(map[string]any)["error"] == nil {
		t.Error("没有服务管理器读不了日志应写原因")
	}
	if out["backend"].(map[string]any)["version"] != "v1.2.3" || out["firmware"] == nil || out["kernel"].(map[string]any)["version"] != "1.14.4" {
		t.Error("别的块不受影响")
	}
}

func TestBadServiceNameAndFailedCommand(t *testing.T) {
	s, c := newSvc(t, system.InitOpenWrt, nil, system.RunResult{Code: 1, Stderr: "logread: could not connect to https://ubus.example.com/x?t=SECRET"})
	s.CoreService = "sing-box; rm -rf /"
	out := s.Collect(context.Background())["logs"].(map[string]any)
	if out["core"].(map[string]any)["error"] == nil {
		t.Error("不合法的服务名不该拼进命令")
	}
	for _, cmd := range c.cmds {
		for _, a := range cmd {
			if strings.Contains(a, "rm -rf") {
				t.Errorf("命令里不该出现不合法的服务名: %v", cmd)
			}
		}
	}
	e := out["panel"].(map[string]any)["error"].(string)
	if !strings.Contains(e, "退出码 1") || strings.Contains(e, "SECRET") {
		t.Errorf("命令失败应带退出码且脱敏 stderr: %q", e)
	}
	s.Run = func(context.Context, string, ...string) (system.RunResult, error) {
		return system.RunResult{}, errors.New("没有 logread")
	}
	s.CoreService = "sing-box"
	if got := s.Collect(context.Background())["logs"].(map[string]any)["core"].(map[string]any)["error"]; got == nil {
		t.Error("命令启动不了应写原因")
	}
}

func TestParseKV(t *testing.T) {
	kv := parseKV([]byte("A='x y'\nB=\"z\"\nC=plain\n# 注释\nbad line\nD=\n"))
	if kv["A"] != "x y" || kv["B"] != "z" || kv["C"] != "plain" || kv["D"] != "" || len(kv) != 4 {
		t.Errorf("%v", kv)
	}
}
