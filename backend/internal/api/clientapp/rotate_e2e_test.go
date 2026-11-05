package clientapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// 端到端:真实的档案服务(原子改单台服务器 + servers 校验)、真实的 clientapp.Service、真实路由。
// 部署用假的:只记被调用几次。验证 rotate 之后档案里只有这一台的密码变了、旧代数取不回、新代数重新出码后取得回。
func TestShareRotateWithRealProfile(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	prof := profile.New(st, nil, nil)
	var servers []any
	if err := json.Unmarshal([]byte(`[
		{"id":"a","name":"A","protocol":"shadowsocks","port":8388,"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA=="},
		{"id":"b","name":"B","protocol":"hysteria2","port":8443,"password":"keep-b","obfs":"o"}]`), &servers); err != nil {
		t.Fatal(err)
	}
	if _, err := prof.Patch(ctx, map[string]any{"servers": servers}); err != nil {
		t.Fatal(err)
	}

	deploys := 0
	svc := &clientapp.Service{
		Store: st, Profile: prof.Get, KDFIter: 1000, PanelPort: 3036,
		UpdateServer: func(ctx context.Context, id string, change func(map[string]any) error) (map[string]any, error) {
			out, err := prof.UpdateServer(ctx, id, change)
			if err == profile.ErrServerNotFound {
				return nil, clientapp.ErrServerNotFound
			}
			return out, err
		},
		Running: func(context.Context) (bool, error) { return true, nil },
		Deploy:  func(context.Context) clientapp.DeployResult { deploys++; return clientapp.DeployResult{OK: true} },
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Share: svc, Config: svc})
	post := func(path, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}

	code, first := post("/api/openbox/client-app/share-codes", `{"links":{"a":"ss://a","b":"hy2://b"}}`)
	if code != 200 || first["a"] == nil {
		t.Fatalf("出码: %d %v", code, first)
	}
	oldGen := first["a"].(map[string]any)["gen"].(string)

	code, got := post("/api/openbox/client-app/share-codes/a/rotate", "")
	if code != 200 || got["applied"] != "restarted" || deploys != 1 || got["gen"] == oldGen {
		t.Fatalf("rotate: %d %v 部署 %d 次", code, got, deploys)
	}
	srv := got["server"].(map[string]any)
	if srv["password"] == "AAAAAAAAAAAAAAAAAAAAAA==" || srv["method"] != "2022-blake3-aes-128-gcm" {
		t.Fatalf("2022 系列密码要换成新的 16 字节 base64: %v", srv)
	}

	cur, err := prof.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list := cur["servers"].([]any)
	if list[0].(map[string]any)["password"] != srv["password"] || list[1].(map[string]any)["password"] != "keep-b" || list[1].(map[string]any)["obfs"] != "o" {
		t.Fatalf("档案里只有 a 的密码变了: %v", list)
	}

	if code, _ := post("/api/openbox/client-app/share-codes/ghost/rotate", ""); code != 404 {
		t.Fatalf("不存在的服务器: %d", code)
	}
	if deploys != 1 {
		t.Fatalf("404 不该部署: %d", deploys)
	}
	// 面板拿新密码重新交链接:沿用换好的配对码和代数
	code, again := post("/api/openbox/client-app/share-codes", `{"links":{"a":"ss://a2","b":"hy2://b"}}`)
	a := again["a"].(map[string]any)
	if code != 200 || a["pairCode"] != got["pairCode"] || a["gen"] != got["gen"] || a["sealed"] == "" {
		t.Fatalf("重新出码: %d %v", code, again)
	}
}
