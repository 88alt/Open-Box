//go:build !linux

package update

import "errors"

// FreeSpace 在非 linux 上没有实现:升级只跑在路由器 / Debian 上,开发机上量不出来就跳过空间检查
// (checkSpace 拿到错误时不拦,免得明明有空间却升不了级)。
func FreeSpace(dir string) (int64, error) {
	return 0, errors.New("这个平台上量不出可用空间")
}
