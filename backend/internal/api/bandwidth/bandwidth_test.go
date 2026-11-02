package bandwidth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/bandwidth"
)

func TestGetStatus(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: &bandwidth.Service{
		Profile: func(context.Context) (map[string]any, error) { return map[string]any{}, nil },
		Config: func() (map[string]any, error) {
			return map[string]any{"outbounds": []any{
				map[string]any{"type": "vless", "tag": "A", "server": "a"},
				map[string]any{"type": "urltest", "tag": "U", "bandwidth": true, "outbounds": []any{"A"}},
			}}, nil
		},
	}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/openbox/bandwidth", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("code=%d headers=%v", rec.Code, rec.Header())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	set := got["settings"].(map[string]any)
	if set["intervalHours"] != float64(2) || set["maxDownloadMB"] != float64(64) || got["nodes"] != float64(1) || got["dailyBytes"] != float64(64*1_000_000*12) || got["running"] != false || got["lastError"] != "" {
		t.Errorf("%v", got)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/openbox/bandwidth", nil))
	if rec.Code != 405 {
		t.Errorf("POST 应 405: %d", rec.Code)
	}
}
