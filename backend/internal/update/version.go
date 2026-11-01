// Package update 是「组件更新」(前端 OpenboxUpdateCard)的后端部分。
//
// 本包目前做只读的三件事(自己的设计,依据前端 api/openbox.ts 的 OpenboxUpdateStatus / OpenboxUpdateNotes 类型和注释、
// composables/openboxUpdate.ts 与 OpenboxUpdateCard.vue 的用法、GitHub 官方 REST API 文档的 Releases 一节,没有参照上游):
//   - 当前状态(版本、内核版本、安装通道、升级进度);
//   - 检查有没有新版(读仓库的公开发布列表);
//   - 取某一版的更新说明。
//
// 真正的「下载 → 校验 → 解包 → 替换文件 → 重启」依赖 M6 打包定下来的发布包格式和安装目录,不在本包里;
// 没有它之前 run 如实回 501,cancel 回「没有在升级」。
package update

import (
	"regexp"
	"strconv"
)

// Version 是解析后的版本号:v<主>.<次>.<修订>[-main.<n>]。
// 过渡期的发布版本号是 v0.1.<上游号>-main.<n>(见 DESIGN 第 8 节),所以 -main.<n> 要认;
// 没有后缀的当作 n = 0(上游号相同时,带 -main 的自研构建比不带的新)。
type Version struct {
	Major, Minor, Patch, Main int
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-main\.(\d+))?$`)

// ParseVersion 解析版本号;认不出来(例如 "dev"、"v1.2")返回 ok = false。
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	n := func(i int) int {
		if m[i] == "" {
			return 0
		}
		v, _ := strconv.Atoi(m[i])
		return v
	}
	return Version{n(1), n(2), n(3), n(4)}, true
}

// Compare 返回 -1 / 0 / 1:v 比 o 旧 / 相同 / 新。
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}, {v.Main, o.Main}} {
		switch {
		case p[0] < p[1]:
			return -1
		case p[0] > p[1]:
			return 1
		}
	}
	return 0
}
