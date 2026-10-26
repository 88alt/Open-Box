package profile

// Merge 把 patch 深度合并到 base 上,返回新值(不改入参):
// 对象逐键递归合并;数组和标量整体替换;patch 里的 null 表示删除该键。
func Merge(base, patch map[string]any) map[string]any {
	out := Clone(base).(map[string]any)
	mergeInto(out, patch)
	return out
}

func mergeInto(dst, patch map[string]any) {
	for k, pv := range patch {
		if pv == nil {
			delete(dst, k)
			continue
		}
		pm, pIsMap := pv.(map[string]any)
		dm, dIsMap := dst[k].(map[string]any)
		if pIsMap && dIsMap {
			mergeInto(dm, pm)
			continue
		}
		dst[k] = Clone(pv)
	}
}
