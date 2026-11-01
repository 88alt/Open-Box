package storage

import (
	"context"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func TestPanelReadsSettingsWithoutPasswordsAndImage(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := Panel{Store: st}
	ctx := context.Background()
	if set, err := p.Settings(ctx); err != nil || set == nil || len(set) != 0 {
		t.Fatalf("没有设置时要是空 map:%v %v", set, err)
	}
	if img, err := p.BackgroundImage(ctx); err != nil || img != "" {
		t.Fatalf("%q %v", img, err)
	}
	if err := st.Save(settingsName, settingsFile{Entries: map[string]string{
		"config/theme": "dark", "config/Access-Password-Hint": "x", "config/lang": "zh",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(imageName, imageFile{Image: "data:image/png;base64,AAAA"}); err != nil {
		t.Fatal(err)
	}
	set, err := p.Settings(ctx)
	if err != nil || len(set) != 2 || set["config/theme"] != "dark" || set["config/lang"] != "zh" {
		t.Fatalf("名字带 password 的键不该给:%v %v", set, err)
	}
	if img, _ := p.BackgroundImage(ctx); img != "data:image/png;base64,AAAA" {
		t.Fatalf("%q", img)
	}
}

func TestCleanSettingsDropsWhatPutStorageWouldDropAndPasswords(t *testing.T) {
	got, err := CleanSettings(map[string]any{
		"config/theme":                         "dark",
		"config/lang":                          "zh",
		"other/x":                              "前缀不对",
		"config/number":                        5,
		"config/access-password-authenticated": "true",
		"config/Access-Password-Hint":          "x",
		"config/" + string(make([]byte, 300)):  "键太长",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["config/theme"] != "dark" || got["config/lang"] != "zh" {
		t.Fatalf("只该留下合规的两条: %v", got)
	}
	big := make(map[string]any, maxEntries+1)
	for i := 0; i <= maxEntries; i++ {
		big["config/"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))] = "x"
	}
	if _, err := CleanSettings(big); err != ErrTooManySettings {
		t.Fatalf("条目太多应被拒绝: %v", err)
	}
}

func TestReplaceSettingsKeepsStoredPasswordKeysAndReplacesTheRest(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	p := Panel{Store: st}
	ctx := context.Background()
	if err := st.Save(settingsName, settingsFile{Entries: map[string]string{
		"config/theme": "dark", "config/old": "gone", "config/access-password-hint": "keep-me",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := p.ReplaceSettings(ctx, map[string]string{"config/theme": "light", "config/new": "1"}); err != nil {
		t.Fatal(err)
	}
	f := settingsFile{}
	if _, err := st.Load(settingsName, &f); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"config/theme": "light", "config/new": "1", "config/access-password-hint": "keep-me"}
	if len(f.Entries) != len(want) {
		t.Fatalf("得到 %v", f.Entries)
	}
	for k, v := range want {
		if f.Entries[k] != v {
			t.Fatalf("%s: 想要 %q 得到 %q(%v)", k, v, f.Entries[k], f.Entries)
		}
	}
	// 导出仍看不到名字带 password 的键
	if set, _ := p.Settings(ctx); len(set) != 2 {
		t.Fatalf("导出不该带 password 键: %v", set)
	}
	// 空快照:清掉普通设置,密码键仍在
	if err := p.ReplaceSettings(ctx, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	f = settingsFile{}
	_, _ = st.Load(settingsName, &f)
	if len(f.Entries) != 1 || f.Entries["config/access-password-hint"] != "keep-me" {
		t.Fatalf("%v", f.Entries)
	}
}

func TestSetBackgroundImageValidates(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	p := Panel{Store: st}
	ctx := context.Background()
	for _, bad := range []string{"", "http://x/y.png", "data:text/html;base64,AAAA"} {
		if err := p.SetBackgroundImage(ctx, bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	if err := p.SetBackgroundImage(ctx, "data:image/png;base64,"+string(make([]byte, maxImageBody))); err == nil {
		t.Error("超过上限应被拒绝")
	}
	if img, _ := p.BackgroundImage(ctx); img != "" {
		t.Fatalf("被拒绝的不该落盘: %q", img)
	}
	if err := p.SetBackgroundImage(ctx, "data:image/png;base64,AAAA"); err != nil {
		t.Fatal(err)
	}
	if img, _ := p.BackgroundImage(ctx); img != "data:image/png;base64,AAAA" {
		t.Fatalf("%q", img)
	}
}
