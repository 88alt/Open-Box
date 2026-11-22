package businesspref

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubGroups struct {
	groups []any
	err    error
}

func (s stubGroups) Groups(context.Context) ([]any, error) { return s.groups, s.err }

func do(t *testing.T, d Deps) (int, response) {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/business-preference/status", nil))
	var got response
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
	}
	return rec.Code, got
}

// 没有分组服务:空列表 + 1 小时窗口,不是错误(面板显示「暂无分组开启」)
func TestNoGroupsService(t *testing.T) {
	code, got := do(t, Deps{})
	if code != http.StatusOK || got.WindowMinutes != 60 || len(got.Groups) != 0 {
		t.Fatalf("got %d %+v", code, got)
	}
	if got.Groups == nil {
		t.Fatal("groups 应该是空数组而不是 null")
	}
}

// 只挑开着业务优选的自动类分组;选择器 / 没开标记 / 静态组都不算
func TestOnlyEnabledAutomaticGroups(t *testing.T) {
	code, got := do(t, Deps{Groups: stubGroups{groups: []any{
		map[string]any{"id": "g1", "name": "自动优选", "type": "urltest", "businessPreference": true},
		map[string]any{"id": "g2", "name": "故障转移", "type": "failover", "businessPreference": true},
		map[string]any{"id": "g3", "name": "负载均衡", "type": "loadbalance", "businessPreference": true},
		map[string]any{"id": "g4", "name": "没开", "type": "urltest"},
		map[string]any{"id": "g5", "name": "选择器", "type": "selector", "businessPreference": true},
		map[string]any{"id": "g6", "name": "标记不是布尔", "type": "urltest", "businessPreference": "true"},
		"不是对象",
	}}})
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	if len(got.Groups) != 3 {
		t.Fatalf("应该只有 3 个,got %+v", got.Groups)
	}
	for i, want := range []string{"g1", "g2", "g3"} {
		g := got.Groups[i]
		if g.ID != want {
			t.Fatalf("第 %d 个是 %q,want %q", i, g.ID, want)
		}
		// 统计读不到:次数为 null、状态 unavailable、没有 last
		if g.Reliability != nil || g.Performance != nil {
			t.Fatalf("%s 的次数应该是 null: %+v", g.ID, g)
		}
		if g.State != "unavailable" {
			t.Fatalf("%s 状态应该是 unavailable: %+v", g.ID, g)
		}
		if g.Last != nil {
			t.Fatalf("%s 不该有 last: %+v", g.ID, g)
		}
	}
}

// 读分组失败:500,面板走「暂时无法读取内核状态」那一支
func TestGroupsError(t *testing.T) {
	code, _ := do(t, Deps{Groups: stubGroups{err: errors.New("读不到")}})
	if code != http.StatusInternalServerError {
		t.Fatalf("状态码 %d", code)
	}
}
