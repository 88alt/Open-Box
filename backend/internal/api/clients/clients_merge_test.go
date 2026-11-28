package clients

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func get(t *testing.T, d Deps) []map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/clients", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 %d:%s", rec.Code, rec.Body.String())
	}
	v := decode(t, rec.Body.Bytes())
	raw, _ := v["clients"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		m, _ := x.(map[string]any)
		out = append(out, m)
	}
	return out
}

// 租约里的设备照旧带着名字与 MAC;流量里出现过但没租约的补成只有 IP 的条目。
func TestClientsMergeTrafficSources(t *testing.T) {
	cs := get(t, Deps{
		List: func() []system.Client {
			return []system.Client{{IP: "192.168.2.50", Name: "laptop", MAC: "aa:bb:cc:dd:ee:ff"}}
		},
		Sources: func() []string { return []string{"192.168.2.50", "192.168.2.77", "10.0.0.9"} },
	})
	if len(cs) != 3 {
		t.Fatalf("应有 3 台(1 个租约 + 2 个只在流量里):%v", cs)
	}
	if cs[0]["ip"] != "192.168.2.50" || cs[0]["name"] != "laptop" || cs[0]["mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("租约那条要保留原文:%v", cs[0])
	}
	// 重复的 IP 不能出现两次
	seen := map[string]int{}
	for _, c := range cs {
		seen[c["ip"].(string)]++
	}
	for ip, n := range seen {
		if n > 1 {
			t.Fatalf("%s 出现了 %d 次", ip, n)
		}
	}
	// 补进来的只有 IP:name 是空串(前端类型里 name 必填,所以保留这个键),没有 MAC
	for _, c := range cs {
		if c["ip"] == "192.168.2.77" {
			if c["name"] != "" {
				t.Fatalf("补进来的 name 应为空:%v", c)
			}
			if _, has := c["mac"]; has {
				t.Fatalf("补进来的不该有 MAC:%v", c)
			}
		}
	}
}

// 没接 Sources 时行为与以前完全一致(只有租约)。
func TestClientsWithoutSources(t *testing.T) {
	cs := get(t, Deps{List: func() []system.Client { return []system.Client{{IP: "1.2.3.4", Name: "x"}} }})
	if len(cs) != 1 || cs[0]["ip"] != "1.2.3.4" {
		t.Fatalf("%v", cs)
	}
}

// 两路都没有时回空数组,不是 null。
func TestClientsEmptyIsArray(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Deps{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/clients", nil))
	body := rec.Body.String()
	if !contains(body, `"clients":[]`) {
		t.Fatalf("空的时候要给数组不是 null:%s", body)
	}
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("响应不是 JSON 对象:%v\n%s", err, b)
	}
	return v
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// 路由器自己的地址不进已知终端列表:它是来源之一,但不是一个可选的分流对象。
func TestClientsSkipsSelfAddress(t *testing.T) {
	cs := get(t, Deps{
		Sources: func() []string { return []string{"192.168.2.5", "192.168.2.77"} },
		Self: func(addr string) (string, string, bool) {
			if addr == "192.168.2.5" {
				return "lan", "br-lan", true
			}
			return "", "", false
		},
	})
	if len(cs) != 1 || cs[0]["ip"] != "192.168.2.77" {
		t.Fatalf("本机地址应被滤掉,只剩真终端:%v", cs)
	}
}
