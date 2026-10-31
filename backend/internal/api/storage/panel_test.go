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
