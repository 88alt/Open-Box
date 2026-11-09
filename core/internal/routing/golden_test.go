package routing

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

type goldenCase struct {
	Fn   string            `json:"fn"`
	Args []json.RawMessage `json:"args"`
	Want any               `json:"want"`
	FP   string            `json:"fp"`
	Geo  *[]string         `json:"geoTags"`
}

// 金标准来自上游 engine 的明文实现(见 testdata/gen-golden.mjs):同一份输入,Go 的输出必须一致。
func TestGoldenAgainstUpstream(t *testing.T) {
	b, err := os.ReadFile("../../testdata/routing_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Cases []goldenCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	reg := protocol.NewRegistry()
	Register(reg)

	bad, skipped := 0, 0
	for i, c := range g.Cases {
		// 有意的差异:前置自定义分流里网段写错的 ipCidr 行,整理时直接丢掉(基线原样保留)。
		// 这类用例的期望值与指纹是旧行为,不再逐条比对;新行为由 model_vectors_test.go 自写向量覆盖。
		if c.Fn == "normalizeRouting" && hasBadCustomCidr(c.Args) {
			skipped++
			continue
		}
		req, _ := json.Marshal(map[string]any{"protocol": 1, "method": c.Fn, "args": c.Args, "geoTags": c.Geo})
		got := call(t, reg, req)
		if !reflect.DeepEqual(got, c.Want) {
			if bad++; bad <= 5 {
				gb, _ := json.Marshal(got)
				wb, _ := json.Marshal(c.Want)
				in, _ := json.Marshal(c.Args)
				t.Errorf("#%d %s 输出不一致\n输入: %s\n有: %s\n要: %s", i, c.Fn, in, gb, wb)
			}
		}
		if c.Fn == "normalizeRouting" {
			req, _ := json.Marshal(map[string]any{"protocol": 1, "method": "routingFingerprint", "args": c.Args, "geoTags": c.Geo})
			if fp := call(t, reg, req); fp != c.FP {
				if bad++; bad <= 5 {
					in, _ := json.Marshal(c.Args)
					t.Errorf("#%d 指纹不一致: 有 %v 要 %s\n输入: %s", i, fp, c.FP, in)
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("共 %d 条不一致(总 %d 条)", bad, len(g.Cases))
	}
	t.Logf("%d 条金标准全部一致(另有 %d 条含非法 ipCidr 自定义行,按有意差异跳过)", len(g.Cases)-skipped, skipped)
}

// hasBadCustomCidr 判断 normalizeRouting 的入参里,custom.rules 是否有网段写法不合法的 ipCidr 行。
func hasBadCustomCidr(args []json.RawMessage) bool {
	if len(args) == 0 {
		return false
	}
	var in struct {
		Custom struct {
			Rules []any `json:"rules"`
		} `json:"custom"`
	}
	if json.Unmarshal(args[0], &in) != nil {
		return false
	}
	for _, raw := range in.Custom.Rules {
		r, ok := raw.(map[string]any)
		if !ok || r["type"] != "ipCidr" {
			continue
		}
		if v, ok := r["value"].(string); ok {
			if validIPCidr(jsTrim(v)) {
				continue
			}
		}
		return true
	}
	return false
}

// call 走完整的协议入口,和 Node 的调用方式一致,返回 value 解码后的值。
func call(t *testing.T, reg *protocol.Registry, req []byte) any {
	t.Helper()
	var out bytes.Buffer
	protocol.Serve(bytes.NewReader(req), &out, reg)
	var resp struct {
		OK    bool   `json:"ok"`
		Value any    `json:"value"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v\n%s", err, out.String())
	}
	if !resp.OK {
		t.Fatalf("调用失败: %s", resp.Error)
	}
	return resp.Value
}

func TestListTagForUrlMatchesNode(t *testing.T) {
	// 期望值由旧 JS 的 listTagForUrl 算出(含非 ASCII 和需要代理对的字符)
	for in, want := range map[string]string{
		"":                             "",
		"   ":                          "",
		" https://example.com/a.list ": "list-1aea4a3b",
		"https://例え.jp/😀.list":         "list-959f1739",
	} {
		if got := ListTagForUrl(in); got != want {
			t.Errorf("ListTagForUrl(%q)=%q 要 %q", in, got, want)
		}
	}
}
