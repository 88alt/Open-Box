package profile

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestReplaceIsWholesaleButKeepsAbsentOptionalParts(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"ipv6":true,"testUrl":"https://a.test/204","chainProxies":[{"id":"c1","name":"链","link":"ss://x","upstream":"u","enabled":true}],"clientRoutes":[{"id":"r1"}],"servers":[]}`)); err != nil {
		t.Fatal(err)
	}
	// 文件里没有 ipv6 / testUrl(整体替换,回默认),也没有三个可选部分(沿用现状)
	got, err := svc.Replace(ctx, j(t, `{"region":"intl","dns":{"mode":"hijack"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got["region"] != "intl" || got["dns"].(map[string]any)["mode"] != "hijack" {
		t.Fatalf("文件里的内容没生效: %v", got)
	}
	if got["ipv6"] != false || got["testUrl"] != Default()["testUrl"] {
		t.Fatalf("文件里没有的键应回默认值: ipv6=%v testUrl=%v", got["ipv6"], got["testUrl"])
	}
	if got["dns"].(map[string]any)["direct"] != WAN {
		t.Fatalf("dns 里没写的字段应由默认值补上: %v", got["dns"])
	}
	if l, _ := got["chainProxies"].([]any); len(l) != 1 {
		t.Fatalf("文件没带 chainProxies 应保持现状: %v", got["chainProxies"])
	}
	if l, _ := got["clientRoutes"].([]any); len(l) != 1 {
		t.Fatalf("文件没带 clientRoutes 应保持现状: %v", got["clientRoutes"])
	}
	// 落盘后再读一样
	again, err := svc.Get(ctx)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("落盘与返回不一致: %v %v", err, again)
	}
}

func TestReplaceFileOptionalPartsOverrideAndNullMeansAbsent(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"clientRoutes":[{"id":"r1"}]}`)); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Replace(ctx, j(t, `{"clientRoutes":[],"chainProxies":null,"ipv6":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := got["clientRoutes"].([]any); !ok || len(l) != 0 {
		t.Fatalf("文件里的空数组应清空: %v", got["clientRoutes"])
	}
	if got["ipv6"] != false {
		t.Fatalf("null 当作没有,回默认: %v", got["ipv6"])
	}
}

func TestReplaceInvalidChangesNothing(t *testing.T) {
	svc, st := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"ipv6":true}`)); err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if _, err := st.Load(storeName, &before); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"档案校验":         `{"region":"mars"}`,
		"servers 校验":   `{"servers":[{"id":"a","name":"","protocol":"mixed","port":1080}]}`,
		"shareRegions": `{"shareRegions":[{"id":"BAD ID"}]}`,
	} {
		_, err := svc.Replace(ctx, j(t, body))
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: 应是 ValidationError,得到 %v", name, err)
		}
		if err := svc.CheckReplace(ctx, j(t, body)); !errors.As(err, &ve) {
			t.Fatalf("%s: CheckReplace 也应拒绝,得到 %v", name, err)
		}
	}
	var after map[string]any
	if _, err := st.Load(storeName, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("校验不过不该改动已保存的内容\n前 %v\n后 %v", before, after)
	}
}

func TestCheckReplaceDoesNotWriteAndAgreesWithReplace(t *testing.T) {
	svc, st := newSvc(t, nil)
	ctx := context.Background()
	if err := svc.CheckReplace(ctx, j(t, `{"ipv6":true}`)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Load(storeName, &map[string]any{}); ok {
		t.Fatal("CheckReplace 不该写文件")
	}
}

func TestReplaceIgnoresRulesetDirAndKeepsUnknownKeys(t *testing.T) {
	svc, _ := newSvc(t, nil)
	got, err := svc.Replace(context.Background(), j(t, `{"rulesetDir":"/evil","someFutureKey":{"k":"v"},"serverInfo":{"name":"家里"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["rulesetDir"]; has {
		t.Fatalf("rulesetDir 由服务端决定,不收文件里的: %v", got["rulesetDir"])
	}
	if got["someFutureKey"].(map[string]any)["k"] != "v" {
		t.Fatalf("未知键应原样保留: %v", got)
	}
	if got["serverInfo"].(map[string]any)["name"] != "家里" {
		t.Fatalf("serverInfo 没存上: %v", got["serverInfo"])
	}
}

func TestReplaceDropsOldServerInfoAndKeepsChainSummaryByLink(t *testing.T) {
	svc, _ := newSvc(t, nil)
	ctx := context.Background()
	if _, err := svc.Patch(ctx, j(t, `{"serverInfo":{"name":"旧","region":"JP"},"chainProxies":[{"id":"c1","name":"链","link":"ss://x","upstream":"u","enabled":true}]}`)); err != nil {
		t.Fatal(err)
	}
	// 摘要是服务端按 link 解析出来的只读内容:这里手工放一份,验证 link 没变时沿用、变了丢弃
	svc2 := svc
	cur, _ := svc2.Get(ctx)
	cur["chainProxies"].([]any)[0].(map[string]any)["node"] = map[string]any{"server": "s"}
	if err := svc2.st.Save(storeName, cur); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Replace(ctx, j(t, `{"chainProxies":[{"id":"c1","name":"链","link":"ss://x","upstream":"u","enabled":true,"node":{"server":"伪造"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["serverInfo"]; has {
		t.Fatalf("文件里没有 serverInfo,不该留着旧的: %v", got["serverInfo"])
	}
	node := got["chainProxies"].([]any)[0].(map[string]any)["node"].(map[string]any)
	if node["server"] != "s" {
		t.Fatalf("摘要应沿用服务端自己的,不收文件里的: %v", node)
	}
	got, err = svc.Replace(ctx, j(t, `{"chainProxies":[{"id":"c1","name":"链","link":"ss://changed","upstream":"u","enabled":true,"node":{"server":"伪造"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["chainProxies"].([]any)[0].(map[string]any)["node"]; has {
		t.Fatal("链接变了,旧摘要应丢弃")
	}
}

func TestReplaceMigratesLegacyRouting(t *testing.T) {
	core := &fakeCore{}
	svc, _ := newSvc(t, core)
	got, err := svc.Replace(context.Background(), j(t, `{"routing":{"regions":[{"id":"x"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if core.migrateCalls.Load() == 0 {
		t.Fatal("旧版备份里的分流应走迁移")
	}
	if got["routing"].(map[string]any)["policies"].([]any)[0].(map[string]any)["id"] != "legacy-1" {
		t.Fatalf("没有迁移: %v", got["routing"])
	}
}
