package storage

import (
	"context"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// Panel 把面板设置和背景图的读取方法给别的功能用(备份导出)。
type Panel struct{ Store *store.Store }

// Settings 返回面板设置(config/ 开头的键值)。名字里带 password 的键一律不给——备份文件要能随便存放,
// 访问密码不在这里,这是个保险。没有设置时返回空 map(不是 nil)。
func (p Panel) Settings(_ context.Context) (map[string]string, error) {
	f := settingsFile{}
	if _, err := p.Store.Load(settingsName, &f); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(f.Entries))
	for k, v := range f.Entries {
		if strings.Contains(strings.ToLower(k), "password") {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// BackgroundImage 返回背景图(data URL),没有是空串。
func (p Panel) BackgroundImage(_ context.Context) (string, error) {
	f := imageFile{}
	if _, err := p.Store.Load(imageName, &f); err != nil {
		return "", err
	}
	return f.Image, nil
}
