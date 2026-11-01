package profile

import (
	"context"
	"fmt"
)

// OptionalKeys 是备份里「可选部分」对应的档案键:文件里没有这个键,表示「保持现状」,不是「清空」。
// 其余的键文件里没有就回到默认值(Replace 是整体替换,不是合并)。
var OptionalKeys = []string{"chainProxies", "clientRoutes", "servers"}

// buildReplace 由「现有档案 cur」和「文件里的档案 in」算出替换后的完整档案(纯计算,不落盘)。
// 规则:一、以默认档案打底再叠文件里的内容(同 Get 的合成方式);二、值为 null 的键当作没有;
// 三、OptionalKeys 文件里没有的,沿用现有的;四、走和 Patch 同一套规整与校验(rulesetDir 忽略、
// 链式代理的只读摘要按链接沿用、shareRegions / serverInfo / servers 规整、旧版分流迁移、Check)。
func (s *Service) buildReplace(ctx context.Context, cur, in map[string]any) (map[string]any, error) {
	file := make(map[string]any, len(in))
	for k, v := range in {
		if v != nil {
			file[k] = Clone(v)
		}
	}
	clean := prepare(cur, file)
	// normalizeClientApp 会摘掉 cur 里的旧 serverInfo;这里是整体替换,不带旧值,给它一份空的草稿即可。
	if err := normalizeClientApp(map[string]any{}, clean); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	if err := normalizeServers(clean); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	for _, k := range OptionalKeys {
		if _, ok := clean[k]; ok {
			continue
		}
		if v, ok := cur[k]; ok {
			clean[k] = Clone(v)
		}
	}
	next := effective(clean)
	s.migrate(ctx, next) // 旧版备份里的分流可能还带老字段
	if err := Check(next); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	return next, nil
}

// CheckReplace 只校验、不写盘:in 能不能整体替换进档案。给「先整份校验、再动手」的导入用。
func (s *Service) CheckReplace(ctx context.Context, in map[string]any) error {
	stored := map[string]any{}
	if _, err := s.st.Load(storeName, &stored); err != nil {
		return fmt.Errorf("读取档案失败: %w", err)
	}
	cur := effective(stored)
	s.migrate(ctx, cur)
	_, err := s.buildReplace(ctx, cur, in)
	return err
}

// Replace 用 in 整体替换档案并保存,返回替换后的完整档案。规则见 buildReplace。
// 校验失败返回 *ValidationError,已保存的内容不动。
func (s *Service) Replace(ctx context.Context, in map[string]any) (map[string]any, error) {
	var result map[string]any
	stored := map[string]any{}
	err := s.st.Update(storeName, &stored, func() error {
		cur := effective(stored)
		s.migrate(ctx, cur)
		next, err := s.buildReplace(ctx, cur, in)
		if err != nil {
			return err
		}
		stored, result = next, next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
