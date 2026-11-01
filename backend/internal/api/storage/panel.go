package storage

import (
	"context"
	"errors"
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

// hasPassword:名字带 password 的设置键不进备份(导出时去掉),导入时也不收、不动。
func hasPassword(k string) bool { return strings.Contains(strings.ToLower(k), "password") }

// ErrTooManySettings:备份里的面板设置条目太多。
var ErrTooManySettings = errors.New("备份里的面板设置条目太多")

// CleanSettings 把备份里的面板设置整理成能保存的快照:不合规的条目(前缀不是 config/、值不是字符串、
// 键过长、遗留的登录状态键、名字带 password)直接丢弃,与 PUT /api/storage 的做法一致。条目太多返回
// ErrTooManySettings。纯计算,不碰存储。
func CleanSettings(in map[string]any) (map[string]string, error) {
	if len(in) > maxEntries {
		return nil, ErrTooManySettings
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := cleanEntry(k, v); ok && !hasPassword(k) {
			out[k] = s
		}
	}
	return out, nil
}

// CheckImage 校验背景图能不能存:data:image/ 开头,不超过上限。
func CheckImage(img string) error {
	if !strings.HasPrefix(img, imagePrefix) {
		return errors.New("背景图必须是 data:image/ 开头的图片数据")
	}
	if len(img) > maxImageBody {
		return errors.New("背景图太大了")
	}
	return nil
}

// ReplaceSettings 用 clean(CleanSettings 的结果)整体替换面板设置。已保存的设置里名字带 password 的键
// 原样保留(备份文件里本来就不会有它们,替换不该把它们删掉)。
func (p Panel) ReplaceSettings(_ context.Context, clean map[string]string) error {
	f := settingsFile{}
	return p.Store.Update(settingsName, &f, func() error {
		next := make(map[string]string, len(clean))
		for k, v := range f.Entries {
			if hasPassword(k) {
				next[k] = v
			}
		}
		for k, v := range clean {
			next[k] = v
		}
		f.Entries = next
		return nil
	})
}

// SetBackgroundImage 保存背景图(先过 CheckImage)。
func (p Panel) SetBackgroundImage(_ context.Context, img string) error {
	if err := CheckImage(img); err != nil {
		return err
	}
	return p.Store.Save(imageName, imageFile{Image: img})
}

// CleanSettings 是包级函数 CleanSettings 的方法形式(备份导入的依赖接口用)。
func (Panel) CleanSettings(in map[string]any) (map[string]string, error) { return CleanSettings(in) }

// CheckImage 是包级函数 CheckImage 的方法形式。
func (Panel) CheckImage(img string) error { return CheckImage(img) }
