package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type fakeSelectionsStore struct {
	data  map[string]any
	err   error
	saves int
}

func (f *fakeSelectionsStore) Load(name string, v any) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.data == nil {
		return false, nil
	}
	b, err := json.Marshal(f.data)
	if err != nil {
		return false, err
	}
	out, _ := v.(*map[string]any)
	return true, json.Unmarshal(b, out)
}

func (f *fakeSelectionsStore) Save(name string, v any) error {
	f.saves++
	if f.err != nil {
		return f.err
	}
	m, _ := v.(map[string]any)
	f.data = m
	return nil
}

func TestSelectionsStoreGetFiltersBadValues(t *testing.T) {
	st := &SelectionsStore{Store: &fakeSelectionsStore{data: map[string]any{
		"PROXY": "n1", "DIRECT": "", "数字": 5.0, "": "x",
	}}}
	got := st.Get()
	want := map[string]any{"PROXY": "n1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v,想要 %v", got, want)
	}
}

func TestSelectionsStoreGetTolerant(t *testing.T) {
	if got := (&SelectionsStore{}).Get(); len(got) != 0 {
		t.Errorf("没有存储时应返回空 map: %v", got)
	}
	bad := &SelectionsStore{Store: &fakeSelectionsStore{err: errors.New("文件坏了")}}
	if got := bad.Get(); len(got) != 0 {
		t.Errorf("读不出来时应返回空 map: %v", got)
	}
	empty := &SelectionsStore{Store: &fakeSelectionsStore{}}
	if got := empty.Get(); got == nil || len(got) != 0 {
		t.Errorf("没有记录时应返回空 map 而不是 nil: %v", got)
	}
}

func TestSelectionsStoreSet(t *testing.T) {
	store := &fakeSelectionsStore{}
	st := &SelectionsStore{Store: store}
	if err := st.Set(map[string]any{"PROXY": "n1", "坏值": 1.5, "DIRECT": ""}); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 {
		t.Errorf("应写一次,实际 %d", store.saves)
	}
	if !reflect.DeepEqual(store.data, map[string]any{"PROXY": "n1"}) {
		t.Errorf("写进去的是 %v", store.data)
	}
	if err := (&SelectionsStore{}).Set(nil); err == nil {
		t.Error("没有存储时应报错")
	}
}

func TestParseControllerSelections(t *testing.T) {
	body := `{"proxies":{
		"PROXY":{"type":"Selector","now":"n1"},
		"空组":{"type":"Selector","now":""},
		"n1":{"type":"Shadowsocks","now":""},
		"":{"type":"Selector","now":"x"}
	}}`
	got, err := ParseControllerSelections(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]string{"PROXY": "n1"}) {
		t.Errorf("got %v", got)
	}
	if _, err := ParseControllerSelections(strings.NewReader("不是 JSON")); err == nil {
		t.Error("坏 JSON 应报错")
	}
}

func TestControllerSelectionsHTTP(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"proxies":{"PROXY":{"type":"Selector","now":"n2"}}}`))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	got, err := ControllerSelections(context.Background(), addr, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/proxies" || gotAuth != "Bearer s3cret" {
		t.Errorf("path=%q auth=%q", gotPath, gotAuth)
	}
	if got["PROXY"] != "n2" {
		t.Errorf("got %v", got)
	}

	if _, err := ControllerSelections(context.Background(), "", "x"); err == nil {
		t.Error("没有地址应报错")
	}
	// 连不上(内核没在跑)要报错,由调用方退到快照。
	if _, err := ControllerSelections(context.Background(), "127.0.0.1:1", ""); err == nil {
		t.Error("连不上应报错")
	}
}

func TestControllerSelectionsRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := ControllerSelections(context.Background(), strings.TrimPrefix(srv.URL, "http://"), ""); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v,想要 401", err)
	}
}

func TestSelectionsToTags(t *testing.T) {
	got := SelectionsToTags(map[string]string{"PROXY": "DIRECT", "拦截": "REJECT", "别的组": "n1"},
		BuiltinNames{Direct: "直连", Block: "拦截"})
	want := map[string]any{"PROXY": "直连", "拦截": "拦截", "别的组": "n1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v,想要 %v", got, want)
	}
	// 没给内置名字时退回 direct / block。
	got = SelectionsToTags(map[string]string{"a": "DIRECT", "b": "REJECT"}, BuiltinNames{})
	if got["a"] != "direct" || got["b"] != "block" {
		t.Errorf("got %v", got)
	}
}
