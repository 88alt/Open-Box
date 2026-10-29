package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactNodesAndKeepsDNSRules(t *testing.T) {
	cfg := map[string]any{
		"outbounds": []any{
			map[string]any{"type": "vless", "tag": "HK 01", "server": "hk.secret-host.com", "server_port": 443.0, "uuid": "11111111-2222-3333-4444-555555555555",
				"tls":       map[string]any{"enabled": true, "server_name": "front.example.com", "reality": map[string]any{"enabled": true, "public_key": "PUBKEY", "short_id": "ab12"}},
				"transport": map[string]any{"type": "ws", "path": "/secret-path", "headers": map[string]any{"Host": "h.example.com"}}},
			map[string]any{"type": "trojan", "tag": "T", "server": "1.2.3.4", "server_port": 8443.0, "password": "hunter2"},
		},
		"dns": map[string]any{"servers": []any{map[string]any{"tag": "dns-direct", "server": "223.5.5.5", "type": "udp"}},
			"rules": []any{map[string]any{"rule_set": "geosite-cn", "server": "dns-direct"}}},
		"route":        map[string]any{"rule_set": []any{map[string]any{"tag": "x", "type": "local", "path": "/data/rules/x.srs"}}},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:9090", "secret": "apisecret"}},
		"log":          map[string]any{"level": "warn"},
	}
	raw, _ := json.Marshal(Redact(cfg))
	out := string(raw)
	for _, leak := range []string{"hk.secret-host.com", "11111111-2222", "front.example.com", "PUBKEY", "ab12", "secret-path", "h.example.com", "hunter2", "apisecret"} {
		if strings.Contains(out, leak) {
			t.Errorf("泄漏了 %q: %s", leak, out)
		}
	}
	// 排查要用的东西留着
	for _, keep := range []string{`"server":"223.5.5.5"`, `"server":"dns-direct"`, "/data/rules/x.srs", "127.0.0.1:9090", `"server_port":443`, `"type":"vless"`, `"tag":"HK 01"`, `"level":"warn"`, `"enabled":true`} {
		if !strings.Contains(out, keep) {
			t.Errorf("不该删掉 %s: %s", keep, out)
		}
	}
	if !strings.Contains(out, `"server":"[已脱敏]"`) {
		t.Errorf("节点地址应被抹掉: %s", out)
	}
	// 不改动传入的值
	if cfg["outbounds"].([]any)[1].(map[string]any)["password"] != "hunter2" {
		t.Error("不该改动原值")
	}
}

func TestRedactValueShapes(t *testing.T) {
	in := map[string]any{"password": "", "token": nil, "uuid": 12345.0, "users": []any{map[string]any{"name": "a", "password": "p"}}, "flag_key": "x", "keepalive": "30s", "enabled": true}
	out := Redact(in).(map[string]any)
	if out["password"] != "" || out["token"] != nil {
		t.Errorf("空值保持空: %v", out)
	}
	if out["uuid"] != Redacted || out["flag_key"] != Redacted {
		t.Errorf("数字和以 key 结尾的键也抹: %v", out)
	}
	if out["keepalive"] != "30s" || out["enabled"] != true {
		t.Errorf("无关的键不该动: %v", out)
	}
	us := out["users"].([]any)[0].(map[string]any)
	if us["name"] != Redacted || us["password"] != Redacted {
		t.Errorf("users 整体按敏感处理: %v", us)
	}
}

func TestScrubString(t *testing.T) {
	cases := map[string]string{
		"vless://uuid@host:443?security=tls#名字":                     "[分享链接已脱敏]",
		"见 trojan://pw@x.com:1#a 这条":                                "见 [分享链接已脱敏] 这条",
		"https://sub.example.com/api/v1/client/subscribe?token=abc": "https://sub.example.com/…",
		"https://example.com/":                                      "https://example.com",
		"拉取 https://u:p@dl.example.com/a/b.txt, 失败":                 "拉取 https://dl.example.com/…, 失败",
		"没有链接的普通文字 and 127.0.0.1:7890":                              "没有链接的普通文字 and 127.0.0.1:7890",
	}
	for in, want := range cases {
		if got := ScrubString(in); got != want {
			t.Errorf("%q\n 得到 %q\n 期望 %q", in, got, want)
		}
	}
	pem := "x -----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY----- y"
	if got := ScrubString(pem); strings.Contains(got, "MIIE") || !strings.HasSuffix(got, " y") {
		t.Errorf("PEM 应整块抹掉且不吞后面的字: %q", got)
	}
	if got := ScrubString("-----BEGIN CERTIFICATE-----\nMIIB截断了"); strings.Contains(got, "MIIB") {
		t.Errorf("没有结尾的 PEM 也要抹到末尾: %q", got)
	}
}
