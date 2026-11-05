package clientapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	authapi "github.com/timeflysoon/Open-Box/backend/internal/api/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// 端到端:真实的登录鉴权中间件 + 真实的 clientapp.Service。
// 公开同步接口不要登录就能到达(只受开关、nonce、限速约束),而 /api/ 下的同名管理接口要登录。
func TestPublicSyncBypassesLoginButAPIDoesNot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := auth.NewManager(st, auth.WithIterations(1000))
	if err != nil {
		t.Fatal(err)
	}
	svc := &clientapp.Service{
		Store:     st,
		Profile:   func(context.Context) (map[string]any, error) { return map[string]any{}, nil },
		LAN:       func() ([]string, []string) { return []string{"192.168.5.1"}, []string{"192.168.5.0/24"} },
		PanelPort: 3036,
		KDFIter:   1000,
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Service: svc, Config: svc, File: svc, Sync: svc})
	srv := httptest.NewServer(authapi.Require(m)(mux))
	defer srv.Close()

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, b.String()
	}

	// 没设访问密码、没登录:管理接口被挡(403 要先设置密码),公开接口照常答「同步开关关着」
	if code, body := get("/api/openbox/client-config"); code != 403 || strings.Contains(body, "pairCode") {
		t.Fatalf("管理接口应被鉴权挡住且不泄露配对码: %d %s", code, body)
	}
	if code, body := get("/client/v1/config?p=0123456789abcdef"); code != 403 || !strings.Contains(body, `"error":"disabled"`) {
		t.Fatalf("公开接口应直达服务层并回 disabled: %d %s", code, body)
	}

	// 开着开关(直接走服务层,模拟面板里点过)后,不登录凭 nonce 就能同步;nonce 错回 404
	ctx := context.Background()
	if _, err := svc.SetClientConfigEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	is, _ := svc.ClientConfig(ctx)
	code, body := get("/client/v1/config?p=" + is.Nonce)
	if code != 200 {
		t.Fatalf("不登录凭 nonce 应能同步: %d %s", code, body)
	}
	var res clientapp.SyncResult
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if _, err := clientapp.OpenEnvelope(res.Seal(), is.PairCode, 1000, 1000); err != nil {
		t.Fatalf("配对码应能解开: %v", err)
	}
	if strings.Contains(body, is.PairCode) {
		t.Fatal("响应里不能出现配对码")
	}
	if code, _ := get("/client/v1/config?p=ffffffffffffffff"); code != 404 {
		t.Fatalf("nonce 错: %d", code)
	}
	if after, _ := svc.ClientConfig(ctx); after.LastSyncAt == 0 {
		t.Fatal("成功同步后 lastSyncAt 应更新")
	}
}
