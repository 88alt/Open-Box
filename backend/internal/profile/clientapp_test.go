package profile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
)

const okRegions = `[{"id":"jp","name" : " 日本 ","regions":["JP"],"rules":[{"type":"geosite","value":"cn","action":"proxy"}],"catchAll":"direct"}]`

// 校验不过:返回 *ValidationError(接口回 400),已保存的内容原样不动。
func TestPatchRejectsBadShareRegionsAndServerInfoWithoutWriting(t *testing.T) {
	svc, st := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"shareRegions":`+okRegions+`,"serverInfo":{"name":"家"}}`)); err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if _, err := st.Load(storeName, &before); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, patch, want string }{
		{"地区重复占用", `{"shareRegions":[{"id":"a","name":"a","regions":["CN"],"rules":[],"catchAll":"direct"},{"id":"x","name":"x","regions":["CN"],"rules":[],"catchAll":"direct"}]}`, "region CN is in both a and x"},
		{"没有名称", `{"shareRegions":[{"id":"a","regions":[],"rules":[],"catchAll":"direct"}]}`, "name is required"},
		{"不是数组", `{"shareRegions":{}}`, "must be an array"},
		{"名称过长", `{"serverInfo":{"name":"` + strings.Repeat("a", 41) + `"}}`, "name is longer than 40"},
		{"svg 不对", `{"serverInfo":{"iconSvg":"<p/>"}}`, "iconSvg must start with <svg"},
		{"不是对象", `{"serverInfo":[]}`, "must be an object"},
		// 同一个补丁里别的字段合法也不放行:整个补丁作废
		{"和合法字段一起", `{"ipv6":true,"serverInfo":{"region":"cn"}}`, "region must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Patch(ctx, j(t, c.patch))
			var ve *ValidationError
			if !errorsAs(err, &ve) || !strings.Contains(ve.Msg, c.want) {
				t.Fatalf("应是含 %q 的 *ValidationError: %v", c.want, err)
			}
			var after map[string]any
			if _, err := st.Load(storeName, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("校验失败不该改动已保存的内容:\n之前 %v\n之后 %v", before, after)
			}
		})
	}
}

// 合法的值会被规整后保存(去空白、补 dns 默认值),读出来就是规整后的样子。
func TestPatchStoresNormalizedShareRegions(t *testing.T) {
	svc, _ := newSvc(t, nil)
	got, err := svc.Patch(context.Background(), j(t, `{"shareRegions":`+okRegions+`}`))
	if err != nil {
		t.Fatal(err)
	}
	g := got["shareRegions"].([]any)[0].(map[string]any)
	if g["name"] != "日本" {
		t.Errorf("名称应去空白: %q", g["name"])
	}
	if d, _ := g["dns"].(map[string]any); d == nil || d["direct"] != "wan" || d["directPort"] != 53.0 {
		t.Errorf("缺的 dns 应补默认值: %v", g["dns"])
	}
	again, err := svc.Get(context.Background())
	if err != nil || !reflect.DeepEqual(again["shareRegions"], got["shareRegions"]) {
		t.Errorf("读回的和保存的不一致: %v %v", again["shareRegions"], err)
	}
}

// 恢复默认:传 [] 存成空数组(client-app/info 把它当默认三组);传 null 删掉这个键。
func TestPatchShareRegionsResetForms(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"shareRegions":`+okRegions+`}`)); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Patch(ctx, j(t, `{"shareRegions":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if arr, ok := got["shareRegions"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("[] 应存成空数组: %#v", got["shareRegions"])
	}
	if _, err := svc.Patch(ctx, j(t, `{"shareRegions":`+okRegions+`}`)); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Patch(ctx, j(t, `{"shareRegions":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["shareRegions"]; has {
		t.Fatalf("null 应删掉这个键: %v", got["shareRegions"])
	}
}

// 整份覆盖:新值里没有的字段就是没有了,{} 清空。(深度合并做不到这一点。)
func TestPatchServerInfoReplacesWholesale(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"serverInfo":{"name":"家里","icon":"US","iconSvg":"<svg/>","region":"JP"}}`)); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Patch(ctx, j(t, `{"serverInfo":{"name":"办公室"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["serverInfo"], map[string]any{"name": "办公室"}) {
		t.Fatalf("没给的字段应该没了,而不是留着旧值: %v", got["serverInfo"])
	}
	got, err = svc.Patch(ctx, j(t, `{"serverInfo":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := got["serverInfo"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("{} 应清空为全用默认: %#v", got["serverInfo"])
	}
	// 不碰 serverInfo 的补丁不会动它
	if _, err := svc.Patch(ctx, j(t, `{"serverInfo":{"name":"保留"}}`)); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Patch(ctx, j(t, `{"ipv6":true}`))
	if err != nil || got["serverInfo"].(map[string]any)["name"] != "保留" {
		t.Fatalf("无关补丁不该动 serverInfo: %v %v", got["serverInfo"], err)
	}
	// null 删掉键
	got, err = svc.Patch(ctx, j(t, `{"serverInfo":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["serverInfo"]; has {
		t.Fatalf("null 应删掉 serverInfo: %v", got["serverInfo"])
	}
}

// 档案里已有的坏数据(手工改坏的文件)不拦住不相关的保存;client-app/info 读它时有自己的降级。
func TestUnrelatedPatchNotBlockedByBadStoredClientAppData(t *testing.T) {
	svc, st := newSvc(t, nil)
	bad := map[string]any{"shareRegions": "oops", "serverInfo": []any{1}}
	if err := st.Save(storeName, bad); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Patch(context.Background(), j(t, `{"ipv6":true}`))
	if err != nil {
		t.Fatalf("坏的旧数据不该让无关保存失败: %v", err)
	}
	if got["ipv6"] != true || got["shareRegions"] != "oops" {
		t.Fatalf("无关保存不该改动这两个键: %v", got)
	}
	// 要改这两个键时,按新规则校验并覆盖掉坏数据
	got, err = svc.Patch(context.Background(), j(t, `{"shareRegions":[],"serverInfo":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if arr, ok := got["shareRegions"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("%#v", got["shareRegions"])
	}
}

// 默认三组原样保存回去不变:前端读到默认值再原样保存,不会悄悄改掉数据。
func TestSavingDefaultShareRegionsBackIsStable(t *testing.T) {
	svc, _ := newSvc(t, nil)
	in := map[string]any{"shareRegions": jsonAny(t, clientapp.DefaultShareRegions())}
	got, err := svc.Patch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jsonAny(t, got["shareRegions"]), in["shareRegions"]) {
		t.Fatalf("默认三组存回后变了:\n%v", got["shareRegions"])
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

func jsonAny(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const okServers = `[{"id":"s1","enabled":true,"name":" 客厅 ","protocol":"mixed","port":7080,"address":" router.lan "},{"id":"s2","enabled":true,"name":"SS","protocol":"shadowsocks","port":8388,"method":"aes-256-gcm","password":"pw"}]`

// 档案 servers 校验不过:返回 *ValidationError(接口回 400),已保存的内容原样不动;同一补丁里别的字段合法也作废。
func TestPatchRejectsBadServersWithoutWriting(t *testing.T) {
	svc, st := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"servers":`+okServers+`}`)); err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if _, err := st.Load(storeName, &before); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, patch, want string }{
		{"端口重复", `{"servers":[{"id":"a","name":"a","protocol":"mixed","port":7080},{"id":"b","name":"b","protocol":"mixed","port":7080}]}`, "同一个端口"},
		{"没有名称", `{"servers":[{"id":"a","protocol":"mixed","port":7080}]}`, "名称不能为空"},
		{"不是数组", `{"servers":{}}`, "必须是数组"},
		{"和合法字段一起", `{"ipv6":true,"servers":[{"id":"a","name":"a","protocol":"vless","port":1,"uuid":"x"}]}`, "uuid 格式不对"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Patch(ctx, j(t, c.patch))
			var ve *ValidationError
			if !errorsAs(err, &ve) || !strings.Contains(ve.Msg, c.want) {
				t.Fatalf("应是含 %q 的 *ValidationError: %v", c.want, err)
			}
			var after map[string]any
			if _, err := st.Load(storeName, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("校验失败不该改动已保存的内容:\n之前 %v\n之后 %v", before, after)
			}
		})
	}
}

// 合法的 servers 规整后保存并整份替换;没带 servers 的补丁不碰它(旧数据不会拦住不相关的保存);null 清掉。
func TestPatchStoresNormalizedServers(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	got, err := svc.Patch(ctx, j(t, `{"servers":`+okServers+`}`))
	if err != nil {
		t.Fatal(err)
	}
	list := got["servers"].([]any)
	s0 := list[0].(map[string]any)
	if len(list) != 2 || s0["name"] != "客厅" || s0["address"] != "router.lan" {
		t.Fatalf("应规整后保存: %v", list)
	}
	// 读回来端口仍是数字(落盘 JSON 往返)
	again, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := again["servers"].([]any)[1].(map[string]any)["port"]; p != 8388.0 {
		t.Fatalf("端口: %#v", p)
	}
	// 不带 servers 的补丁:不校验、不改
	if _, err := svc.Patch(ctx, j(t, `{"ipv6":true}`)); err != nil {
		t.Fatalf("%v", err)
	}
	// 整份替换:只剩一台
	got, err = svc.Patch(ctx, j(t, `{"servers":[{"id":"only","name":"o","protocol":"mixed","port":7081}]}`))
	if err != nil || len(got["servers"].([]any)) != 1 {
		t.Fatalf("%v %v", got["servers"], err)
	}
	// 空数组:清空
	got, err = svc.Patch(ctx, j(t, `{"servers":[]}`))
	if err != nil || len(got["servers"].([]any)) != 0 {
		t.Fatalf("%v %v", got["servers"], err)
	}
}
