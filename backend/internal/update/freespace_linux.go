//go:build linux

package update

import "syscall"

// FreeSpace 返回 dir 所在文件系统还能给普通用户用的字节数。
func FreeSpace(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
