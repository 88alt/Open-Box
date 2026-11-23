package profile

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestUpdatePolicies(t *testing.T) {
	svc, st := newSvc(t, nil)
	ctx := context.Background()

	// 全新档案带默认站点集:change 拿到它的副本,返回的新列表整个换掉,档案别的字段不动
	got, err := svc.UpdatePolicies(ctx, func(list []any) ([]any, error) {
		if len(list) != len(DefaultRouting()["policies"].([]any)) {
			t.Errorf("应拿到默认站点集,得到 %#v", list)
		}
		return []any{map[string]any{"id": "p1", "name": "甲", "domain": []any{"a.com"}}}, nil
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("UpdatePolicies = %v, %v", got, err)
	}
	prof, _ := svc.Get(ctx)
	if prof["region"] != Default()["region"] {
		t.Errorf("档案别的字段被动了")
	}
	pol := prof["routing"].(map[string]any)["policies"].([]any)
	if !reflect.DeepEqual(pol[0].(map[string]any)["domain"], []any{"a.com"}) {
		t.Errorf("没存上: %v", pol)
	}

	// change 拿到的是副本:改了又返回错误,存下的不变
	boom := errors.New("boom")
	if _, err := svc.UpdatePolicies(ctx, func(list []any) ([]any, error) {
		list[0].(map[string]any)["name"] = "被改了"
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	prof, _ = svc.Get(ctx)
	if n := prof["routing"].(map[string]any)["policies"].([]any)[0].(map[string]any)["name"]; n != "甲" {
		t.Errorf("出错不该留下改动,名字 = %v", n)
	}

	// 返回 nil 当作清空
	if _, err := svc.UpdatePolicies(ctx, func([]any) ([]any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	prof, _ = svc.Get(ctx)
	if n := len(prof["routing"].(map[string]any)["policies"].([]any)); n != 0 {
		t.Errorf("应清空,还剩 %d 个", n)
	}
	_ = st
}
