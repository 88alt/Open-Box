//go:build windows

package update

import "errors"

// startDetached 在 Windows 上没有实现:在线升级只跑在路由器 / Debian 上,
// 开发机上不会走到提交这一步(没有安装根就不会提交)。
func startDetached(script string) error {
	return errors.New("这个平台上不支持脱离进程的看门狗")
}
